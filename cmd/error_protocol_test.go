package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
	"github.com/spf13/cobra"
)

// The error path's byte-level baseline.
//
// Every case runs the REAL Execute() in a re-exec of this test binary — the
// model probeFail established in internal/output. Only a separate process sees
// output.Err() on its real stderr, the exit code and stdout together, and a
// driver that calls rootCmd.Execute() in-process sees none of the three: it
// reads cobra's own buffers, which the root's print point never writes to.
//
// A case may name a stub set. The child is a fresh process, so a seam
// assigned in the parent never reaches it; the name crosses the boundary in
// the environment and the child assigns the seams itself, before Execute().
//
// testdata/error-protocol.golden holds each case's exit code and both
// streams, first recorded from the tree before phase 1 changed anything; a
// commit that changes it names each case it changes (`git log -p` on the
// file shows them). Re-record by naming the cases the change rewrites or
// adds, comma-separated:
//
//	go test ./cmd -run '^TestErrorOutputBaseline$' -update-error-baseline=<case>,<case>
//
// Any other case whose rendering differs from its record fails the run and
// nothing is written, so a re-record cannot carry a change nobody named.

var updateErrorBaseline = flag.String("update-error-baseline", "",
	"comma-separated names of the cases testdata/error-protocol.golden may rewrite or add")

const (
	executeChildEnv   = "WS_TEST_EXECUTE_CHILD"
	executeArgsEnv    = "WS_TEST_EXECUTE_ARGS"
	executeStubEnv    = "WS_TEST_EXECUTE_STUB"
	errorBaselineFile = "testdata/error-protocol.golden"

	// childHome is deliberately a path that does not exist: every directory
	// the CLI derives from $HOME resolves under it, so a message that names
	// one is the same on every machine, and nothing the CLI reads is there.
	childHome = "/nonexistent/ws-error-baseline"

	// emptyDirArg in a case's args is replaced by an empty temporary
	// directory, for commands that need a real path whose name they never
	// print.
	emptyDirArg = "{EMPTY_DIR}"
)

// errorCase is one invocation of the CLI.
type errorCase struct {
	name string   // stable key in the golden file
	args []string // argv after "ws"
	stub string   // stub set the child installs; "" for none

	// workspace, when set, is created under WORKSPACES_DIR before the run.
	workspace string

	// legacyXray runs the child in a fresh directory holding a legacy
	// regular-file home/.config/xray/config.json, with HOME=home. HOME is
	// relative on purpose: the refusal that state provokes names the file,
	// and an absolute temporary path would differ in length from machine to
	// machine, and so would where the message wraps.
	legacyXray bool
}

// errorCases covers the four branches of the root protocol, the argument
// and runtime halves of the usage distinction, a failed step, whose error
// the spinner used to print a second time, and every helper and in-body exit
// that phase 1 moves onto a returned value and a hermetic process can reach.
var errorCases = []errorCase{
	// A successful command: no error text, exit 0.
	{name: "success/detect", args: []string{"detect", emptyDirArg}},

	// Argument errors print their usage lines — the synopsis and where to
	// read the help of the command they name — under the ✗ line. One on a
	// command that already returns its error, one on a body phase 1 converts,
	// one cobra raises before any command is found, one raised while parsing
	// flags.
	{name: "arg-error/runE-command", args: []string{"proxy", "profile", "use"}},
	{name: "arg-error/converted-body", args: []string{"start"}},
	{name: "arg-error/unknown-command", args: []string{"nosuch"}},
	{name: "arg-error/unknown-flag", args: []string{"start", "--bogus", "wsx"}},
	// An extra argument to a NoArgs command carries cobra's "unknown command"
	// text too. It is the leaf's usage error, not the root's: its lines name
	// ws status. This case is the other side of arg-error/unknown-command.
	{name: "arg-error/noargs-extra", args: []string{"status", "x"}},
	// The root's own usage errors: an unknown command with suggestions, one
	// with --help after it, which cobra rejects before it looks at --help,
	// and a flag the root cannot parse.
	{name: "arg-error/unknown-command-suggested", args: []string{"lst"}},
	{name: "arg-error/unknown-command-with-help", args: []string{"nosuch", "--help"}},
	{name: "arg-error/root-unknown-flag", args: []string{"--bogus"}},
	// A word under a command group that names none of its subcommands: the
	// help function turns it into a usage error of the group.
	{name: "arg-error/proxy-unknown-subcommand", args: []string{"proxy", "zzz"}},
	{name: "arg-error/vault-unknown-subcommand", args: []string{"vault", "zzz"}},
	{name: "arg-error/profile-unknown-subcommand", args: []string{"proxy", "profile", "zzz"}},
	// ws help with a topic that names no command answers as ws <topic> does.
	{name: "arg-error/help-unknown-topic", args: []string{"help", "nosuch"}},
	// A shell completion does not support.
	{name: "arg-error/completion-unsupported-shell", args: []string{"completion", "powershell"}},

	// A runtime error prints no usage lines. A plain error returned from a
	// RunE body, on a path with no spinner.
	{name: "runtime/runE-plain", args: []string{"proxy", "profile", "use", "x", "--no-migrate"}, stub: "proxy-not-ready"},

	// Paths output.Die printed before phase 1, with no spinner — one for each
	// file whose Die sites a hermetic process can reach: an error passed
	// through, a wrapped one, a literal, a literal in a switch's default.
	{name: "runtime/die-non-spinner", args: []string{"profile-create", "Bad Name"}},
	{name: "runtime/proxy-profile-current", args: []string{"proxy", "profile", "current", "--no-migrate"}},
	{name: "runtime/proxy-profile-show-missing", args: []string{"proxy", "profile", "show", "nope", "--no-migrate"}},
	{name: "runtime/proxy-init-no-scheme", args: []string{"proxy", "init", "nosuch"}},
	{name: "runtime/proxy-init-bad-scheme", args: []string{"proxy", "init", "ftp://x"}},
	{name: "runtime/proxy-debug-bad-mode", args: []string{"proxy", "debug", "maybe", "--force"}},
	// A pre-run hook's refusal is a runtime error, with no usage lines:
	// profileCmd's hook refuses to migrate a legacy config under --no-migrate.
	{name: "runtime/migration-refusal", args: []string{"proxy", "profile", "list", "--no-migrate"}, legacyXray: true},

	// A failed step: its start and result lines, then the root's line — the
	// error printed once, all on stderr.
	{name: "runtime/spinner-failure", args: []string{"stop", "wsx"}},

	// A *cliErrorWithExit with text, at exit 1 and at a vault exit code.
	{name: "cli-exit/code-1", args: []string{"profile-delete", "default"}},
	{name: "cli-exit/code-4", args: []string{"vault", "backup-verify"}},

	// The three *cliErrorWithExit sites with an empty message: the command
	// printed everything itself and the exit code is the rest of it.
	{name: "silent/workspace-status", args: []string{"status"}},
	{name: "silent/vault-health-score", args: []string{"vault", "vault-health-score"}, stub: "health-yellow"},
	{name: "silent/vault-status", args: []string{"vault", "status"}, stub: "vault-status-red"},

	// The two helpers that called Die before phase 1, and the exits beside them.
	{name: "helper/select-no-workspaces", args: []string{"ssh"}},
	{name: "helper/select-cancelled", args: []string{"ssh"}, workspace: "wsx"},
	{name: "helper/connected-enumeration-fails", args: []string{"proxy", "down"}, stub: "connected-enumeration-fails"},
	{name: "helper/connected-declined", args: []string{"proxy", "down"}, stub: "connected-declined"},

	// The paths that reach the Docker SDK: every one of them fails on the
	// unreachable DOCKER_HOST that runExecuteChild sets, which is what makes
	// them hermetic.
	{name: "body-exit/proxy-up-unreachable", args: []string{"proxy", "up"}},
	{name: "body-exit/proxy-doctor-unreachable", args: []string{"proxy", "doctor"}},
	{name: "body-exit/proxy-doctor-json-unreachable", args: []string{"proxy", "doctor", "--json"}},
	{name: "runtime/proxy-test-not-running", args: []string{"proxy", "test"}},
	{name: "runtime/proxy-fix-routes-not-running", args: []string{"proxy", "fix-routes"}},
	// ws proxy check reports and exits 1; ws proxy status's first docker call
	// fails, and the root prints the error.
	{name: "runtime/proxy-check-unreachable", args: []string{"proxy", "check"}},
	{name: "runtime/proxy-status-unreachable", args: []string{"proxy", "status"}},

	// A body that refuses with the Problem it returns, exit 1; it rendered
	// its own error box before phase 3.
	{name: "body-exit/start-not-found", args: []string{"start", "nope"}},
	{name: "body-exit/new-exists", args: []string{"new", "wsx"}, workspace: "wsx"},

	// ws delete of a workspace that is not there: a Problem the command
	// returns, refused before the confirmation, with --force or without.
	{name: "runtime/delete-not-found", args: []string{"delete", "nosuch"}},
	{name: "runtime/delete-not-found-force", args: []string{"delete", "--force", "nosuch"}},
}

// installExecuteStub assigns the seams a stub set names. It runs only in the
// child, before Execute().
func installExecuteStub(name string) {
	switch name {
	case "":
	case "proxy-not-ready":
		verifyProxyReadyFn = func(config.Config) error {
			return errors.New("stub: proxy container not inspectable")
		}
	case "proxy-not-ready-problem":
		verifyProxyReadyFn = func(config.Config) error { return carriedProblemError }
	case "connected-enumeration-fails":
		proxyConnectedContainersFn = func(config.Config) ([]string, error) {
			return nil, errors.New("stub: proxy network not inspectable")
		}
	case "connected-declined":
		proxyConnectedContainersFn = func(config.Config) ([]string, error) {
			return []string{"wsa"}, nil
		}
		warnConfirmFn = func(string, string) bool { return false }
	case "health-yellow":
		vaultHealthScoreComputeFn = func(context.Context, *cobra.Command) (int, error) { return 55, nil }
	case "tunnel-up":
		proxyTestProbeFn = func(config.Config) (proxyengine.ProbeResult, error) {
			return proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "198.51.100.9", Tunneled: true, Latency: 182 * time.Millisecond}, nil
		}
		proxyTestProbeDNSFn = func(config.Config) (proxyengine.DNSProbeResult, error) {
			return proxyengine.DNSProbeResult{ExitIP: "198.51.100.9"}, nil
		}
	case "tunnel-dns-leak":
		proxyTestProbeFn = func(config.Config) (proxyengine.ProbeResult, error) {
			return proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "198.51.100.9", Tunneled: true, Latency: 182 * time.Millisecond}, nil
		}
		proxyTestProbeDNSFn = func(config.Config) (proxyengine.DNSProbeResult, error) {
			return proxyengine.DNSProbeResult{ExitIP: "203.0.113.7"}, nil
		}
	case "tunnel-down":
		proxyTestProbeFn = func(config.Config) (proxyengine.ProbeResult, error) {
			return proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "203.0.113.7", Latency: 95 * time.Millisecond}, nil
		}
		proxyTestProbeDNSFn = func(config.Config) (proxyengine.DNSProbeResult, error) {
			panic("stub: the UDP/DNS leg runs with the tunnel down")
		}
	case "vault-status-red":
		vaultStatusRunFn = func(context.Context, *cobra.Command) (*statusReport, error) {
			return &statusReport{OverallBand: bandRed, ExitCode: 2}, nil
		}
	default:
		fmt.Fprintf(os.Stderr, "EXECUTE-CHILD-UNKNOWN-STUB %q\n", name)
		os.Exit(98)
	}
}

// TestExecuteChildProcess is the far side of the subprocess: it runs only
// when runExecuteChild re-execs the test binary with the environment below.
func TestExecuteChildProcess(t *testing.T) {
	if os.Getenv(executeChildEnv) != "1" {
		t.Skip("child half of TestErrorOutputBaseline; runs only in the subprocess")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(executeArgsEnv)), &args); err != nil {
		fmt.Fprintf(os.Stderr, "EXECUTE-CHILD-BAD-ARGS %v\n", err)
		os.Exit(99)
	}
	installExecuteStub(os.Getenv(executeStubEnv))
	rootCmd.SetArgs(args)
	Execute()
	// Execute returns only when it does not exit; the real binary's main
	// then returns and the process exits 0. Exit here the same way, before
	// the test framework writes anything of its own.
	os.Exit(0)
}

func runExecuteChild(t *testing.T, c errorCase) (code int, stdout, stderr string) {
	t.Helper()
	// PATH is an empty directory: no devpod, docker or git for a case to find,
	// on a developer machine or on a CI runner alike.
	emptyDir := t.TempDir()
	wsDir := t.TempDir()
	if c.workspace != "" {
		if err := os.Mkdir(filepath.Join(wsDir, c.workspace), 0o700); err != nil {
			t.Fatalf("creating workspace %q: %v", c.workspace, err)
		}
	}
	args := make([]string, len(c.args))
	for i, a := range c.args {
		args[i] = strings.ReplaceAll(a, emptyDirArg, emptyDir)
	}
	argv, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding args: %v", err)
	}

	home := childHome
	var dir string
	if c.legacyXray {
		dir, home = t.TempDir(), "home"
		xrayDir := filepath.Join(dir, home, ".config", "xray")
		if err := os.MkdirAll(xrayDir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", xrayDir, err)
		}
		if err := os.WriteFile(filepath.Join(xrayDir, "config.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("writing the legacy config: %v", err)
		}
	}
	// Absolute, because a relative path to the test binary would be read
	// from the child's own directory once dir is set.
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolving the test binary: %v", err)
	}

	// No -test.v: the framework's own "=== RUN" line would land on stdout.
	// The deadline names the case when a child blocks: without one, a
	// regression that reintroduces an interactive path (P-9) would hang the
	// whole package until go test's own timeout killed it, in a goroutine
	// dump that names no case.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, testBinary, "-test.run=^TestExecuteChildProcess$")
	cmd.Dir = dir
	// WaitDelay bounds Run itself: the context only kills the child, and a
	// grandchild holding the pipes open would otherwise keep Run waiting past
	// the 30s deadline above.
	cmd.WaitDelay = 5 * time.Second
	// Setsid, so the child has no controlling terminal. Its stdin is the null
	// device, and huh's terminal layer answers that by opening /dev/tty
	// instead — which, when `go test` is run from a real terminal, is the
	// developer's: the selector case then draws a live prompt there. The
	// 45 s timeout panic under `script -qec` was measured before the
	// per-case 30 s deadline below existed; with the deadline, the same
	// regression fails the case by name after 30 s instead.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The environment is built, not inherited: a COLUMNS, NO_COLOR, CI or
	// VAULT_AI_REPO_ROOT in the developer's shell must not reach the case.
	cmd.Env = []string{
		executeChildEnv + "=1",
		executeArgsEnv + "=" + string(argv),
		executeStubEnv + "=" + c.stub,
		"PATH=" + emptyDir,
		"HOME=" + home,
		"WORKSPACES_DIR=" + wsDir,
		// Every Docker SDK call in the tree builds its client with
		// client.FromEnv, so a DOCKER_HOST pointing at a socket that does not
		// exist fails the same way on a laptop and on a CI runner — which has a
		// daemon — and no case can reach a real proxy.
		"DOCKER_HOST=unix://" + childHome + "/docker.sock",
		"COLUMNS=80",
		"NO_COLOR=1",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"TMPDIR=" + os.TempDir(),
		// Under -race a child sleeps before a successful exit — the race
		// runtime's atexit_sleep_ms, 1000 by default, left for other
		// goroutines to finish reporting. Measured: 1.05 s per successful
		// child with it, 0.04 s without; a failing child does not wait.
		"GORACE=atexit_sleep_ms=0",
	}
	if v, ok := os.LookupEnv("GOCOVERDIR"); ok {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+v)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		code = 0
	case ctx.Err() != nil:
		t.Fatalf("case %s did not finish within 30s — a child that blocks is the hang P-9 describes; stderr so far:\n%s",
			c.name, errBuf.String())
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running case %s: %v\n%s", c.name, err, errBuf.String())
	}
	return code, normaliseStepTimes(out.String()), normaliseStepTimes(errBuf.String())
}

// renderErrorCase is one case's block of the golden file. Each stream is
// headed by its byte count and every line is prefixed with "|", so a trailing
// space, a missing final newline or a stray empty line is visible in a diff.
func renderErrorCase(name string, code int, stdout, stderr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s\nexit %d\n", name, code)
	for _, s := range []struct{ label, text string }{{"stdout", stdout}, {"stderr", stderr}} {
		fmt.Fprintf(&b, "--- %s (%d bytes)\n", s.label, len(s.text))
		for _, line := range strings.SplitAfter(s.text, "\n") {
			if line == "" {
				continue
			}
			if strings.HasSuffix(line, "\n") {
				fmt.Fprintf(&b, "|%s", line)
			} else {
				fmt.Fprintf(&b, "|%s\n--- (no newline at end of %s)\n", line, s.label)
			}
		}
	}
	return b.String()
}

// splitErrorCases maps each case name in a golden rendering to its block.
func splitErrorCases(s string) map[string]string {
	blocks := map[string]string{}
	for _, block := range strings.Split(s, "=== ")[1:] {
		name, _, _ := strings.Cut(block, "\n")
		blocks[name] = "=== " + block
	}
	return blocks
}

// refusedRewrites is why a re-record may not be written, one line per
// reason: a case whose rendering differs from its record, or that has no
// record yet, without being named in allowed; a recorded case the table no
// longer holds, unnamed; a name in allowed that is no case at all, which is
// how a typo shows up instead of silently allowing nothing; and a name two
// cases share, whatever is named, because the map of renderings keeps one of
// their blocks and the other would be written without ever being compared.
func refusedRewrites(recorded, rendered map[string]string, order []string, allowed map[string]bool) []string {
	var refused []string
	inTable := map[string]bool{}
	for _, name := range order {
		if inTable[name] {
			refused = append(refused, name+": two cases share this name")
			continue
		}
		inTable[name] = true
		old, wasRecorded := recorded[name]
		switch {
		case allowed[name]:
		case !wasRecorded:
			refused = append(refused, name+": new, and not named")
		case rendered[name] != old:
			refused = append(refused, name+": differs from its record, and not named")
		}
	}
	for _, name := range sortedKeys(recorded) {
		if !inTable[name] && !allowed[name] {
			refused = append(refused, name+": recorded, no longer in the table, and not named")
		}
	}
	for _, name := range sortedKeys(allowed) {
		if _, ok := recorded[name]; !ok && !inTable[name] {
			refused = append(refused, name+": named, and no such case")
		}
	}
	return refused
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestErrorBaselineRewriteIsNamed is refusedRewrites' own control: each
// kind of unnamed change is refused, and naming it is what lets it through.
func TestErrorBaselineRewriteIsNamed(t *testing.T) {
	recorded := map[string]string{"a": "A", "b": "B", "gone": "G"}
	rendered := map[string]string{"a": "A2", "b": "B", "new": "N"}
	order := []string{"a", "b", "new"}
	for _, c := range []struct {
		name    string
		allowed []string
		want    []string
	}{
		{"nothing named", nil, []string{
			"a: differs from its record, and not named",
			"new: new, and not named",
			"gone: recorded, no longer in the table, and not named",
		}},
		{"every change named", []string{"a", "new", "gone"}, nil},
		{"an unchanged case may be named", []string{"a", "b", "new", "gone"}, nil},
		{"a typo is refused", []string{"a", "new", "gone", "nwe"}, []string{"nwe: named, and no such case"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			allowed := map[string]bool{}
			for _, n := range c.allowed {
				allowed[n] = true
			}
			got := refusedRewrites(recorded, rendered, order, allowed)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("refused\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
	t.Run("a name two cases share is refused, whatever is named", func(t *testing.T) {
		all := map[string]bool{"a": true, "b": true, "new": true, "gone": true}
		got := refusedRewrites(recorded, rendered, []string{"a", "b", "b", "new"}, all)
		if want := "b: two cases share this name"; strings.Join(got, "\n") != want {
			t.Errorf("refused\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})
}

func TestErrorOutputBaseline(t *testing.T) {
	var got strings.Builder
	order := make([]string, 0, len(errorCases))
	for _, c := range errorCases {
		code, stdout, stderr := runExecuteChild(t, c)
		got.WriteString(renderErrorCase(c.name, code, stdout, stderr))
		order = append(order, c.name)
	}

	if *updateErrorBaseline != "" {
		allowed := map[string]bool{}
		for _, name := range strings.Split(*updateErrorBaseline, ",") {
			allowed[strings.TrimSpace(name)] = true
		}
		// A missing file is an empty record: every case is then new.
		recorded, err := os.ReadFile(errorBaselineFile)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("reading %s: %v", errorBaselineFile, err)
		}
		if refused := refusedRewrites(splitErrorCases(string(recorded)), splitErrorCases(got.String()), order, allowed); len(refused) > 0 {
			t.Fatalf("refusing to rewrite %s; nothing was written:\n%s", errorBaselineFile, strings.Join(refused, "\n"))
		}
		if err := os.MkdirAll(filepath.Dir(errorBaselineFile), 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(errorBaselineFile, []byte(got.String()), 0o644); err != nil {
			t.Fatalf("writing %s: %v", errorBaselineFile, err)
		}
		t.Logf("rewrote %s (%d cases; named: %s)", errorBaselineFile, len(errorCases), *updateErrorBaseline)
		return
	}

	want, err := os.ReadFile(errorBaselineFile)
	if err != nil {
		t.Fatalf("reading %s: %v (record it with -update-error-baseline)", errorBaselineFile, err)
	}
	if got.String() == string(want) {
		return
	}
	gotCases, wantCases := splitErrorCases(got.String()), splitErrorCases(string(want))
	for _, c := range errorCases {
		if gotCases[c.name] != wantCases[c.name] {
			t.Errorf("case %s differs from the baseline.\n--- baseline:\n%s--- now:\n%s",
				c.name, wantCases[c.name], gotCases[c.name])
		}
	}
	if len(wantCases) != len(errorCases) {
		t.Errorf("the baseline holds %d cases and the table %d; re-record deliberately, not by accident",
			len(wantCases), len(errorCases))
	}
	if !t.Failed() {
		// The file differs from the rendering somewhere no case block covers:
		// blocks out of order, a duplicate, or stray text before the first one.
		t.Errorf("the baseline differs from the rendering outside any case block; re-record deliberately, not by accident")
	}
}

// TestRootProtocolBranches drives run() directly: which of its four branches
// an error takes, and the exit code each one routes. The bytes those branches
// print are TestErrorOutputBaseline's; this is the selection.
//
// Planted and observed red: run() printing cerr.msg instead of err.Error()
// fails the two wrapped cases; the plain branch returning exit 0 fails
// "plain"; errors.As replaced with a bare type assertion fails both wrapped
// cases.
func TestRootProtocolBranches(t *testing.T) {
	for _, c := range []struct {
		name     string
		err      error
		wantMsg  string
		wantCode int
	}{
		{"nil", nil, "", 0},
		{"silent", &cliErrorWithExit{code: 2, msg: ""}, "", 2},
		{"cli-exit", &cliErrorWithExit{code: 4, msg: "backup-verify: no logs"}, "backup-verify: no logs", 4},
		{"wrapped cli-exit keeps the wrapper's text",
			fmt.Errorf("vault: %w", &cliErrorWithExit{code: 5, msg: "inner"}), "vault: inner", 5},
		{"wrapped silent prints the wrapper's text",
			fmt.Errorf("context: %w", &cliErrorWithExit{code: 3, msg: ""}), "context: ", 3},
		{"plain", errors.New("plain failure"), "plain failure", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg, code := run(c.err)
			if msg != c.wantMsg || code != c.wantCode {
				t.Errorf("run(%v) = (%q, %d); want (%q, %d)", c.err, msg, code, c.wantMsg, c.wantCode)
			}
		})
	}
}

// carriedProblemError is an error that carries a Problem, for the tests of
// the root's print point below.
var carriedProblemError = &output.ProblemError{
	P: output.Problem{
		Title: "Proxy is not ready",
		Cause: "stub: the container is restarting",
		Facts: []output.Fact{{K: "container", V: "dev-proxy"}},
		Steps: []output.Remedy{{Label: "Check it", Cmd: "ws proxy status"}},
	},
	Err: errors.New("stub: proxy container not inspectable"),
}

// TestRootProblemSelects drives rootProblem directly: what the root prints
// for each kind of error. The bytes are TestErrorOutputBaseline's and
// TestExecutePrintsTheCarriedProblem's.
func TestRootProblemSelects(t *testing.T) {
	carried := carriedProblemError.P
	for _, c := range []struct {
		name  string
		err   error
		want  output.Problem
		print bool
	}{
		{"nil", nil, output.Problem{}, false},
		{"silent", &cliErrorWithExit{code: 2, msg: ""}, output.Problem{}, false},
		{"cli-exit", &cliErrorWithExit{code: 4, msg: "backup-verify: no logs"}, output.Problem{Title: "backup-verify: no logs"}, true},
		{"plain", errors.New("plain failure"), output.Problem{Title: "plain failure"}, true},
		{"a carrier", carriedProblemError, carried, true},
		{"a carrier wrapped with %w", fmt.Errorf("proxy not ready for reload: %w", carriedProblemError), carried, true},
		{"a usage error", &usageError{cmd: rootCmd, err: errors.New(`unknown command "x" for "ws"`)},
			output.Problem{Title: `unknown command "x" for "ws"`}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg, _ := run(c.err)
			got, ok := rootProblem(msg, c.err)
			if ok != c.print || !reflect.DeepEqual(got, c.want) {
				t.Errorf("rootProblem = (%+v, %t); want (%+v, %t)", got, ok, c.want, c.print)
			}
		})
	}
}

// TestExecutePrintsTheCarriedProblem runs the real Execute in a child whose
// command returns, wrapped, an error that carries a Problem: stderr is that
// Problem, rendered once, with nothing else, and the exit code is 1. The
// stream the expectation is rendered on is the child's: 80 columns, no
// colour, the UTF-8 glyph mode.
func TestExecutePrintsTheCarriedProblem(t *testing.T) {
	c := errorCase{name: "carried-problem", args: []string{"proxy", "profile", "use", "x", "--no-migrate"}, stub: "proxy-not-ready-problem"}
	code, stdout, stderr := runExecuteChild(t, c)
	s := output.NewStreamAt(io.Discard, 80, false, output.ColourNone, false)
	want := carriedProblemError.P.Render(s) + "\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("exit %d, stdout %q, stderr\n%s\nwant exit 1, no stdout, stderr\n%s", code, stdout, stderr, want)
	}
}

// TestUsageTargetClassifies drives usageTarget directly: which errors are
// usage errors, and which command their usage lines name. The bytes those
// lines print are TestErrorOutputBaseline's.
func TestUsageTargetClassifies(t *testing.T) {
	leaf := &cobra.Command{Use: "leaf"}
	started := &cobra.Command{Use: "started", SilenceUsage: true}
	group := &cobra.Command{Use: "group"}
	group.AddCommand(&cobra.Command{Use: "child"})
	named := &cobra.Command{Use: "named"}
	typed := &usageError{cmd: named, err: errors.New("unknown command"), suggestions: []string{"near"}}

	for _, c := range []struct {
		name        string
		cmd         *cobra.Command
		err         error
		wantTarget  *cobra.Command
		wantSuggest []string
		wantUsage   bool
	}{
		{"no error", leaf, nil, nil, nil, false},
		{"typed, naming its own command", leaf, typed, named, []string{"near"}, true},
		{"typed and wrapped", started, fmt.Errorf("context: %w", typed), named, []string{"near"}, true},
		{"a leaf whose body had not started", leaf, errors.New("accepts 1 arg(s), received 0"), leaf, nil, true},
		{"a leaf whose body had started", started, errors.New("proxy restart failed"), nil, nil, false},
		{"a command with subcommands", group, errors.New("write /dev/stdout: broken pipe"), nil, nil, false},
		{"a flag that failed to parse", started, flagError(started, errors.New("unknown flag: --bogus")), started, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			target, suggestions, ok := usageTarget(c.cmd, c.err)
			if ok != c.wantUsage || target != c.wantTarget || strings.Join(suggestions, ",") != strings.Join(c.wantSuggest, ",") {
				t.Errorf("usageTarget = (%v, %v, %v); want (%v, %v, %v)", target, suggestions, ok, c.wantTarget, c.wantSuggest, c.wantUsage)
			}
		})
	}

	// Every command inherits the root's FlagErrorFunc. The type is what makes
	// a flag error a usage error on a command with subcommands: such a
	// command has no body, so its SilenceUsage marks nothing and rule 2 does
	// not apply. On a leaf rule 2 would catch it untyped.
	for _, cmd := range []*cobra.Command{rootCmd, proxyCmd} {
		err := cmd.FlagErrorFunc()(cmd, errors.New("unknown flag: --bogus"))
		if err.Error() != "unknown flag: --bogus" {
			t.Errorf("%s: the FlagErrorFunc changed the message: %q", cmd.CommandPath(), err)
		}
		if target, _, ok := usageTarget(cmd, err); !ok || target != cmd {
			t.Errorf("%s: a flag error is not a usage error of the command: (%v, %v)", cmd.CommandPath(), target, ok)
		}
	}
}

// TestUnknownCommandIsRecognised pins isUnknownCommand and
// unknownRootCommand against the error cobra actually builds, not against a
// copy of its text: if cobra rewords it, this goes red instead of `ws nosuch`
// silently losing its usage lines.
func TestUnknownCommandIsRecognised(t *testing.T) {
	_, _, err := rootCmd.Find([]string{"nosuch"})
	if err == nil {
		t.Fatal("cobra found a command named nosuch")
	}
	if !isUnknownCommand(err) {
		t.Errorf("isUnknownCommand(%q) = false; execute would not type it", err)
	}
	if isUnknownCommand(errors.New("proxy not ready for reload")) || isUnknownCommand(nil) {
		t.Error("isUnknownCommand matched an error cobra did not raise")
	}
	// cobra.NoArgs raises the same sentence for an extra argument to a command
	// that takes none (args.go NoArgs). The text cannot tell the two apart,
	// which is why execute types it only for the root. If this stops matching,
	// execute's guard needs re-reading, not just this test.
	if noArgs := cobra.NoArgs(newWorkspaceStatusCmd(), []string{"x"}); noArgs == nil || !isUnknownCommand(noArgs) {
		t.Errorf("cobra.NoArgs no longer carries the unknown-command text (%v); re-read execute's guard", noArgs)
	}

	// The word comes back out of cobra's %q exactly: with the quote and the
	// backslash %q escapes, with a space inside the quotes, and outside ASCII.
	for _, word := range []string{"lst", `a"b`, `c\d`, "two words", "ünïcode"} {
		_, _, cobraErr := rootCmd.Find([]string{word})
		if got, ok := unknownRootWord(cobraErr); !ok || got != word {
			t.Errorf("from %q read back (%q, %v); want %q", cobraErr, got, ok, word)
		}
	}
	// And the typed error keeps cobra's message and carries the suggestions.
	_, _, cobraErr := rootCmd.Find([]string{"lst"})
	ue, ok := unknownRootCommand(rootCmd, cobraErr)
	if !ok || ue.Error() != cobraErr.Error() || ue.cmd != rootCmd || strings.Join(ue.suggestions, ",") != "list,ssh" {
		t.Errorf("lst: typed as (%v, %v); want cobra's %q, for ws, suggesting list,ssh", ue, ok, cobraErr)
	}
}

// TestUsageIsTheHelpDocument: a caller of Usage() gets the help document
// without its description, rendered for stderr, where cobra writes usage —
// not cobra's stock template.
func TestUsageIsTheHelpDocument(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out) // OutOrStderr, where cobra writes usage, prefers it
	t.Cleanup(func() { rootCmd.SetOut(nil) })
	if err := proxyCmd.Usage(); err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if want := renderHelp(output.Err(), proxyCmd, false) + "\n"; out.String() != want {
		t.Errorf("ws proxy's usage is\n%s\nwant the help document without its description:\n%s", out.String(), want)
	}
	if strings.Contains(out.String(), proxyCmd.Short) {
		t.Errorf("ws proxy's usage carries its description: %q", out.String())
	}
}

// executeIn runs root with args through execute, in this process, and
// returns the command that failed, what root wrote to stdout, and the
// error. When the test ends, the failed command's leftover words are parsed
// away: a command object lives for the whole test process, and execute
// reads a group's leftover words.
func executeIn(t *testing.T, root *cobra.Command, args ...string) (*cobra.Command, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	t.Cleanup(func() {
		root.SetOut(nil)
		root.SetErr(nil)
		root.SetArgs(nil)
	})
	cmd, err := execute(root)
	t.Cleanup(func() { _ = cmd.Flags().Parse(nil) })
	return cmd, out.String(), err
}

// configureLikeRoot gives a fixture root the real root's help, usage and
// error handling, read from rootCmd so that the two cannot drift apart.
func configureLikeRoot(root *cobra.Command) {
	root.SilenceErrors = rootCmd.SilenceErrors
	root.SilenceUsage = rootCmd.SilenceUsage
	root.DisableSuggestions = rootCmd.DisableSuggestions
	root.SetHelpFunc(rootCmd.HelpFunc())
	root.SetUsageFunc(rootCmd.UsageFunc())
	root.SetFlagErrorFunc(rootCmd.FlagErrorFunc())
}

// TestGroupsRejectStrayWords: every command with subcommands answers a word
// that names none of them — with --help after it or not, with a flag before
// it or not — with exit 1 and the typed usage error. Before, the groups
// printed their help and exited 0.
func TestGroupsRejectStrayWords(t *testing.T) {
	groups := 0
	for _, c := range helpCommands(rootCmd) {
		if !c.HasSubCommands() {
			continue
		}
		groups++
		path := strings.Fields(c.CommandPath())[1:]
		wantFirst := fmt.Sprintf("✗ unknown command %q for %q\n", "zzz", c.CommandPath())
		wantUsage := "\n  Usage: " + c.CommandPath() + " [command]\n"
		for _, tail := range [][]string{{"zzz"}, {"zzz", "--help"}, {"--json", "zzz"}} {
			args := append(append([]string{}, path...), tail...)
			code, stdout, stderr := runExecuteChild(t, errorCase{name: strings.Join(args, " "), args: args})
			if code != 1 || stdout != "" || !strings.HasPrefix(stderr, wantFirst) || !strings.Contains(stderr, wantUsage) {
				t.Errorf("ws %s: exit %d, stdout %q, stderr %q; want exit 1, %q and %q",
					strings.Join(args, " "), code, stdout, stderr, wantFirst, wantUsage)
			}
		}
	}
	if groups < 4 {
		t.Fatalf("found %d commands with subcommands; ws, proxy, proxy profile and vault all have some", groups)
	}
	// A near miss under a group gets suggestions, as one at the root does.
	if ue := unknownSubcommand(proxyCmd, "stauts"); strings.Join(ue.suggestions, ",") != "status" {
		t.Errorf("ws proxy stauts suggests %v; want status", ue.suggestions)
	}
}

// TestHelpReadsNoWordOfAnotherRun: a word left over in one run stays in the
// group's flag set until the group's flags are parsed again. `fx help grp`
// reaches the group's help through cmd.Help(), which parses none, and must
// print the group's help all the same.
func TestHelpReadsNoWordOfAnotherRun(t *testing.T) {
	fx := newHelpFixture(&fixtureHook{})
	configureLikeRoot(fx)
	if _, _, err := executeIn(t, fx, "grp", "zzz"); err == nil {
		t.Fatal("fx grp zzz: want a usage error")
	}
	if _, stdout, err := executeIn(t, fx, "help", "grp"); err != nil || !strings.Contains(stdout, "Usage:") {
		t.Errorf("fx help grp after a run that left a word: error %v, printed %q; want the group's help", err, stdout)
	}
}

// TestExecuteRestoresTheHelpFunction: execute wraps the root's help function
// for its own run only, and leaves the one it found.
func TestExecuteRestoresTheHelpFunction(t *testing.T) {
	fx := newHelpFixture(&fixtureHook{})
	configureLikeRoot(fx)
	want := reflect.ValueOf(fx.HelpFunc()).Pointer()
	if _, _, err := executeIn(t, fx, "grp", "zzz"); err == nil {
		t.Fatal("fx grp zzz: want a usage error")
	}
	if got := reflect.ValueOf(fx.HelpFunc()).Pointer(); got != want {
		t.Errorf("after a run the root's help function is %#x, want %#x", got, want)
	}
}

// TestVersionLeavesAStrayWordAlone: cobra answers --version before it
// answers a command with subcommands with its help, so a word left over at
// the root never reaches the help function, and the version is the whole
// answer, exit 0, as it was before the groups rejected stray words.
func TestVersionLeavesAStrayWordAlone(t *testing.T) {
	fx := newHelpFixture(&fixtureHook{})
	configureLikeRoot(fx)
	fx.Version = "1.2.3"
	if _, stdout, err := executeIn(t, fx, "--version=true", "--", "zzz"); err != nil || !strings.Contains(stdout, "1.2.3") {
		t.Errorf("fx --version=true -- zzz: error %v, stdout %q; want the version and no error", err, stdout)
	}
}

// TestFixtureHookRunsOnlyForALeaf holds the order a pre-run hook meets, on
// the fixture group: it runs neither for the group nor for a stray word
// under it, nor for a leaf's --help even where it would refuse; a leaf's
// missing required flag, which cobra checks after the hook has passed, is a
// usage error; and a hook's refusal is a runtime error, even where a
// required flag is missing too.
func TestFixtureHookRunsOnlyForALeaf(t *testing.T) {
	for _, c := range []struct {
		name      string
		fail      bool
		args      []string
		wantRan   int
		wantHelp  string // with no error wanted: the synopsis the help prints
		wantErr   string // "" for none
		wantUsage bool
	}{
		{"the group prints its help", false, []string{"grp"}, 0, "fx grp [command]", "", false},
		{"a leaf's --help under a hook that would refuse", true, []string{"grp", "needs", "--help"}, 0, "fx grp needs [flags]", "", false},
		{"a stray word under the group", false, []string{"grp", "zzz"}, 0, "", `unknown command "zzz" for "fx grp"`, true},
		{"a stray word before --help", false, []string{"grp", "zzz", "--help"}, 0, "", `unknown command "zzz" for "fx grp"`, true},
		{"an empty word under the group", false, []string{"grp", ""}, 0, "", `unknown command "" for "fx grp"`, true},
		{"an empty word before another", false, []string{"grp", "", "zzz"}, 0, "", `unknown command "" for "fx grp"`, true},
		{"a missing required flag after the hook passed", false, []string{"grp", "needs"}, 1, "", `required flag(s) "req" not set`, true},
		{"the hook refuses", true, []string{"grp", "needs", "--req", "x"}, 1, "", "fixture hook refused", false},
		{"the hook refuses before the required flag is checked", true, []string{"grp", "needs"}, 1, "", "fixture hook refused", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := &fixtureHook{fail: c.fail}
			fx := newHelpFixture(h)
			configureLikeRoot(fx)
			cmd, stdout, err := executeIn(t, fx, c.args...)
			if h.ran != c.wantRan {
				t.Errorf("the hook ran %d times, want %d", h.ran, c.wantRan)
			}
			if c.wantErr == "" {
				if err != nil || !strings.Contains(ansi.Strip(stdout), "Usage:\n  "+c.wantHelp+"\n") {
					t.Errorf("want help carrying %q and no error; got %v and %q", c.wantHelp, err, stdout)
				}
				return
			}
			if err == nil || err.Error() != c.wantErr {
				t.Fatalf("error %v, want %q", err, c.wantErr)
			}
			if _, _, isUsage := usageTarget(cmd, err); isUsage != c.wantUsage {
				t.Errorf("classified as a usage error: %v, want %v", isUsage, c.wantUsage)
			}
		})
	}
}

// TestProfileHookRunsOnlyForALeaf: profileCmd's hook, with EnsureMigrated
// stubbed to refuse. `ws proxy profile` prints its help and
// `ws proxy profile zzz` is a usage error of the group, neither calling the
// stub; `ws proxy profile list` calls it, and its refusal is a runtime error.
func TestProfileHookRunsOnlyForALeaf(t *testing.T) {
	orig := ensureMigratedFn
	t.Cleanup(func() { ensureMigratedFn = orig })
	calls := 0
	ensureMigratedFn = func(config.Config, bool) error {
		calls++
		return errors.New("stub: migration required")
	}

	_, stdout, err := executeIn(t, rootCmd, "proxy", "profile")
	if err != nil || calls != 0 || !strings.Contains(ansi.Strip(stdout), "Usage:\n  ws proxy profile [command]") {
		t.Errorf("ws proxy profile: error %v, stub calls %d, stdout %q; want its help and no call", err, calls, stdout)
	}

	cmd, _, err := executeIn(t, rootCmd, "proxy", "profile", "zzz")
	if target, _, ok := usageTarget(cmd, err); !ok || target != profileCmd || calls != 0 {
		t.Errorf("ws proxy profile zzz: error %v as a usage error of %v (%v), stub calls %d; want a usage error of the group and no call",
			err, target, ok, calls)
	}

	resetSilenceUsage(t, "proxy", "profile", "list")
	cmd, _, err = executeIn(t, rootCmd, "proxy", "profile", "list")
	if calls != 1 || err == nil || err.Error() != "stub: migration required" {
		t.Fatalf("ws proxy profile list: error %v, stub calls %d; want the stub's refusal from one call", err, calls)
	}
	if _, _, isUsage := usageTarget(cmd, err); isUsage {
		t.Errorf("the hook's refusal is classified as a usage error")
	}
}

// TestHelpCommandAnswersLikeTheCommand: `ws help <words>` gives exactly
// what `ws <words>` gives when the words name no command — the same stream,
// bytes and exit code — and the help `ws <words> --help` gives when they
// do, a leaf's argument included.
func TestHelpCommandAnswersLikeTheCommand(t *testing.T) {
	for _, c := range []struct{ help, same []string }{
		{[]string{"help", "nosuch"}, []string{"nosuch"}},
		{[]string{"help", "lst"}, []string{"lst"}},
		{[]string{"help", "proxy", "zzz"}, []string{"proxy", "zzz"}},
		{[]string{"help", "proxy", "profile", "zzz"}, []string{"proxy", "profile", "zzz"}},
		{[]string{"help"}, []string{"--help"}},
		{[]string{"help", "proxy"}, []string{"proxy", "--help"}},
		{[]string{"help", "start", "wsx"}, []string{"start", "wsx", "--help"}},
		{[]string{"help", "help"}, []string{"help", "--help"}},
	} {
		code, stdout, stderr := runExecuteChild(t, errorCase{name: strings.Join(c.help, " "), args: c.help})
		wantCode, wantOut, wantErr := runExecuteChild(t, errorCase{name: strings.Join(c.same, " "), args: c.same})
		if code != wantCode || stdout != wantOut || stderr != wantErr {
			t.Errorf("ws %s: exit %d, stdout %q, stderr %q\nws %s: exit %d, stdout %q, stderr %q",
				strings.Join(c.help, " "), code, stdout, stderr, strings.Join(c.same, " "), wantCode, wantOut, wantErr)
		}
	}
}

// TestCompletionStillListsCommands: `ws __complete ""` and
// `ws __complete help ""` list the same commands. The second is cobra's
// completion of the help command's argument, which the new body keeps.
func TestCompletionStillListsCommands(t *testing.T) {
	names := func(args ...string) []string {
		t.Helper()
		code, stdout, _ := runExecuteChild(t, errorCase{name: strings.Join(args, " "), args: args})
		if code != 0 {
			t.Fatalf("ws %q: exit %d", args, code)
		}
		var out []string
		for _, line := range strings.Split(stdout, "\n") {
			if name, _, ok := strings.Cut(line, "\t"); ok {
				out = append(out, name)
			}
		}
		return out
	}
	root, help := names("__complete", ""), names("__complete", "help", "")
	if !slices.Contains(root, "list") || !slices.Contains(root, "proxy") {
		t.Fatalf(`ws __complete "" lists %v; want the root's commands`, root)
	}
	if !slices.Equal(root, help) {
		t.Errorf(`ws __complete help "" lists %v; ws __complete "" lists %v`, help, root)
	}
}

// TestCompletionReportsAFailedWrite: a script that cannot be written — a
// full disk under `> file` — is an error, for every shell, where it used to
// be discarded with exit 0; and a runtime error, with no usage lines.
func TestCompletionReportsAFailedWrite(t *testing.T) {
	rootCmd.SetOut(failingWriter{})
	t.Cleanup(func() { rootCmd.SetOut(nil) })
	// Over a copy: generating the bash script sorts every command's
	// ValidArgs in place, this one's included, under the loop.
	for _, shell := range slices.Clone(completionCmd.ValidArgs) {
		resetSilenceUsage(t, "completion")
		err := completionCmd.RunE(completionCmd, []string{shell})
		if err == nil || !strings.Contains(err.Error(), "write refused") {
			t.Errorf("ws completion %s into a failing writer returned %v; want the write error", shell, err)
		}
		if _, _, isUsage := usageTarget(completionCmd, err); isUsage {
			t.Errorf("ws completion %s: a failed write is classified as a usage error", shell)
		}
	}
}

// TestUnknownRootCommandBeatsHelpAndVersion: cobra rejects an unknown root
// command before it looks at --help or --version, so neither turns the
// mistake into help or a version at exit 0.
func TestUnknownRootCommandBeatsHelpAndVersion(t *testing.T) {
	for _, flagArg := range []string{"--help", "--version"} {
		code, stdout, stderr := runExecuteChild(t, errorCase{name: "nosuch " + flagArg, args: []string{"nosuch", flagArg}})
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, `✗ unknown command "nosuch" for "ws"`+"\n") {
			t.Errorf("ws nosuch %s: exit %d, stdout %q, stderr %q; want exit 1 and the unknown-command error", flagArg, code, stdout, stderr)
		}
	}
}

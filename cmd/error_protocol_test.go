package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/spf13/cobra"
)

// The error path's byte-level baseline.
//
// Every case runs the REAL Execute() in a re-exec of this test binary — the
// model probeDie established in internal/output. Only a separate process sees
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
// file shows them). Re-record with:
//
//	go test ./cmd -run '^TestErrorOutputBaseline$' -update-error-baseline

var updateErrorBaseline = flag.Bool("update-error-baseline", false,
	"rewrite testdata/error-protocol.golden from the current tree")

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

	// spinner marks a case whose stdout carries huh/spinner frames, which
	// depend on how long the action ran; see normaliseSpinner.
	spinner bool
}

// errorCases covers the four branches of the root protocol, the argument
// and runtime halves of the usage distinction, the spinner's known second
// print, and every helper and in-body exit that phase 1 moves onto a
// returned value and a hermetic process can reach.
var errorCases = []errorCase{
	// A successful command: no error text, exit 0.
	{name: "success/detect", args: []string{"detect", emptyDirArg}},

	// Argument errors keep their Usage: block. One on a command that already
	// returns its error, one on a body phase 1 converts, one cobra raises
	// before any command is found, one raised while parsing flags.
	{name: "arg-error/runE-command", args: []string{"proxy", "profile", "use"}},
	{name: "arg-error/converted-body", args: []string{"start"}},
	{name: "arg-error/unknown-command", args: []string{"nosuch"}},
	{name: "arg-error/unknown-flag", args: []string{"start", "--bogus", "wsx"}},
	// An extra argument to a NoArgs command carries cobra's "unknown command"
	// text too, but the usage hint follows only the one the root itself
	// raises (cobra printed it only there; Execute reproduces that). This
	// case is the other side of arg-error/unknown-command: the hint must NOT
	// appear here.
	{name: "arg-error/noargs-extra", args: []string{"status", "x"}},

	// A runtime error prints no Usage: block. A plain error returned from a
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

	// The spinner prints its own line on STDOUT and returns the error, which
	// renders again on stderr: the known second print phase 3b removes.
	{name: "runtime/spinner-double", args: []string{"stop", "wsx"}, spinner: true},

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
	{name: "body-exit/proxy-up-unreachable", args: []string{"proxy", "up"}, spinner: true},
	{name: "body-exit/proxy-doctor-unreachable", args: []string{"proxy", "doctor"}},
	{name: "body-exit/proxy-doctor-json-unreachable", args: []string{"proxy", "doctor", "--json"}},
	{name: "runtime/proxy-test-not-running", args: []string{"proxy", "test"}},
	{name: "runtime/proxy-fix-routes-not-running", args: []string{"proxy", "fix-routes"}},

	// A body that renders its own error box, then leaves with exit 1.
	{name: "body-exit/start-not-found", args: []string{"start", "nope"}},
	{name: "body-exit/new-exists", args: []string{"new", "wsx"}, workspace: "wsx"},
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

	// No -test.v: the framework's own "=== RUN" line would land on stdout.
	// The deadline names the case when a child blocks: without one, a
	// regression that reintroduces an interactive path (P-9) would hang the
	// whole package until go test's own timeout killed it, in a goroutine
	// dump that names no case.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecuteChildProcess$")
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
		"HOME=" + childHome,
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
	stdout = out.String()
	if c.spinner {
		stdout = normaliseSpinner(stdout)
	}
	return code, stdout, errBuf.String()
}

// spinnerFrames are the runes huh/spinner cycles through while it runs. Which
// one is on screen when the action ends depends on how long it took, so they
// are the only thing normaliseSpinner is allowed to drop.
const spinnerFrames = "⣾⣽⣻⢿⡿⣟⣯⣷⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// normaliseSpinner collapses the spinner animation to the one line it leaves
// on screen, independent of how many frames it rendered before the test's
// action finished. Text after the spinner's last erase-line survives as it
// is (ANSI stripped, \r removed). Text before it splits at the last
// newline: complete lines that precede the spinner survive too, ANSI
// stripped and \r-free, because an erase-line clears only one line and a
// diagnostic printed earlier is still on the operator's screen; the
// spinner's own line — redraws separated by \r — collapses to its last
// non-empty redraw, with the frame rune and surrounding whitespace trimmed.
// ansi.Strip runs over the whole stream, including the erase-line and the
// cursor-restore sequences around it, so a regression that stopped
// restoring the cursor would not show up in this comparison.
func normaliseSpinner(s string) string {
	const eraseLine = "\x1b[2K"
	// The carriage returns the animation leaves behind are not escape
	// sequences, so ansi.Strip keeps them; in the golden they would be
	// invisible bytes inside a line.
	clean := func(part string) string { return strings.ReplaceAll(ansi.Strip(part), "\r", "") }
	i := strings.LastIndex(s, eraseLine)
	if i < 0 {
		return clean(s)
	}
	pre := ansi.Strip(s[:i])
	prefix, spinnerLine := "", pre
	if j := strings.LastIndex(pre, "\n"); j >= 0 {
		prefix, spinnerLine = pre[:j+1], pre[j+1:]
	}
	prefix = strings.ReplaceAll(prefix, "\r", "")
	last := ""
	for _, redraw := range strings.Split(spinnerLine, "\r") {
		if redraw != "" {
			last = redraw
		}
	}
	before := strings.Trim(last, spinnerFrames+" \t\n")
	return prefix + before + clean(s[i+len(eraseLine):])
}

// TestNormaliseSpinner pins normaliseSpinner against synthetic input built
// from the real shape huh/spinner writes (captured from `stop wsx`), with
// one, two and three redraws before the erase-line — the review's finding
// was that only the one-frame shape was ever exercised, so the two- and
// three-frame cases here are the regression pin.
func TestNormaliseSpinner(t *testing.T) {
	const title = `Stopping workspace "wsx"`
	const tail = `✗ Stopping workspace "wsx": boom` + "\n"
	const wantSpinner = title + tail

	frame := func(runes string) string {
		var b strings.Builder
		for _, r := range runes {
			fmt.Fprintf(&b, "\r%c %s", r, title)
		}
		return "\x1b[?25l\x1b[?2004h" + b.String() +
			"\r\x1b[2K\r\x1b[?2004l\x1b[?25h" + tail
	}
	one := frame("⣽")
	two := frame("⣽⣻")
	three := frame("⣽⣻⢿")

	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"one redraw", one, wantSpinner},
		{"two redraws", two, wantSpinner},
		{"three redraws", three, wantSpinner},
		{"diagnostic line before the spinner starts", "⚠ something\n" + one, "⚠ something\n" + wantSpinner},
		{"no erase-line at all", "\x1b[1msuccess\x1b[0m\n", "success\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := normaliseSpinner(c.in); got != c.want {
				t.Errorf("normaliseSpinner(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
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

func TestErrorOutputBaseline(t *testing.T) {
	var got strings.Builder
	for _, c := range errorCases {
		code, stdout, stderr := runExecuteChild(t, c)
		got.WriteString(renderErrorCase(c.name, code, stdout, stderr))
	}

	if *updateErrorBaseline {
		if err := os.MkdirAll(filepath.Dir(errorBaselineFile), 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(errorBaselineFile, []byte(got.String()), 0o644); err != nil {
			t.Fatalf("writing %s: %v", errorBaselineFile, err)
		}
		t.Logf("rewrote %s (%d cases)", errorBaselineFile, len(errorCases))
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

// TestUnknownCommandIsRecognised pins isUnknownCommand against the error
// cobra actually builds, not against a copy of its text: if cobra rewords it,
// this goes red instead of the hint silently disappearing.
func TestUnknownCommandIsRecognised(t *testing.T) {
	_, _, err := rootCmd.Find([]string{"nosuch"})
	if err == nil {
		t.Fatal("cobra found a command named nosuch")
	}
	if !isUnknownCommand(err) {
		t.Errorf("isUnknownCommand(%q) = false; Execute would drop the usage hint", err)
	}
	if isUnknownCommand(errors.New("proxy not ready for reload")) || isUnknownCommand(nil) {
		t.Error("isUnknownCommand matched an error cobra did not raise")
	}
	// cobra.NoArgs raises the same sentence for an extra argument to a command
	// that takes none (args.go NoArgs), and cobra prints no hint for that one.
	// The text cannot tell the two apart, which is why Execute gates the hint
	// on the command having no parent. If this stops matching, the guard in
	// Execute needs re-reading, not just this test.
	if noArgs := cobra.NoArgs(newWorkspaceStatusCmd(), []string{"x"}); noArgs == nil || !isUnknownCommand(noArgs) {
		t.Errorf("cobra.NoArgs no longer carries the unknown-command text (%v); re-read Execute's hint guard", noArgs)
	}
}

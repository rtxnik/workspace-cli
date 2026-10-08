package cmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/profile"
	"github.com/rtxnik/workspace-cli/internal/workspace"
)

// The tables of ws list, ws status, ws profiles and ws proxy profile list,
// and the Problems the commands under cmd build, held to the width contract
// at every width (§4.1), whole in a pipe (§4.2), and recorded as a reviewer
// reads them (§4.5). The Problem of a failed profile switch is swept where it
// is built, in internal/xray.

// sweepName is a 64-character workspace name, sweepToken a 200-character
// token with nowhere to break, and sweepGitError the text of a git probe that
// failed.
var (
	sweepName     = strings.Repeat("payments-", 7) + "x"
	sweepToken    = strings.Repeat("9f86d081884c7d65", 13)[:200]
	sweepGitError = "status: exit status 128: fatal: unable to access " +
		"'https://git.example.com/platform/infrastructure-live.git/': Could not resolve host: git.example.com"
)

// sweepTail is a failed child's last 20 lines: the token, CJK, escapes and a
// carriage return among them.
func sweepTail() []string {
	tail := []string{sweepToken, "错误：无法连接到代理服务器，正在重试", "\x1b[31mred\x1b[0m \x1b]0;title\x07 done\r"}
	for i := len(tail); i < 20; i++ {
		tail = append(tail, fmt.Sprintf("[12:01:%02d] info up %s: step %d of 20", i, sweepName, i))
	}
	return tail
}

// The rows of each table, laid out by the commands' own builders.
var (
	sweepWorkspaces = []workspace.Info{
		{Name: sweepName, Status: "Running", Profile: "数据科学-gpu", Proxy: true},
		{Name: "api", Status: "Busy", Profile: sweepToken},
		{Name: "ops", Status: "Stopped", Profile: "devops"},
		{Name: "legacy-billing", Profile: "default"},
		{Name: "ml", Status: "Rebuilding", Profile: "python", Proxy: true},
	}
	sweepRepos = []workspace.RepoStatus{
		{Name: sweepName, Exists: true, Branch: "feature/" + sweepToken, Ahead: 1234, Behind: 56},
		{Name: "仓库-数据", Exists: true, Branch: "main", Clean: true, NoRemote: true},
		{Name: "dotfiles"},
		{Name: "vault-ai", Exists: true, Error: sweepGitError},
		{Name: "workspace-cli", Exists: true, Branch: "main", Clean: true},
	}
	sweepProfiles = []profile.Info{
		{Name: sweepName, BaseImage: "registry.example.com/" + sweepToken, Tools: "中文工具链, 日本語ツール, go, rust, " + sweepToken},
		{Name: "default", BaseImage: "mcr.microsoft.com/devcontainers/base:ubuntu-24.04"},
		{Name: "数据", BaseImage: "debian:bookworm-slim", Tools: "zig, zls"},
	}
)

// sweepProxyProfiles are ws proxy profile list's rows, the UUIDs masked or,
// under --reveal, whole.
func sweepProxyProfiles(reveal bool) []proxyProfileRow {
	uuids := []string{"1f2e3d4c-****-****-****-********4a5b", "9a8b7c6d-****-****-****-********7c6d"}
	if reveal {
		uuids = []string{fixtureXrayProfiles[0].uuid, fixtureXrayProfiles[1].uuid}
	}
	return []proxyProfileRow{
		{true, sweepName, "tcp", "2001:db8:85a3::8a2e:370:7334", 8443, sweepToken, uuids[0]},
		{false, "备用-节点", "ws", "de-fra-01.example-vpn.net", 443, "www.microsoft.com", uuids[1]},
		{false, "h2", "hysteria2", "198.51.100.7", 36712, "", ""},
	}
}

// sweptTable is one of the tables, with what it must render whole in a pipe:
// every value its rows were built from, and its state cells in the UTF-8
// glyph mode.
type sweptTable struct {
	name   string
	t      output.Table
	values []string
	states []string
}

// sweepTables builds every table through its command's builder; a column
// set NewTableBlock refuses fails the test.
func sweepTables(t *testing.T) []sweptTable {
	t.Helper()
	if len(sweepName) != 64 || len(sweepToken) != 200 {
		t.Fatalf("the sweep's name is %d characters and its token %d; want 64 and 200", len(sweepName), len(sweepToken))
	}
	var tables []sweptTable
	add := func(name string, tb output.Table, err error, values, states []string) {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tables = append(tables, sweptTable{name, tb, values, states})
	}
	var values []string
	for _, ws := range sweepWorkspaces {
		values = append(values, ws.Name, ws.Profile)
	}
	tb, err := listTable(sweepWorkspaces)
	add("ws list", tb, err, values, []string{"✓ running", "~ busy", "- stopped", "- not created", "? rebuilding"})
	values = []string{"+1234 -56", "no remote", "in sync"}
	for _, r := range sweepRepos {
		values = append(values, r.Name, r.Branch, r.Error)
	}
	tb, err = statusTable(sweepRepos)
	add("ws status", tb, err, values, []string{"~ dirty", "✓ clean", "✗ missing", "✗ error"})
	values = nil
	for _, p := range sweepProfiles {
		values = append(values, p.Name, p.BaseImage, p.Tools)
	}
	tb, err = profilesTable(sweepProfiles)
	add("ws profiles", tb, err, values, nil)
	for _, reveal := range []bool{false, true} {
		values = nil
		for _, r := range sweepProxyProfiles(reveal) {
			values = append(values, r.name, r.transport, net.JoinHostPort(r.addr, strconv.Itoa(r.port)), r.sni, r.uuid)
		}
		name := "ws proxy profile list"
		if reveal {
			name += " --reveal"
		}
		tb, err = proxyProfileTable(sweepProxyProfiles(reveal), reveal)
		add(name, tb, err, values, []string{"✓ yes", "- no"})
	}
	return tables
}

// sweepProblems are the Problems the root prints for the commands under cmd,
// each taken from the error its command returns.
func sweepProblems(t *testing.T) []output.Problem {
	t.Helper()
	var problems []output.Problem
	for _, err := range []error{
		workspaceNotFound(sweepName, output.Remedy{Label: "Create it", Cmd: "ws new " + sweepName}),
		workspaceNotFound(sweepName, output.Remedy{Label: "Remove it from devpod", Cmd: "devpod delete " + sweepName}),
		workspaceExists(sweepName),
		upProblem(&output.TaskError{Title: "Starting proxy", Err: errors.New("start proxy: " + sweepGitError), Tail: sweepTail()}),
		upProblem(&docker.RouteFixError{Report: docker.FixRoutesReport{Attempted: 3, Failures: []string{
			sweepName + ": " + sweepGitError, "数据科学: exec ip route replace: " + sweepToken}}}),
		&output.TaskError{Title: "Stopping workspace", Err: errors.New("devpod stop: exit status 1"), Tail: sweepTail()},
	} {
		p, ok := output.ProblemOf(err)
		if !ok {
			t.Fatalf("%v carries no Problem", err)
		}
		problems = append(problems, p)
	}
	return problems
}

// TestTablesFitEveryWidth is the deliverable: every table and every Problem
// at every width from MinWidth to 200, in both glyph modes, and not one line
// wider than the width.
func TestTablesFitEveryWidth(t *testing.T) {
	sweepTablesAndProblems(t, []bool{false, true})
}

// sweepTablesAndProblems sweeps every table and every Problem.
func sweepTablesAndProblems(t *testing.T, modes []bool) {
	t.Helper()
	var renders []func(*output.Stream) string
	for _, tb := range sweepTables(t) {
		renders = append(renders, tb.t.Render)
	}
	for _, p := range sweepProblems(t) {
		renders = append(renders, p.Render)
	}
	sweepRenders(t, renders, modes)
}

// sweepRenders renders each of renders at every width from MinWidth to 200
// in each glyph mode of modes, on a terminal stream, and measures every line
// with the layer's W. Its control re-checks the same renders against a limit
// one cell smaller and counts, over all of them, the lines that are then over
// it: at every width the count must be non-zero, or the sweep could not see
// an overflow of one cell. A single render narrower than the width proves
// nothing, which is why the count is the corpus's: the token is hard-broken
// into lines exactly the width.
func sweepRenders(t *testing.T, renders []func(*output.Stream) string, modes []bool) {
	t.Helper()
	n, lines, over := 0, 0, 0
	for w := output.MinWidth; w <= 200; w++ {
		control := 0
		for _, ascii := range modes {
			s := output.NewStreamAt(io.Discard, w, true, output.ColourTrue, ascii)
			for i, render := range renders {
				n++
				for _, line := range strings.Split(render(s), "\n") {
					lines++
					switch m := output.W(line); {
					case m > w:
						if over++; over <= 10 {
							t.Errorf("render %d at %d columns: a line is %d cells wide: %q", i, w, m, line)
						}
					case m == w:
						control++
					}
				}
			}
		}
		if control == 0 {
			t.Errorf("control at %d columns: no line is wider than %d, so the sweep cannot see an overflow of one cell", w, w-1)
		}
		if w == output.MinWidth {
			t.Logf("control at %d columns: %d lines are wider than %d", w, control, w-1)
		}
	}
	if over > 0 {
		t.Errorf("%d lines over the width in all", over)
	}
	t.Logf("swept %d renders, %d lines", n, lines)
}

const tablesAmbiWideEnv = "WS_TEST_TABLES_AMBIWIDE"

// TestTablesFitEveryWidthAmbiguousWide runs the sweep under
// RUNEWIDTH_EASTASIAN=1.
func TestTablesFitEveryWidthAmbiguousWide(t *testing.T) {
	ambiguousWide(t, "TestTablesFitEveryWidthAmbiguousWide", tablesAmbiWideEnv, func(t *testing.T) {
		sweepTablesAndProblems(t, []bool{true})
	})
}

// ambiguousWide runs sweep under RUNEWIDTH_EASTASIAN=1 in a child — x/ansi
// reads the variable in init() — that re-runs test with env set to 1, in the
// glyph mode the layer selects there: ASCII, because the UTF-8 borders, marks
// and truncation marker are Ambiguous. The child checks both, then sweeps.
func ambiguousWide(t *testing.T, test, env string, sweep func(t *testing.T)) {
	t.Helper()
	if os.Getenv(env) == "1" {
		if n := output.W("…"); n != 2 {
			t.Fatalf("U+2026 measures %d cells under RUNEWIDTH_EASTASIAN=1, want 2: the convention did not reach x/ansi", n)
		}
		if mode := output.Err().Mode(); mode != output.GlyphASCII {
			t.Fatalf("the layer selected glyph mode %v on an Ambiguous-wide terminal; want ASCII", mode)
		}
		sweep(t)
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^"+test+"$", "-test.v")
	// The child's environment is its own: with a UTF-8 locale set, ASCII
	// can only come from RUNEWIDTH_EASTASIAN, so the glyph-mode check above
	// cannot pass on a host that sets no locale at all.
	child.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(),
		"LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "RUNEWIDTH_EASTASIAN=1", env + "=1"}
	if v, ok := os.LookupEnv("GOCOVERDIR"); ok {
		child.Env = append(child.Env, "GOCOVERDIR="+v)
	}
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("the Ambiguous-wide sweep failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "swept ") {
		t.Fatalf("the child ran no sweep:\n%s", out)
	}
}

// TestTablesLoseNothingInAPipe: piped, with no width, every table holds
// every value its rows were built from, whole and with no truncation marker;
// every state cell renders its mark and its word; and the caption closes the
// table, with nothing hidden.
func TestTablesLoseNothingInAPipe(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		s := output.NewStreamAt(io.Discard, output.WidthUnbounded, false, output.ColourNone, ascii)
		marker := "…"
		if ascii {
			marker = "..."
		}
		for _, tb := range sweepTables(t) {
			got := tb.t.Render(s)
			for _, v := range tb.values {
				if v != "" && !strings.Contains(got, " "+v+" ") {
					t.Errorf("%s (ascii %v): the pipe lost the cell %q:\n%s", tb.name, ascii, v, got)
				}
			}
			for _, st := range tb.states {
				if ascii {
					st = strings.NewReplacer("✓ ", "+ ", "✗ ", "x ").Replace(st)
				}
				if !strings.Contains(got, " "+st+" ") {
					t.Errorf("%s (ascii %v): no state cell reads %q:\n%s", tb.name, ascii, st, got)
				}
			}
			if strings.Contains(got, marker) || strings.Contains(got, "Hidden:") || strings.Contains(got, "Narrowed:") ||
				!strings.HasSuffix(got, "\n"+tb.t.Caption) {
				t.Errorf("%s (ascii %v): the pipe cut, hid or narrowed something, or lost the caption %q:\n%s",
					tb.name, ascii, tb.t.Caption, got)
			}
		}
	}
}

// The tables baseline: each command piped at COLUMNS=80 and COLUMNS=40, in
// the streams fixture, with its exit code and both streams, in the format of
// testdata/error-protocol.golden. Re-record by naming the cases the change
// rewrites or adds, comma-separated:
//
//	go test ./cmd -run '^TestTablesBaseline$' -update-tables-baseline=<case>,<case>
//
// Any other case whose rendering differs from its record fails the run and
// nothing is written.

var updateTablesBaseline = flag.String("update-tables-baseline", "",
	"comma-separated names of the cases testdata/tables.golden may rewrite or add")

const tablesBaselineFile = "testdata/tables.golden"

// tablesCases are the baseline's cases, each command at both widths.
func tablesCases() []streamsRow {
	var cases []streamsRow
	for _, c := range []streamsRow{
		{name: "ws list", args: []string{"list"}},
		{name: "ws status", args: []string{"status"}, setup: withRepos},
		{name: "ws profiles", args: []string{"profiles"}},
		{name: "ws proxy profile list", args: []string{"proxy", "profile", "list"}},
		{name: "ws proxy profile list --reveal", args: []string{"proxy", "profile", "list", "--reveal"}},
		{name: "ws list: none", args: []string{"list"}, setup: withoutWorkspaces},
		{name: "ws profiles: none", args: []string{"profiles"}, setup: withoutProfiles},
		{name: "ws proxy profile list: none", args: []string{"proxy", "profile", "list"}, setup: withoutXrayProfiles},
	} {
		for _, w := range []string{"80", "40"} {
			row := c
			row.name, row.env = c.name+" at COLUMNS="+w, []string{"COLUMNS=" + w}
			cases = append(cases, row)
		}
	}
	return cases
}

func TestTablesBaseline(t *testing.T) {
	var got strings.Builder
	var order []string
	for _, c := range tablesCases() {
		fx := newStreamsFixture(t)
		if c.setup != nil {
			c.setup(t, fx)
		}
		code, stdout, stderr := runStreamsChild(t, fx, c)
		got.WriteString(renderErrorCase(c.name, code, stdout, stderr))
		order = append(order, c.name)
	}

	if *updateTablesBaseline != "" {
		allowed := map[string]bool{}
		for _, name := range strings.Split(*updateTablesBaseline, ",") {
			allowed[strings.TrimSpace(name)] = true
		}
		// A missing file is an empty record: every case is then new.
		recorded, err := os.ReadFile(tablesBaselineFile)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("reading %s: %v", tablesBaselineFile, err)
		}
		if refused := refusedRewrites(splitErrorCases(string(recorded)), splitErrorCases(got.String()), order, allowed); len(refused) > 0 {
			t.Fatalf("refusing to rewrite %s; nothing was written:\n%s", tablesBaselineFile, strings.Join(refused, "\n"))
		}
		if err := os.WriteFile(tablesBaselineFile, []byte(got.String()), 0o644); err != nil {
			t.Fatalf("writing %s: %v", tablesBaselineFile, err)
		}
		t.Logf("rewrote %s (%d cases; named: %s)", tablesBaselineFile, len(order), *updateTablesBaseline)
		return
	}

	want, err := os.ReadFile(tablesBaselineFile)
	if err != nil {
		t.Fatalf("reading %s: %v (record it with -update-tables-baseline)", tablesBaselineFile, err)
	}
	if got.String() == string(want) {
		return
	}
	gotCases, wantCases := splitErrorCases(got.String()), splitErrorCases(string(want))
	for _, name := range order {
		if gotCases[name] != wantCases[name] {
			t.Errorf("case %s differs from the baseline.\n--- baseline:\n%s--- now:\n%s", name, wantCases[name], gotCases[name])
		}
	}
	if len(wantCases) != len(order) {
		t.Errorf("the baseline holds %d cases and the table %d; re-record deliberately, not by accident", len(wantCases), len(order))
	}
	if !t.Failed() {
		t.Errorf("the baseline differs from the rendering outside any case block; re-record deliberately, not by accident")
	}
}

// TestWithReposIgnoresTheParentsGitEnvironment: the fixture's git commands
// run in the fixture's repositories whatever git environment the test
// inherits — a git hook exports GIT_DIR and GIT_WORK_TREE, and with them the
// fixture's commits and checkouts would land in the enclosing repository.
func TestWithReposIgnoresTheParentsGitEnvironment(t *testing.T) {
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("GIT_DIR", filepath.Join(elsewhere, ".git"))
	t.Setenv("GIT_WORK_TREE", elsewhere)
	fx := newStreamsFixture(t)
	withRepos(t, fx)
	if _, err := os.Lstat(elsewhere); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the fixture's git wrote to the inherited GIT_DIR: %v", err)
	}
	for _, repo := range []string{"workspace-cli", "vault-ai"} {
		if _, err := os.Stat(filepath.Join(fx.home, "projects", repo, ".git")); err != nil {
			t.Errorf("the fixture repository %s was not made: %v", repo, err)
		}
	}
}

// withoutGitEnv is env without its GIT_* variables.
func withoutGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

// withRepos gives ws status the fixture's three repositories, and links git
// onto the fixture's PATH: workspace-cli on main, clean, two commits ahead of
// its upstream and one behind; vault-ai on a branch, dirty, with no upstream;
// dotfiles not there.
func withRepos(t *testing.T, fx streamsFixture) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(fx.bin, "git")); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, append([]string{"-C", dir, "-c", "user.name=ws", "-c", "user.email=ws@example.com",
			"-c", "commit.gpgsign=false"}, args...)...)
		// The parent's GIT_* variables stay out: an enclosing git process —
		// a hook — exports GIT_DIR and GIT_WORK_TREE, which would point
		// these commands at its own repository.
		cmd.Env = append(withoutGitEnv(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cli, vault := filepath.Join(fx.home, "projects", "workspace-cli"), filepath.Join(fx.home, "projects", "vault-ai")
	for _, dir := range []string{cli, vault} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(cli, "init", "-q", "-b", "main")
	git(cli, "commit", "-q", "--allow-empty", "-m", "a")
	git(cli, "checkout", "-q", "-b", "upstream")
	git(cli, "commit", "-q", "--allow-empty", "-m", "d")
	git(cli, "checkout", "-q", "main")
	git(cli, "commit", "-q", "--allow-empty", "-m", "b")
	git(cli, "commit", "-q", "--allow-empty", "-m", "c")
	git(cli, "branch", "-q", "--set-upstream-to=upstream")
	git(vault, "init", "-q", "-b", "feat/render-tables")
	git(vault, "commit", "-q", "--allow-empty", "-m", "a")
	if err := os.WriteFile(filepath.Join(vault, "draft.md"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

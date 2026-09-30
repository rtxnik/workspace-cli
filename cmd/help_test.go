package cmd

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The help document's acceptance checks. They run over two trees: the real
// one, and a fixture that carries what the real one lacks.

// fixtureHook is the state of the fixture group's pre-run hook: how many
// times it ran, and whether it refuses, the way profileCmd's hook refuses a
// migration.
type fixtureHook struct {
	ran  int
	fail bool
}

// helpFixtureToken is the fixture's 200-character token with no break
// opportunity. Wrap hard-breaks it into lines exactly as wide as the budget,
// which is what keeps the width sweep's control above zero at every width.
var helpFixtureToken = strings.Repeat("x", 200)

// newHelpFixture builds the tree the help checks run over beside the real
// one. It carries one of each thing the real tree lacks: an Example; aliases;
// a hidden flag and a deprecated one; flags with a type and a non-zero
// default, a back-quoted name and a NoOptDefVal; the 200-character token;
// CJK in a Short; indented lines in a Long, one of them indented deeper than
// the narrowest terminal; a group with no available member; a root child
// with no group; and a group whose pre-run hook, h, sits over a leaf with a
// required flag.
//
// It is built fresh for every use: running a command leaves state on it —
// SilenceUsage, parsed flags — that the next run would read.
func newHelpFixture(h *fixtureHook) *cobra.Command {
	body := func(cmd *cobra.Command, _ []string) error {
		cmd.SilenceUsage = true
		return nil
	}
	root := &cobra.Command{
		Use:   "fx",
		Short: "Fixture root",
		Long: "The fixture's description — one line of prose.\n" +
			helpFixtureToken + "\n" +
			"\n" +
			"    an indented line, kept indented when it wraps: " + strings.Repeat("word ", 20) + "\n" +
			"\tand a line indented by a tab\n" +
			strings.Repeat(" ", 40) + "深い字下げ",
	}
	root.PersistentFlags().Bool("json", false, "Output in JSON format, where the command supports it")

	// The example's aliases line, "example, ex, sample, specimen", is 29
	// cells: wider than its budget at 29 and 30 columns (27 and 28), so the
	// sweep sees it wrap.
	example := &cobra.Command{
		Use:     "example <name>",
		Short:   "A leaf with an example and aliases",
		Aliases: []string{"ex", "sample", "specimen"},
		Example: "  fx example one\n" +
			"  fx example two --count 5\n" +
			"      a deeper line that wraps: " + strings.Repeat("wrapped ", 12),
		Annotations: map[string]string{"group": "workspace"},
		RunE:        body,
	}
	example.Flags().String("name", "fixture-default", "the `label` to print")
	example.Flags().IntP("count", "c", 3, "how many times")
	example.Flags().Duration("timeout", 5*time.Second, "how long to wait")
	example.Flags().String("mode", "", "an optional mode")
	example.Flags().Lookup("mode").NoOptDefVal = "auto"
	example.Flags().Bool("secret", false, "a hidden flag")
	_ = example.Flags().MarkHidden("secret")
	example.Flags().Bool("old", false, "a deprecated flag")
	_ = example.Flags().MarkDeprecated("old", "use --name")

	cjk := &cobra.Command{
		Use:         "cjk",
		Short:       "代理の状態を表示します — 全角の説明文",
		Annotations: map[string]string{"group": "workspace"},
		RunE:        body,
	}
	// The only member of the profile group, and hidden: the group has no
	// available member, so its heading must not appear.
	hidden := &cobra.Command{
		Use:         "hidden",
		Short:       "The profile group's only member, hidden",
		Hidden:      true,
		Annotations: map[string]string{"group": "profile"},
		RunE:        body,
	}
	loose := &cobra.Command{Use: "loose", Short: "A root child with no group", RunE: body}

	grp := &cobra.Command{
		Use:         "grp",
		Short:       "A group with a pre-run hook",
		Annotations: map[string]string{"group": "proxy"},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			h.ran++
			if h.fail {
				cmd.SilenceUsage = true
				return errors.New("fixture hook refused")
			}
			return nil
		},
	}
	needs := &cobra.Command{Use: "needs", Short: "A leaf with a required flag", RunE: body}
	needs.Flags().String("req", "", "a required flag")
	_ = needs.MarkFlagRequired("req")
	grp.AddCommand(needs)

	root.AddCommand(example, cjk, hidden, loose, grp)
	return root
}

// helpCommands is every command of a tree whose help can be asked for, root
// first. It adds cobra's help command first, as ExecuteC does. It leaves out
// cobra's completion plumbing (__complete, __completeNoDesc): ExecuteC adds
// it lazily, so whether it exists depends on what ran before in this process.
func helpCommands(root *cobra.Command) []*cobra.Command {
	root.InitDefaultHelpCmd()
	var out []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		out = append(out, c)
		for _, child := range c.Commands() {
			if !strings.HasPrefix(child.Name(), "__") {
				walk(child)
			}
		}
	}
	walk(root)
	return out
}

// bothTrees is the real tree's commands followed by the fixture's.
func bothTrees() []*cobra.Command {
	return append(helpCommands(rootCmd), helpCommands(newHelpFixture(&fixtureHook{}))...)
}

// TestHelpFitsEveryWidth is the deliverable: every command's help, in both
// trees, at every width from MinWidth to 200, in both glyph modes, with and
// without the description, and not one line wider than the width.
func TestHelpFitsEveryWidth(t *testing.T) {
	sweepHelpWidths(t)
}

// sweepHelpWidths measures with the layer's W, which is what the terminal's
// own arithmetic is modelled on. Its control re-checks the same renders
// against a budget one cell smaller: at every width some line must then be
// over it, or the sweep could not see an overflow of one cell. The fixture's
// token guarantees such a line — it is hard-broken into lines exactly W
// wide. Rendering at MinWidth-1 instead would prove nothing: the stream
// clamps any width below MinWidth up to it.
func sweepHelpWidths(t *testing.T) {
	t.Helper()
	cmds := bothTrees()
	renders, lines, over := 0, 0, 0
	for w := output.MinWidth; w <= 200; w++ {
		control := 0
		for _, ascii := range []bool{false, true} {
			s := output.NewStreamAt(io.Discard, w, true, output.ColourTrue, ascii)
			for _, c := range cmds {
				for _, withDescription := range []bool{true, false} {
					renders++
					for _, line := range strings.Split(renderHelp(s, c, withDescription), "\n") {
						lines++
						switch n := output.W(line); {
						case n > w:
							if over++; over <= 10 {
								t.Errorf("%s at %d columns: a line is %d cells wide: %q", c.CommandPath(), w, n, line)
							}
						case n == w:
							control++
						}
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
	t.Logf("swept %d renders, %d lines, over %d commands", renders, lines, len(cmds))
}

// TestHelpBelowTheFloorRendersAtTheFloor: a terminal narrower than MinWidth
// gets the help laid out at MinWidth, byte for byte. The stream keeps the
// width it resolved and clamps its budget; the renderer lays out against the
// budget, never the raw width, or its own sections and the blocks inside it
// would be laid out at two different widths.
func TestHelpBelowTheFloorRendersAtTheFloor(t *testing.T) {
	floor := output.NewStreamAt(io.Discard, output.MinWidth, true, output.ColourNone, false)
	for _, w := range []int{1, 10, output.MinWidth - 1} {
		s := output.NewStreamAt(io.Discard, w, true, output.ColourNone, false)
		for _, c := range bothTrees() {
			if renderHelp(s, c, true) != renderHelp(floor, c, true) {
				t.Errorf("%s at %d columns differs from its render at %d", c.CommandPath(), w, output.MinWidth)
			}
		}
	}
}

const helpAmbiWideEnv = "WS_TEST_HELP_AMBIWIDE"

// TestHelpFitsEveryWidthAmbiguousWide runs the same sweep under
// RUNEWIDTH_EASTASIAN=1, as the render layer's own §6.4 sweep does. The help
// carries East-Asian-Ambiguous characters — the em dash in several
// descriptions — which such a terminal draws two cells wide. It has to be a
// child process: x/ansi reads the variable in init(), so a test that set it
// and carried on would measure the narrow convention.
func TestHelpFitsEveryWidthAmbiguousWide(t *testing.T) {
	if os.Getenv(helpAmbiWideEnv) == "1" {
		// The child's control: the convention reached the measure.
		if n := output.W("—"); n != 2 {
			t.Fatalf("an em dash measures %d cells under RUNEWIDTH_EASTASIAN=1, want 2: the convention did not reach x/ansi", n)
		}
		sweepHelpWidths(t)
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestHelpFitsEveryWidthAmbiguousWide$", "-test.v")
	child.Env = append(os.Environ(), "RUNEWIDTH_EASTASIAN=1", helpAmbiWideEnv+"=1")
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("the Ambiguous-wide sweep failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "swept ") {
		t.Fatalf("the child ran no sweep:\n%s", out)
	}
}

// unspace is s with every whitespace character removed.
func unspace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// pflagLine is pflag's own unwrapped rendering of f, alone in a flag set.
func pflagLine(f *pflag.Flag) string {
	fs := pflag.NewFlagSet("single", pflag.ContinueOnError)
	fs.AddFlag(f)
	return fs.FlagUsagesWrapped(0)
}

// TestHelpLosesNothing: at every width, each field the document carries
// appears in it as one contiguous run once all whitespace is removed from
// both. The fields are the command's name, the description the renderer
// chose (Long, else Short), every listed command's name and Short, and every
// visible flag as pflag itself renders it — key and description together,
// so a row that split them apart would fail too. Removing whitespace rather
// than collapsing it keeps a token Wrap hard-broke across lines comparable
// with its source.
func TestHelpLosesNothing(t *testing.T) {
	cmds := bothTrees()
	checked := 0
	for w := output.MinWidth; w <= 200; w++ {
		s := output.NewStreamAt(io.Discard, w, true, output.ColourNone, false)
		for _, c := range cmds {
			doc := unspace(renderHelp(s, c, true))
			fields := []string{c.Name()}
			if c.Long != "" {
				fields = append(fields, c.Long)
			} else {
				fields = append(fields, c.Short)
			}
			for _, child := range c.Commands() {
				if child.IsAvailableCommand() || child.Name() == "help" {
					fields = append(fields, child.Name(), child.Short)
				}
			}
			for _, fs := range []*pflag.FlagSet{c.LocalFlags(), c.InheritedFlags()} {
				fs.VisitAll(func(f *pflag.Flag) {
					if !f.Hidden && f.Deprecated == "" {
						fields = append(fields, pflagLine(f))
					}
				})
			}
			for _, field := range fields {
				checked++
				if !strings.Contains(doc, unspace(field)) {
					t.Errorf("%s at %d columns lost %q", c.CommandPath(), w, field)
				}
			}
		}
	}
	t.Logf("checked %d fields", checked)
}

// TestFlagRowsMatchPflag holds flagKey and flagDescription to pflag's own
// rendering: for every visible flag of every command in both trees, the row,
// whitespace collapsed, equals pflag's unwrapped line for a set holding that
// flag alone.
func TestFlagRowsMatchPflag(t *testing.T) {
	collapse := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	checked := 0
	for _, c := range bothTrees() {
		c.InitDefaultHelpFlag()
		c.InitDefaultVersionFlag()
		for _, fs := range []*pflag.FlagSet{c.LocalFlags(), c.InheritedFlags()} {
			fs.VisitAll(func(f *pflag.Flag) {
				if f.Hidden || f.Deprecated != "" {
					return
				}
				checked++
				got := collapse(flagKey(f) + " " + flagDescription(f))
				if want := collapse(pflagLine(f)); got != want {
					t.Errorf("%s --%s: the row is %q, pflag renders %q", c.CommandPath(), f.Name, got, want)
				}
			})
		}
	}
	// A positive precondition: the loop above passes vacuously over a tree
	// whose flags it never reached.
	if checked < 100 {
		t.Fatalf("checked %d flags; the two trees carry more than 100", checked)
	}
}

// TestHelpGoesToStdout runs `ws <command> --help` for every command in a
// child process whose streams are pipes. The help lands on stdout, byte for
// byte the document renderHelp makes for an 80-column stream that is not a
// terminal (the child runs with COLUMNS=80); stderr is empty; and there is
// no ESC byte, because a pipe gets no colour and bubbletea sends its terminal
// query only to a terminal. The colour checks run on renders, never on a
// real pty, where that query would precede the help.
func TestHelpGoesToStdout(t *testing.T) {
	s := output.NewStreamAt(io.Discard, 80, false, output.ColourNone, false)
	for _, c := range helpCommands(rootCmd) {
		args := append(strings.Fields(c.CommandPath())[1:], "--help")
		code, stdout, stderr := runExecuteChild(t, errorCase{name: c.CommandPath() + " --help", args: args})
		if code != 0 || stderr != "" {
			t.Errorf("%s --help: exit %d, stderr %q; want exit 0 and nothing on stderr", c.CommandPath(), code, stderr)
		}
		if strings.Contains(stdout, "\x1b") {
			t.Errorf("%s --help wrote an ESC byte into a pipe: %q", c.CommandPath(), stdout)
		}
		if want := renderHelp(s, c, true) + "\n"; stdout != want {
			t.Errorf("%s --help wrote\n%s\nwant\n%s", c.CommandPath(), stdout, want)
		}
	}
}

// helpTitles are the section titles a help document can carry.
func helpTitles() map[string]bool {
	titles := map[string]bool{
		"Usage:": true, "Aliases:": true, "Examples:": true, "Commands:": true,
		"Additional Commands:": true, "Flags:": true, "Global Flags:": true,
	}
	for _, g := range commandGroups {
		titles[g.title] = true
	}
	return titles
}

// TestHelpColourIsTitlesOnly renders every command's help on terminal
// streams. Under NO_COLOR there is no ESC byte at all. With colour, every
// section title is exactly the title painted RoleAccent, and no other line
// carries an ESC byte.
func TestHelpColourIsTitlesOnly(t *testing.T) {
	titles := helpTitles()
	none := output.NewStreamAt(io.Discard, 80, true, output.ColourNone, false)
	coloured := output.NewStreamAt(io.Discard, 80, true, output.ColourTrue, false)
	for _, c := range bothTrees() {
		if doc := renderHelp(none, c, true); strings.Contains(doc, "\x1b") {
			t.Errorf("%s: an ESC byte on a terminal under NO_COLOR: %q", c.CommandPath(), doc)
		}
		painted := 0
		for _, line := range strings.Split(renderHelp(coloured, c, true), "\n") {
			plain := ansi.Strip(line)
			if titles[plain] {
				painted++
				if want := coloured.Style(output.RoleAccent).Render(plain); line != want {
					t.Errorf("%s: the title %q is %q, want %q", c.CommandPath(), plain, line, want)
				}
				continue
			}
			if strings.Contains(line, "\x1b") {
				t.Errorf("%s: a line that is not a section title carries colour: %q", c.CommandPath(), line)
			}
		}
		if painted == 0 {
			t.Errorf("%s: no section title found; every document has at least Usage:", c.CommandPath())
		}
	}
}

// TestRootChildrenCarryAGroup: every root child but completion and help sits
// in one of the root's groups. A child that lost its annotation would drop
// silently to "Additional Commands:".
func TestRootChildrenCarryAGroup(t *testing.T) {
	known := map[string]bool{}
	for _, g := range commandGroups {
		known[g.annotation] = true
	}
	checked := 0
	for _, c := range rootCmd.Commands() {
		if !c.IsAvailableCommand() || c.Name() == "completion" || c.Name() == "help" {
			continue
		}
		checked++
		if !known[c.Annotations["group"]] {
			t.Errorf("ws %s carries group %q, which is none of the root's", c.Name(), c.Annotations["group"])
		}
	}
	if checked < 10 {
		t.Fatalf("checked %d root children; the root has more than 10", checked)
	}
}

// findIn resolves a path in a tree, failing the test if it does not exist.
func findIn(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	c, rest, err := root.Find(path)
	if err != nil || len(rest) != 0 {
		t.Fatalf("%v: found %s with %v left over (%v)", path, c.CommandPath(), rest, err)
	}
	return c
}

// TestHelpSectionsAppearOnlyWithContent checks, on the fixture and on the
// real tree, that each section is printed when it has content and only then.
func TestHelpSectionsAppearOnlyWithContent(t *testing.T) {
	s := output.NewStreamAt(io.Discard, 80, false, output.ColourNone, false)
	fx := newHelpFixture(&fixtureHook{})
	fx.InitDefaultHelpCmd()

	root := renderHelp(s, fx, true)
	for _, want := range []string{
		"\n    an indented line, kept indented when it wraps:",
		"\n        and a line indented by a tab",
		"Workspace Commands:\n  cjk ",
		"Proxy Commands:\n  grp ",
		"Additional Commands:\n  help ",
		"\n  loose ",
		`Use "fx [command] --help" for more information about a command.`,
	} {
		if !strings.Contains(root, want) {
			t.Errorf("the fixture root's help lacks %q:\n%s", want, root)
		}
	}
	for _, absent := range []string{"Profile Commands:", "Vault Commands:", "hidden", "Aliases:", "Examples:"} {
		if strings.Contains(root, absent) {
			t.Errorf("the fixture root's help carries %q:\n%s", absent, root)
		}
	}

	example := renderHelp(s, findIn(t, fx, "example"), true)
	for _, want := range []string{
		"Aliases:\n  example, ex, sample, specimen\n",
		"Examples:\n    fx example one\n    fx example two --count 5\n        a deeper line",
		"--name label",
		`(default "fixture-default")`,
		"(default 5s)",
		`--mode string[="auto"]`,
	} {
		if !strings.Contains(example, want) {
			t.Errorf("the example leaf's help lacks %q:\n%s", want, example)
		}
	}
	for _, absent := range []string{"--secret", "--old", "[command] --help"} {
		if strings.Contains(example, absent) {
			t.Errorf("the example leaf's help carries %q:\n%s", absent, example)
		}
	}

	// The real tree: below the root one "Commands:" list, and no footer on
	// a leaf.
	if proxy := renderHelp(s, findIn(t, rootCmd, "proxy"), true); !strings.Contains(proxy, "\nCommands:\n") || strings.Contains(proxy, "Proxy Commands:") {
		t.Errorf("ws proxy's help lists its children other than under one Commands: heading:\n%s", proxy)
	}
	if list := renderHelp(s, findIn(t, rootCmd, "list"), true); strings.Contains(list, "[command] --help") {
		t.Errorf("ws list's help points at subcommands it does not have:\n%s", list)
	}
}

// failingWriter refuses every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write refused") }

// TestHelpFuncReportsAFailedWrite: a help function has no error return, so a
// failed write is reported on stderr instead of being dropped.
func TestHelpFuncReportsAFailedWrite(t *testing.T) {
	fx := newHelpFixture(&fixtureHook{})
	var errBuf bytes.Buffer
	fx.SetOut(failingWriter{})
	fx.SetErr(&errBuf)
	helpFunc(fx, nil)
	if !strings.Contains(errBuf.String(), "write refused") {
		t.Errorf("a failed help write left stderr %q; want the write error", errBuf.String())
	}
}

var updateHelpGolden = flag.Bool("update-help-golden", false,
	"rewrite testdata/help.golden from the current tree")

const helpGoldenFile = "testdata/help.golden"

// TestHelpGolden pins the help of six commands as it is piped — no width, no
// colour — and of ws proxy on a 40-column terminal without colour. It is the
// file a reviewer reads to see what the help looks like. Re-record with:
//
//	go test ./cmd -run '^TestHelpGolden$' -update-help-golden
func TestHelpGolden(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	piped := output.NewStreamAt(io.Discard, output.WidthUnbounded, false, output.ColourNone, false)
	narrow := output.NewStreamAt(io.Discard, 40, true, output.ColourNone, false)
	var got strings.Builder
	for _, c := range []struct {
		name string
		s    *output.Stream
		path []string
	}{
		{"ws --help, piped", piped, nil},
		{"ws proxy --help, piped", piped, []string{"proxy"}},
		{"ws proxy profile use --help, piped", piped, []string{"proxy", "profile", "use"}},
		{"ws list --help, piped", piped, []string{"list"}},
		{"ws vault doctor --help, piped", piped, []string{"vault", "doctor"}},
		{"ws completion --help, piped", piped, []string{"completion"}},
		{"ws proxy --help, 40-column terminal, no colour", narrow, []string{"proxy"}},
	} {
		fmt.Fprintf(&got, "=== %s\n", c.name)
		for _, line := range strings.Split(renderHelp(c.s, findIn(t, rootCmd, c.path...), true), "\n") {
			fmt.Fprintf(&got, "|%s\n", line)
		}
	}
	if *updateHelpGolden {
		if err := os.WriteFile(helpGoldenFile, []byte(got.String()), 0o644); err != nil {
			t.Fatalf("writing %s: %v", helpGoldenFile, err)
		}
		t.Logf("rewrote %s", helpGoldenFile)
		return
	}
	want, err := os.ReadFile(helpGoldenFile)
	if err != nil {
		t.Fatalf("reading %s: %v (record it with -update-help-golden)", helpGoldenFile, err)
	}
	if got.String() != string(want) {
		t.Errorf("the help differs from %s; re-record deliberately, not by accident.\n--- now:\n%s", helpGoldenFile, got.String())
	}
}

package cmd

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// noTerminalStub stands for the error a prompt returns without a terminal:
// ErrNoTTY under the text that names the stream, as output.PromptReady
// words it.
type noTerminalStub string

func (e noTerminalStub) Error() string { return string(e) }
func (noTerminalStub) Unwrap() error   { return output.ErrNoTTY }

// errNoTerminal is what a prompt seam is stubbed to return for "no
// terminal".
var errNoTerminal error = noTerminalStub("stdin is not a terminal")

// promptParse is what a leaf of promptTree was given by cobra's parse.
type promptParse struct {
	cmd  *cobra.Command
	args []string
}

// promptTree is a fresh command tree of the shapes the prompting commands
// have: `delete`, with its alias `rm`; `proxy profile rm`, under its
// parent's persistent --no-migrate; the real `vault ingest`, whose --reason
// is a string; and the root's persistent --json. Its leaves record their
// parse in got instead of running.
func promptTree(got *promptParse) *cobra.Command {
	record := func(c *cobra.Command, args []string) error {
		*got = promptParse{cmd: c, args: args}
		return nil
	}
	root := &cobra.Command{Use: "ws", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.PersistentFlags().Bool("json", false, "")

	del := &cobra.Command{Use: "delete <name>", Aliases: []string{"rm"}, Args: cobra.ExactArgs(1), RunE: record}
	del.Flags().BoolP("force", "f", false, "")

	rm := &cobra.Command{Use: "rm <name>", Args: cobra.ExactArgs(1), RunE: record}
	rm.Flags().BoolP("force", "f", false, "")
	profile := &cobra.Command{Use: "profile"}
	profile.PersistentFlags().Bool("no-migrate", false, "")
	profile.AddCommand(rm)
	proxy := &cobra.Command{Use: "proxy"}
	proxy.AddCommand(profile)

	ingest := newVaultIngestCmd()
	ingest.RunE = record
	vault := &cobra.Command{Use: "vault"}
	vault.AddCommand(ingest)

	root.AddCommand(del, proxy, vault)
	return root
}

// parsePrompt runs argv, the words after `ws`, through a fresh promptTree
// and returns what its leaf was given.
func parsePrompt(t *testing.T, argv []string) promptParse {
	t.Helper()
	var got promptParse
	root := promptTree(&got)
	root.SetArgs(argv)
	if err := root.Execute(); err != nil {
		t.Fatalf("a fresh tree could not parse %q: %v", argv, err)
	}
	if got.cmd == nil {
		t.Fatalf("a fresh tree parsed %q to no leaf", argv)
	}
	return got
}

// changedFlags is every flag of c that its parse marked Changed, by name,
// with its value.
func changedFlags(c *cobra.Command) map[string]string {
	m := map[string]string{}
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			m[f.Name] = f.Value.String()
		}
	})
	return m
}

// checkReparse asserts that words, parsed by a fresh tree, give orig's
// command, its positional arguments, and every flag orig's parse marked
// Changed with the same value, with skip set.
func checkReparse(t *testing.T, at string, orig promptParse, words []string, skip string) {
	t.Helper()
	if len(words) == 0 || words[0] != "ws" {
		t.Errorf("%s: the step %q does not begin with ws", at, words)
		return
	}
	again := parsePrompt(t, words[1:])
	if got, want := again.cmd.CommandPath(), orig.cmd.CommandPath(); got != want {
		t.Errorf("%s: %q parses to %q; want %q", at, words, got, want)
	}
	if !slices.Equal(again.args, orig.args) {
		t.Errorf("%s: %q parses to the arguments %q; want %q", at, words, again.args, orig.args)
	}
	want := changedFlags(orig.cmd)
	want[skip] = "true"
	if got := changedFlags(again.cmd); !maps.Equal(got, want) {
		t.Errorf("%s: %q parses to the flags %v; want %v", at, words, got, want)
	}
}

// splitBySh hands text to a POSIX shell as the arguments of `set --` and
// returns the words the shell made of them.
func splitBySh(t *testing.T, text string) []string {
	t.Helper()
	script := "set -- " + text + "\nfor a do printf '%s\\0' \"$a\"; done\n"
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sh could not read the step %q: %v", text, err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

// TestRemedyWords: the words are the invocation as cobra parsed it, in one
// form — the command's path, so that an alias reads as the command's name;
// the flags the parse set, inherited ones included, in pflag's order, a
// boolean set to true bare; the skip flag, once; then the positional
// arguments, after a -- when one of them begins with a dash.
func TestRemedyWords(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		skip string
		want []string
	}{
		{[]string{"delete", "ops"}, "force", []string{"ws", "delete", "--force", "ops"}},
		{[]string{"rm", "ops"}, "force", []string{"ws", "delete", "--force", "ops"}},
		{[]string{"--json", "delete", "ops"}, "force", []string{"ws", "delete", "--json", "--force", "ops"}},
		{[]string{"delete", "--force=false", "ops"}, "force", []string{"ws", "delete", "--force", "ops"}},
		{[]string{"vault", "ingest", "--dedup-force", "--reason=x", "--", "-draft.md"}, "yes",
			[]string{"ws", "vault", "ingest", "--dedup-force", "--reason=x", "--yes", "--", "-draft.md"}},
		{[]string{"proxy", "profile", "--no-migrate", "rm", "backup"}, "force",
			[]string{"ws", "proxy", "profile", "rm", "--no-migrate", "--force", "backup"}},
	} {
		p := parsePrompt(t, tc.argv)
		if got := remedyWords(p.cmd, p.args, tc.skip); !slices.Equal(got, tc.want) {
			t.Errorf("ws %q: remedyWords = %q; want %q", tc.argv, got, tc.want)
		}
	}
}

// TestRemedyWordsAfterAnEarlierRun: in one process, a flag set's record of
// the flags it parsed is never cleared. After a run that set --reason, and a
// reset of the leaf's flags as the in-process tests reset them, the step of
// a run that did not set it does not carry it.
func TestRemedyWordsAfterAnEarlierRun(t *testing.T) {
	var got promptParse
	root := promptTree(&got)
	root.SetArgs([]string{"vault", "ingest", "--dedup-force", "--reason=first", "one.md"})
	if err := root.Execute(); err != nil {
		t.Fatalf("the first run: %v", err)
	}
	got.cmd.Flags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	root.SetArgs([]string{"vault", "ingest", "--dedup-force", "two.md"})
	if err := root.Execute(); err != nil {
		t.Fatalf("the second run: %v", err)
	}
	want := []string{"ws", "vault", "ingest", "--dedup-force", "--yes", "two.md"}
	if words := remedyWords(got.cmd, got.args, "yes"); !slices.Equal(words, want) {
		t.Errorf("after a run with --reason=first, remedyWords = %q; want %q", words, want)
	}
}

// TestSkipStepNamesTheFlagAlone: when a word of the invocation would not
// show on a terminal as it is — a newline, a tab, an ESC, DEL, U+009B, a raw
// 0x9B byte, invalid UTF-8, U+202E — no word is echoed, and the step names
// the skip flag alone. A positional argument and a flag's value are read
// alike.
func TestSkipStepNamesTheFlagAlone(t *testing.T) {
	for name, word := range map[string]string{
		"a newline":       "a\nb",
		"a tab":           "a\tb",
		"an ESC":          "a\x1bb",
		"DEL":             "a\x7fb",
		"U+009B":          "a" + string(rune(0x9b)) + "b",
		"a raw 0x9B byte": "a\x9bb",
		"invalid UTF-8":   "a\xffb",
		"U+202E":          "a" + string(rune(0x202e)) + "b",
	} {
		for _, tc := range []struct {
			argv []string
			skip string
		}{
			{[]string{"delete", word}, "force"},
			{[]string{"vault", "ingest", "--dedup-force", "--reason=" + word, "note.md"}, "yes"},
		} {
			p := parsePrompt(t, tc.argv)
			want := output.Remedy{Label: "Re-run with", Cmd: "--" + tc.skip}
			if got := skipStep(p.cmd, p.args, tc.skip); !reflect.DeepEqual(got, want) {
				t.Errorf("%s in ws %q: the step is %+v; want %+v", name, tc.argv, got, want)
			}
		}
	}
}

// TestPromptProblemRoundTrip: the step of the Problem a command returns when
// its prompt could not ask pastes back as the invocation. Rendered by
// Problem.Render at 29, 40 and 80 columns and unbounded, its lines split by
// a POSIX shell give words that a fresh command tree parses to the original
// parse with the skip flag set — for invocations through an alias, with
// inherited flags, and holding a ', a space, an empty argument, an argument
// that begins with - or with =, an empty --reason and one long enough to
// wrap.
func TestPromptProblemRoundTrip(t *testing.T) {
	long := strings.Repeat("duplicate of last week's note ", 4)
	const label = "1. Skip the prompt"
	for _, tc := range []struct {
		argv []string
		skip string
	}{
		{[]string{"rm", "ops"}, "force"},
		{[]string{"--json", "delete", ""}, "force"},
		{[]string{"delete", "=name"}, "force"},
		{[]string{"vault", "ingest", "--dedup-force", "--reason", "it's the weekly note, again", "notes/2026-10-10 stand-up.md"}, "yes"},
		{[]string{"vault", "ingest", "--dedup-force", "--reason", long, "--", "-draft.md"}, "yes"},
		{[]string{"vault", "ingest", "--dedup-force", "--reason=", "note.md"}, "yes"},
		{[]string{"proxy", "profile", "--no-migrate", "rm", "--", "-x"}, "force"},
	} {
		orig := parsePrompt(t, tc.argv)
		p, ok := output.ProblemOf(promptProblem(orig.cmd, orig.args, errNoTerminal, "Cannot ask for confirmation: no terminal", tc.skip))
		if !ok {
			t.Fatalf("ws %q: no Problem for a prompt with no terminal", tc.argv)
		}
		for _, width := range []int{output.MinWidth, 40, 80, output.WidthUnbounded} {
			rendered := p.Render(output.NewStreamAt(io.Discard, width, false, output.ColourNone, false))
			at := strings.Index(rendered, label)
			if at < 0 {
				t.Fatalf("ws %q at %d: the step's label is missing:\n%s", tc.argv, width, rendered)
			}
			step := strings.TrimLeft(rendered[at+len(label):], " \n")
			checkReparse(t, fmt.Sprintf("ws %q at %d", tc.argv, width), orig, splitBySh(t, step), tc.skip)
		}
	}
}

// TestPromptProblem: a prompt with no terminal becomes a Problem whose title
// is the caller's, whose cause is the error's text — the stream that is not
// a terminal — and whose one step is the invocation with the skip flag. The
// Problem wraps the error, so errors.Is still finds ErrNoTTY at the root.
func TestPromptProblem(t *testing.T) {
	orig := parsePrompt(t, []string{"delete", "ops"})
	err := promptProblem(orig.cmd, orig.args, errNoTerminal, "Cannot ask for confirmation: no terminal", "force")
	if !errors.Is(err, output.ErrNoTTY) {
		t.Errorf("errors.Is(%v, ErrNoTTY) = false; want true", err)
	}
	want := output.Problem{
		Title: "Cannot ask for confirmation: no terminal",
		Cause: "stdin is not a terminal",
		Steps: []output.Remedy{{Label: "Skip the prompt", Words: []string{"ws", "delete", "--force", "ops"}}},
	}
	if p, ok := output.ProblemOf(err); !ok || !reflect.DeepEqual(p, want) {
		t.Errorf("the Problem is %+v, %t; want %+v", p, ok, want)
	}
}

// TestPromptProblemPassesTheRestThrough: a cancellation and a prompt that
// failed are returned as they are, so that the root dies of the
// cancellation's signal, printing nothing, and prints the failure as its
// own title — never as a prompt that had no terminal.
func TestPromptProblemPassesTheRestThrough(t *testing.T) {
	orig := parsePrompt(t, []string{"delete", "ops"})
	for _, tc := range []struct {
		err  error
		msg  string
		code int
	}{
		{&output.Cancelled{Signal: syscall.SIGINT}, "", 130},
		{&output.Cancelled{Signal: syscall.SIGTERM}, "", 143},
		{&output.Cancelled{Signal: syscall.SIGHUP}, "", 129},
		{errors.New("prompt failed: raw mode refused"), "prompt failed: raw mode refused", 1},
	} {
		got := promptProblem(orig.cmd, orig.args, tc.err, "Cannot ask for confirmation: no terminal", "force")
		if got != tc.err {
			t.Errorf("promptProblem(%v) returned a %T; want the error itself, a %T", tc.err, got, tc.err)
		}
		msg, code := run(got)
		if msg != tc.msg || code != tc.code {
			t.Errorf("run(%v) = (%q, %d); want (%q, %d)", got, msg, code, tc.msg, tc.code)
		}
		p, printed := rootProblem(msg, got)
		if printed != (tc.msg != "") || (printed && !reflect.DeepEqual(p, output.Problem{Title: tc.msg})) {
			t.Errorf("for %v the root prints %+v, %t; want the title %q alone, or nothing for none", got, p, printed, tc.msg)
		}
	}
}

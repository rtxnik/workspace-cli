package output

import (
	"io"
	"os/exec"
	"strings"
	"testing"
)

// TestShellQuoteWritesWords: a word of the bare class is written as it is;
// every other word, the empty one and one beginning with = included, in
// single quotes, a single quote inside it ended, escaped and reopened.
func TestShellQuoteWritesWords(t *testing.T) {
	for _, tc := range []struct{ word, want string }{
		{"ops", "ops"},
		{"--force", "--force"},
		{"--reason=x", "--reason=x"},
		{"-draft.md", "-draft.md"},
		{"a/b:c@d%e+f,g_h.i=j", "a/b:c@d%e+f,g_h.i=j"},
		{"", "''"},
		{"=cmd", "'=cmd'"},
		{"two words", "'two words'"},
		{"it's", `'it'\''s'`},
		{"''", `''\'''\'''`},
		{"$HOME", "'$HOME'"},
		{"*.md", "'*.md'"},
		{"~", "'~'"},
		{"a;b", "'a;b'"},
		{"café.md", "'café.md'"},
	} {
		got, ok := ShellQuote(tc.word)
		if !ok || got != tc.want {
			t.Errorf("ShellQuote(%q) = %q, %t; want %q, true", tc.word, got, ok, tc.want)
		}
	}
}

// TestShellQuoteRefusesWhatATerminalWouldNotShow: a word holding what the
// terminal would not show as it is is not written at all.
func TestShellQuoteRefusesWhatATerminalWouldNotShow(t *testing.T) {
	for name, word := range map[string]string{
		"a newline":            "a\nb",
		"a tab":                "a\tb",
		"an ESC":               "a\x1bb",
		"an escape sequence":   "\x1b[31mred",
		"DEL":                  "a\x7fb",
		"U+009B":               "a\u009bb",
		"a raw 0x9B byte":      "a\x9bb",
		"invalid UTF-8":        "a\xffb",
		"U+202E":               "a" + string(rune(0x202e)) + "b",
		"U+2066":               "a" + string(rune(0x2066)) + "b",
		"a zero-width space":   "a" + string(rune(0x200b)) + "b",
		"a C0 control":         "a\x01b",
		"a bell":               "a\ab",
		"a carriage return":    "a\rb",
		"an OSC with its text": "\x1b]0;pwned\x07",
	} {
		if got, ok := ShellQuote(word); ok {
			t.Errorf("%s: ShellQuote(%q) = %q, true; want it refused", name, word, got)
		}
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

// TestRemedyWordsRoundTrip: a step given as words, rendered by
// Problem.Render at 29, 40 and 80 columns and unbounded, gives back exactly
// the words it was built from when a POSIX shell splits it — whatever the
// words hold: a ', a space, two spaces, an empty word, a word that begins
// with - or with =, a --reason long enough to wrap. A rendered line wider
// than the budget holds a single word.
func TestRemedyWordsRoundTrip(t *testing.T) {
	long := "--reason=" + strings.Repeat("duplicate of last week's note ", 4)
	argvs := [][]string{
		{"ws", "delete", "--force", "ops"},
		{"ws", "vault", "ingest", "--dedup-force", "--reason=it's the weekly note, again", "--yes", "--", "-draft.md"},
		{"ws", "vault", "ingest", "--dedup-force", long, "--yes", "notes/2026-10-10 stand-up.md"},
		{"ws", "vault", "ingest", "--dedup-force", "--reason=", "--yes", "note.md"},
		{"ws", "proxy", "profile", "rm", "--force", "--", "-x"},
		{"ws", "delete", "--force", "=name"},
		{"ws", "x", "", "a  b", "'", `\`},
	}
	const label = "1. Skip the prompt"
	for _, argv := range argvs {
		quoted := make([]string, len(argv))
		for i, word := range argv {
			q, ok := ShellQuote(word)
			if !ok {
				t.Fatalf("ShellQuote(%q) refused a word a terminal shows", word)
			}
			quoted[i] = q
		}
		p := Problem{
			Title: "Cannot ask for confirmation: no terminal",
			Cause: "stdin is not a terminal",
			Steps: []Remedy{{Label: "Skip the prompt", Words: quoted}},
		}
		for _, width := range []int{MinWidth, 40, 80, WidthUnbounded} {
			rendered := p.Render(NewStreamAt(io.Discard, width, false, ColourNone, false))
			at := strings.Index(rendered, label)
			if at < 0 {
				t.Fatalf("at %d the step's label is missing:\n%s", width, rendered)
			}
			step := strings.TrimLeft(rendered[at+len(label):], " \n")
			if got := splitBySh(t, step); strings.Join(got, "\x00") != strings.Join(argv, "\x00") {
				t.Errorf("at %d the shell read %q back from\n%s\nwant %q", width, got, step, argv)
			}
			for _, line := range strings.Split(rendered, "\n") {
				if W(line) <= width {
					continue
				}
				if word := strings.TrimSpace(strings.TrimSuffix(line, " \\")); !contains(quoted, word) {
					t.Errorf("at %d a line %d cells wide holds more than one word: %q", width, W(line), line)
				}
			}
		}
	}
}

// TestRemedyWordsLayout: where the words do not fit, the step breaks between
// them, ends each broken line with " \" and indents the next by two more
// than the first.
func TestRemedyWordsLayout(t *testing.T) {
	p := Problem{Title: "Cannot ask for confirmation: no terminal", Steps: []Remedy{{Label: "Skip the prompt",
		Words: []string{"ws", "vault", "ingest", "--dedup-force", "--reason=x", "--yes", "--", "-draft.md"}}}}
	rendered := p.Render(NewStreamAt(io.Discard, MinWidth, false, ColourNone, false))
	want := "  1. Skip the prompt\n" +
		"     ws vault ingest \\\n" +
		"       --dedup-force \\\n" +
		"       --reason=x --yes -- \\\n" +
		"       -draft.md"
	if !strings.HasSuffix(rendered, "\n"+want) {
		t.Errorf("at %d the step is laid out\n%s\nwant it to end\n%s", MinWidth, rendered, want)
	}
}

func contains(words []string, word string) bool {
	for _, w := range words {
		if w == word {
			return true
		}
	}
	return false
}

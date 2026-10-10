package output

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"
)

// The screen-reader mode, run through huh's real accessible implementation
// with the tests' own readers.

// accessibleCore is a core in the screen-reader mode that reads in and draws
// on w. Its normal mode must never run.
func accessibleCore(t *testing.T, in io.Reader, w io.Writer) *prompter {
	p := coreFor(make(chan os.Signal, 3), func(*huh.Form) error {
		t.Error("the normal mode ran")
		return nil
	})
	p.dumb = func() bool { return true }
	p.input = in
	p.stream = NewStreamAt(w, 80, false, ColourNone, false)
	return p
}

// readStep is one Read of a scriptedReader: its bytes and its error.
type readStep struct {
	data string
	err  error
}

// scriptedReader answers each Read with its next step, and the end of input
// once the steps run out. It counts the reads that came after it first
// returned the end of input.
type scriptedReader struct {
	steps       []readStep
	sawEOF      bool
	readsPastIt int
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if r.sawEOF {
		r.readsPastIt++
	}
	if len(r.steps) == 0 {
		r.sawEOF = true
		return 0, io.EOF
	}
	s := r.steps[0]
	r.steps = r.steps[1:]
	if errors.Is(s.err, io.EOF) {
		r.sawEOF = true
	}
	return copy(p, s.data), s.err
}

var errEIO = errors.New("read /dev/stdin: input/output error")

// TestScreenReaderAnswer: rule 4 — an answer ended by Enter is the answer.
func TestScreenReaderAnswer(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{{"y\n", true}, {"n\n", false}} {
		got, err := accessibleCore(t, strings.NewReader(tc.line), io.Discard).confirm(`Delete workspace "ops"?`, "")
		if err != nil || got != tc.want {
			t.Errorf("Confirm answered %q: (%t, %v); want (%t, nil)", tc.line, got, err, tc.want)
		}
	}
	got, err := accessibleCore(t, strings.NewReader("2\n"), io.Discard).choose("Select workspace:", threeWorkspaces())
	if err != nil || got != "ops" {
		t.Errorf("Select answered 2: (%q, %v); want (\"ops\", nil)", got, err)
	}
}

// TestScreenReaderEndOfInputCancels: rule 2 — the end of input on an empty
// prompt, and after text typed without Enter, is a cancellation by SIGINT,
// not the default and not the text.
func TestScreenReaderEndOfInputCancels(t *testing.T) {
	for _, input := range []string{"", "n", "y"} {
		_, err := accessibleCore(t, strings.NewReader(input), io.Discard).confirm(`Delete workspace "ops"?`, "")
		if cancelledBy(err) != os.Interrupt {
			t.Errorf("%q then the end of input: %v; want a Cancelled by SIGINT", input, err)
		}
	}
}

// TestScreenReaderReadErrorFails: rule 1 — a reader that fails is a failed
// prompt that carries the read error, never the default answer.
func TestScreenReaderReadErrorFails(t *testing.T) {
	in := &scriptedReader{steps: []readStep{{"", errEIO}}}
	ok, err := accessibleCore(t, in, io.Discard).confirm(`Delete workspace "ops"?`, "")
	if ok || !errors.Is(err, errEIO) || !strings.HasPrefix(err.Error(), "prompt failed: ") {
		t.Errorf("a failing reader: (%t, %v); want a failed prompt carrying the read error", ok, err)
	}
}

// TestScreenReaderOverlongLineFails: rule 1 — a line as long as huh's
// scanner can hold before its newline is a failed prompt, never the default;
// one byte shorter is the answer. The newline comes in a read of its own, so
// every byte before it is counted as a run without one.
func TestScreenReaderOverlongLineFails(t *testing.T) {
	line := func(n int) io.Reader {
		return io.MultiReader(strings.NewReader(strings.Repeat("x", n)), strings.NewReader("\n"))
	}
	var reason string
	form := huh.NewForm(huh.NewGroup(huh.NewInput().Title("Reason").Value(&reason)))
	err := accessibleCore(t, line(bufio.MaxScanTokenSize), io.Discard).ask(form)
	if !errors.Is(err, errLineTooLong) {
		t.Errorf("a line of %d bytes: %v; want a failed prompt for the overlong line", bufio.MaxScanTokenSize, err)
	}
	reason = ""
	form = huh.NewForm(huh.NewGroup(huh.NewInput().Title("Reason").Value(&reason)))
	if err := accessibleCore(t, line(bufio.MaxScanTokenSize-1), io.Discard).ask(form); err != nil || len(reason) != bufio.MaxScanTokenSize-1 {
		t.Errorf("a line one byte shorter: %v, %d bytes kept; want the answer whole", err, len(reason))
	}
}

// TestScreenReaderRejectedNumberDoesNotEscape: huh's Select indexes its
// options with the last rejected number when the scan then fails, and
// panics; the panic is recovered and the recorder decides — the end of input
// is a cancellation, a read error a failed prompt.
func TestScreenReaderRejectedNumberDoesNotEscape(t *testing.T) {
	in := &scriptedReader{steps: []readStep{{"4\n", nil}, {"", io.EOF}}}
	_, err := accessibleCore(t, in, io.Discard).choose("Select workspace:", threeWorkspaces())
	if cancelledBy(err) != os.Interrupt {
		t.Errorf("4 then the end of input: %v; want a Cancelled by SIGINT", err)
	}
	in = &scriptedReader{steps: []readStep{{"abc\n", nil}, {"", errEIO}}}
	_, err = accessibleCore(t, in, io.Discard).choose("Select workspace:", threeWorkspaces())
	if !errors.Is(err, errEIO) {
		t.Errorf("abc then a read error: %v; want a failed prompt carrying the read error", err)
	}
	var picked []string
	tools := huh.NewOptions("go", "node", "python", "rust", "java", "ruby", "deno", "bun")
	form := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().Title("Mise tools").Options(tools...).Value(&picked)))
	in = &scriptedReader{steps: []readStep{{"9\n", nil}, {"", io.EOF}}}
	if err := accessibleCore(t, in, io.Discard).ask(form); cancelledBy(err) != os.Interrupt {
		t.Errorf("a MultiSelect of eight answered 9 then the end of input: %v; want a Cancelled by SIGINT", err)
	}
}

// wizardShaped is a form shaped like the profile wizard's: a Select, an
// Input, a MultiSelect and a Confirm, each in a group of its own.
type wizardShaped struct {
	image, packages string
	tools           []string
	dind            bool
}

func (w *wizardShaped) form() *huh.Form {
	return huh.NewForm(
		huh.NewGroup(huh.NewSelect[string]().Title("Base image").
			Options(huh.NewOptions("ubuntu", "debian", "alpine")...).Value(&w.image)),
		huh.NewGroup(huh.NewInput().Title("System packages").Value(&w.packages)),
		huh.NewGroup(huh.NewMultiSelect[string]().Title("Mise tools to install").
			Options(huh.NewOptions("go", "node", "python")...).Value(&w.tools)),
		huh.NewGroup(huh.NewConfirm().Title("Enable Docker-in-Docker?").Value(&w.dind)),
	)
}

// TestScreenReaderOneLinePerRead: a reader that returns the whole input in
// one read answers every field — huh's scanner per prompt is handed one line
// at a time, so no line is lost with the scanner that read ahead.
func TestScreenReaderOneLinePerRead(t *testing.T) {
	var w wizardShaped
	err := accessibleCore(t, strings.NewReader("2\njq\n1\n0\ny\n"), io.Discard).ask(w.form())
	if err != nil || w.image != "debian" || w.packages != "jq" || len(w.tools) != 1 || w.tools[0] != "go" || !w.dind {
		t.Errorf("the whole input in one read: %v, answers %+v; want nil and debian, jq, [go], true", err, w)
	}
}

// TestScreenReaderLatches: a reader whose end of input is not sticky, as a
// terminal's is not, ends the form whole at its first end of input: the
// lines after it are never read, and the form is cancelled.
func TestScreenReaderLatches(t *testing.T) {
	var w wizardShaped
	in := &scriptedReader{steps: []readStep{{"2\n", nil}, {"", io.EOF}, {"jq\n", nil}, {"0\n", nil}, {"y\n", nil}}}
	err := accessibleCore(t, in, io.Discard).ask(w.form())
	if cancelledBy(err) != os.Interrupt {
		t.Errorf("the end of input at the second field: %v; want a Cancelled by SIGINT", err)
	}
	if in.readsPastIt != 0 {
		t.Errorf("stdin was read %d time(s) after its end of input; want none", in.readsPastIt)
	}
}

// brokenField is a field whose screen-reader run panics on its own.
type brokenField struct{ *huh.Input }

func (f brokenField) WithAccessible(bool) huh.Field { return f }

func (brokenField) RunAccessible(io.Writer, io.Reader) error { panic("field broke") }

// TestScreenReaderPanicWithoutTheRecorderFails: rule 3 — a panic with
// neither the end of input nor a read error recorded is a failed prompt.
func TestScreenReaderPanicWithoutTheRecorderFails(t *testing.T) {
	form := huh.NewForm(huh.NewGroup(brokenField{huh.NewInput().Title("Name")}))
	err := accessibleCore(t, strings.NewReader("x\n"), io.Discard).ask(form)
	if err == nil || err.Error() != "prompt failed: field broke" {
		t.Errorf("a field that panics: %v; want prompt failed: field broke", err)
	}
}

// TestScreenReaderHuhErrorFails: rule 3 — an error huh returns on its own,
// with nothing recorded, is a failed prompt: a timeout, which this mode
// does not support.
func TestScreenReaderHuhErrorFails(t *testing.T) {
	form := huh.NewForm(huh.NewGroup(huh.NewInput().Title("Name"))).WithTimeout(time.Minute)
	if err := accessibleCore(t, strings.NewReader("x\n"), io.Discard).ask(form); !errors.Is(err, huh.ErrTimeoutUnsupported) {
		t.Errorf("a timeout in the screen-reader mode: %v; want a failed prompt carrying huh's error", err)
	}
}

// screenReaderProbes is the screen-reader mode's contract, planted against
// by TestContractMutationHarness in mutation_test.go and run clean by
// TestScreenReaderContract.
func screenReaderProbes() []contractProbe {
	return []contractProbe{probeScreenReaderEOF, probeScreenReaderLatch, probeScreenReaderStream}
}

// TestScreenReaderContract runs the screen-reader mode's probes on the
// shipped code.
func TestScreenReaderContract(t *testing.T) {
	for _, p := range screenReaderProbes() {
		t.Run(p.name, func(t *testing.T) {
			r := newResults()
			p.run(t, r)
			r.report(t)
		})
	}
}

// probeScreenReaderEOF: text ended by the end of input, not by Enter, is a
// cancellation, not the answer huh scans out of it.
var probeScreenReaderEOF = contractProbe{
	name: "screen_reader_end_of_input",
	spec: "phase 6 §4.1",
	what: "in the screen-reader mode, n then the end of input is a Cancelled by SIGINT, not the answer",
	run: func(t *testing.T, r *results) string {
		ok, err := accessibleCore(t, strings.NewReader("n"), io.Discard).confirm("Delete?", "")
		if cancelledBy(err) != os.Interrupt {
			r.fail("screen_reader_end_of_input", "n then the end of input returned (%t, %v); want a Cancelled by SIGINT", ok, err)
		}
		return fmt.Sprintf("%t %v", ok, err)
	},
}

// probeScreenReaderLatch: a terminal's end of input is not sticky, and the
// form ends at the first one, reading nothing after it.
var probeScreenReaderLatch = contractProbe{
	name: "screen_reader_latch",
	spec: "phase 6 §4.1",
	what: "in the screen-reader mode, nothing is read from stdin after its first end of input",
	run: func(t *testing.T, r *results) string {
		var w wizardShaped
		in := &scriptedReader{steps: []readStep{{"2\n", nil}, {"", io.EOF}, {"jq\n", nil}, {"0\n", nil}, {"y\n", nil}}}
		err := accessibleCore(t, in, io.Discard).ask(w.form())
		if in.readsPastIt != 0 {
			r.fail("screen_reader_latch", "stdin was read %d time(s) after its end of input", in.readsPastIt)
		}
		return fmt.Sprintf("reads after the end %d, %v", in.readsPastIt, err)
	},
}

// probeScreenReaderStream: the prompt is drawn on the stream the core is
// given, never on stdout.
var probeScreenReaderStream = contractProbe{
	name: "screen_reader_stream",
	spec: "phase 6 §4.1",
	what: "in the screen-reader mode the prompt is drawn on its stream, and stdout stays empty",
	run: func(t *testing.T, r *results) string {
		var drawn syncBuffer
		stdout, _ := capture(t, func() {
			_, _ = accessibleCore(t, strings.NewReader("y\n"), &drawn).confirm("Delete?", "")
		})
		if stdout != "" || drawn.String() == "" {
			r.fail("screen_reader_stream", "the prompt wrote %q to stdout and %q to its stream", stdout, drawn.String())
		}
		return fmt.Sprintf("stdout %q, drawn %t", stdout, drawn.String() != "")
	},
}

// TestScreenReaderDrawsOnItsStream: the prompt is written to the stream the
// core is given — stderr — never to huh's default, stdout.
func TestScreenReaderDrawsOnItsStream(t *testing.T) {
	var drawn syncBuffer
	var err error
	stdout, stderr := capture(t, func() {
		_, err = accessibleCore(t, strings.NewReader("y\n"), &drawn).confirm(`Delete workspace "ops"?`, "")
	})
	if err != nil {
		t.Fatalf("the prompt: %v", err)
	}
	if !strings.Contains(drawn.String(), `Delete workspace "ops"?`) {
		t.Errorf("the prompt's stream holds %q; want the prompt", drawn.String())
	}
	if stdout != "" || stderr != "" {
		t.Errorf("the prompt wrote stdout %q and stderr %q outside its stream; want nothing", stdout, stderr)
	}
}

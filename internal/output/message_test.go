package output

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The five message helpers.
//
// They are phase 0's entire deliverable and the dominant call volume — 137
// call sites when phase 0 landed — and they were at 0.0 % coverage on main,
// which is why routing every one of them to stdout, or leaving them
// unwrapped, passed the whole suite before these assertions existed.
//
// The two assertions TestContractMutationHarness in mutation_test.go plants
// defects against are contractProbe values, not plain tests: the mutation
// harness has to be able to ask WHICH assertion noticed, and a *testing.T
// reports to the framework rather than to the caller. TestMessageContract
// below is the ordinary entry point that runs them clean.

// ---------------------------------------------------------------- helpers

// squash removes every space, tab and newline. THIS IS THE PACKAGE'S ONE
// squash — Tasks 8, 9 and 10 consume it and declare no second copy.
//
// Wrapping claims are asserted through it because they are claims about
// CHARACTERS SURVIVING rather than about layout: a wrapped line break, a
// hanging indent and a hard break at the budget are all whitespace the
// renderer is entitled to introduce, and a comparison that did not ignore them
// would fail on correct output.
func squash(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// capture runs fn with stdout and stderr replaced by two INDEPENDENT pipes and
// returns what was written to each. Two pipes, not one: a single capture cannot
// tell "the message went to stderr" from "the message went to stdout and both
// are the same file", which is precisely the defect being looked for.
//
// withStdStreams comes from Task 4's stream_contract_test.go and resets the
// Out()/Err() memoisation on both sides, so nothing leaks into or out of a case.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	var wg sync.WaitGroup
	var outBuf, errBuf strings.Builder
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()

	withStdStreams(outW, errW, fn)
	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	_ = outR.Close()
	_ = errR.Close()
	return outBuf.String(), errBuf.String()
}

// --------------------------------------------------- §4.7 message routing

// messageHelpers is §4.7's table as data: the exported call, and the state
// whose mark it must carry. Every one of them belongs to stderr — stdout is
// the answer, stderr is everything about producing it.
var messageHelpers = []struct {
	name    string
	call    func(string)
	mark    State
	hasMark bool
	indent  int
}{
	{"Info", Info, 0, false, 0},
	{"Success", Success, StateOK, true, 0},
	{"Warn", Warn, StateAdvisory, true, 0},
	{"Detail", Detail, 0, false, 2},
}

// probeMessageRouting is §4.7: every one of the four writes to stderr and
// leaves stdout untouched, carrying the mark §4.5 fixes for its state.
//
// The expectations come from fxMark (Task 2's copy of §4.5's table), not from
// stateMark: an assertion that read the marks back out of the package would be
// satisfied by any self-consistent renaming.
//
// The fixture hands each helper a run of spaces and a tab, and routingWant
// spells out what must come back: Wrap re-joins every paragraph on single
// spaces, so the helpers deliver "routing probe for Info" however the input was
// spaced. That collapse is the layer's decided behaviour, not an accident of
// this fixture, so it is asserted here rather than left to be discovered in a
// terminal; Wrap's own doc comment in text.go states the rule.
//
// Each clause was planted in this tree and observed red, rather than claimed:
//
//	Warn pointed at Out()              -> 4 violations, "wrote 27 bytes to stdout"
//	emit resolving one stream for both -> 17 violations, every helper on stdout
//	Warn made a no-op                  -> 3 violations, "did not write its message"
//	Info wired to shapeSuccess         -> 1 violation, the markless clause below
//	a message short enough to fit      -> 6 violations: the text clause for all
//	passed through unwrapped              four helpers, and the markless clause
//	                                      for Info and Detail
//
// The fourth is what the markless clause exists for, and it was added after the
// rest: before it, that mis-wiring left the WHOLE REPOSITORY green. The fifth is
// the whitespace rule: the plant keeps Wrap in the file and merely stops routing
// a fitting message through it, which is the shape a regression here would take.

// routingRaw is what each helper is handed; routingWant is what it must put on
// the wire. They differ, and the difference is the point: the raw form carries
// a two-space run and a tab, and the collapsed form is written out as a literal
// rather than recomputed from the raw one, so the expectation does not move
// when the code that produces it moves.
const (
	routingRaw  = "routing  probe\tfor "
	routingWant = "routing probe for "
)

var probeMessageRouting = contractProbe{
	name: "message_routing",
	spec: "§4.7",
	what: "Info, Success, Warn and Detail each write to stderr and leave stdout untouched",
	run: func(t *testing.T, r *results) string {
		var seen []string
		for _, h := range messageHelpers {
			raw := routingRaw + h.name
			want := routingWant + h.name
			stdout, stderr := capture(t, func() { h.call(raw) })
			seen = append(seen, fmt.Sprintf("%s:out=%d,err=%d", h.name, len(stdout), len(stderr)))

			if stdout != "" {
				r.fail("message_routing", "%s wrote %d bytes to stdout (%q); §4.7 gives stdout to the answer alone",
					h.name, len(stdout), stdout)
			}
			plain := ansi.Strip(stderr)
			if !strings.Contains(plain, want) {
				r.fail("message_routing",
					"%s wrote %q to stderr; it was handed %q, and a run of whitespace inside a paragraph collapses to one space, so this had to carry %q",
					h.name, stderr, raw, want)
			}
			if !strings.HasSuffix(stderr, "\n") {
				r.fail("message_routing", "%s did not terminate its line: %q", h.name, stderr)
			}
			if h.hasMark {
				utf8Mark := fxMark(h.mark, GlyphUTF8) + " "
				asciiMark := fxMark(h.mark, GlyphASCII) + " "
				if !strings.HasPrefix(plain, utf8Mark) && !strings.HasPrefix(plain, asciiMark) {
					r.fail("message_routing", "%s wrote %q, which starts with neither %q nor its ASCII counterpart %q",
						h.name, plain, utf8Mark, asciiMark)
				}
			}
			if h.indent > 0 && !strings.HasPrefix(plain, strings.Repeat(" ", h.indent)) {
				r.fail("message_routing", "%s wrote %q, which is not indented by %d", h.name, plain, h.indent)
			}
			if !h.hasMark {
				// A helper with no state emits no mark, and opens with
				// exactly its declared indent.
				//
				// Without this clause INFO has nothing tying it to its
				// shape. The mark clause is skipped for both markless
				// helpers, and the indent clause is skipped for Info alone,
				// because its declared indent is 0. Detail was never in that
				// hole: renderMessage sets prefix to the mark or to the
				// indent and never to both, so re-pointing Detail at any
				// other shape changes what its line opens with — measured,
				// Detail wired to shapeInfo gives 2 violations and the
				// INDENT clause is the one that reports first.
				//
				// Measured before this clause existed — Info wired to
				// shapeSuccess left the WHOLE REPOSITORY green, under both
				// gate legs.
				wantOpen := strings.Repeat(" ", h.indent) + want
				if !strings.HasPrefix(plain, wantOpen) {
					r.fail("message_routing",
						"%s wrote %q; a helper with no state emits no mark and opens with its declared indent of %d, so this should have begun %q",
						h.name, plain, h.indent, wantOpen)
				}
			}
		}
		return strings.Join(seen, " ")
	},
}

// TestFailReturnsAndRendersTheFailShape pins what Fail writes: exactly the
// fail shape on stderr, and nothing on stdout. Planted and observed red: Fail
// pointed at Out() fails the stdout and stderr clauses together.
//
// The other half of Fail's contract — that it RETURNS, because the exit
// belongs to its caller — cannot be asserted from inside this process: an
// exiting Fail takes the test binary with it, and the run reports
// "exit status 1" naming no test. That is the red here. probeFail drives
// Fail in a child process and names this clause when run on its own (go test
// -run TestMessageContract ./internal/output); in a whole-package run this
// in-process test runs first, so an exiting Fail kills the binary here too,
// naming no test.
func TestFailReturnsAndRendersTheFailShape(t *testing.T) {
	const msg = "workspace \"a\" could not be created: Cannot connect to the Docker daemon at " +
		"unix:///var/run/docker.sock. Is the docker daemon running?"
	var want string
	stdout, stderr := capture(t, func() {
		want = renderMessage(Err(), shapeFail, msg) + "\n"
		Fail(msg)
	})
	if stdout != "" {
		t.Errorf("Fail wrote %d bytes to stdout: %q", len(stdout), stdout)
	}
	if stderr != want {
		t.Errorf("Fail wrote %q to stderr; the fail shape renders %q", stderr, want)
	}
}

// --------------------------------------------------------------- §4.8 Fail probe

// TestFailReturnsAndRendersTheFailShape holds Fail's contract in-process, but
// one half of it only by dying: a Fail that exits takes the test binary with
// it, which fails the run and names nothing. probeFail runs Fail in a process
// of its own, where ending the process is an observable exit code, and where a
// mutation switch the parent sets can be carried across — but that naming
// only happens when probeFail runs on its own; in a whole-package run this
// test runs first, and an exiting Fail kills the binary before probeFail gets
// a turn.
//
// It matters because sweeping the shared body satisfies §6.1's letter and not
// its purpose: measured, with the fail shape alone made to print one unwrapped
// line, the entire render-side suite stays green.
const (
	failChildEnv = "WS_TEST_FAIL"
	// failMutantEnv carries a mutation switch ACROSS THE PROCESS BOUNDARY.
	// TestContractMutationHarness in mutation_test.go makes Fail stop wrapping
	// and then asks whether this probe noticed; it sets mutants.FailUnwrapped
	// in the parent, but the child is a fresh process whose switches are all
	// at their zero value. With no environment variable to carry the decision,
	// the mutant would change nothing in the process actually being measured
	// and would be reported as survived. The two seams below are where the
	// parent asks and the child re-applies.
	failMutantEnv = "WS_TEST_FAIL_UNWRAPPED"
	failColumns   = 40
	// failReturnedExit is the code the child exits with once Fail has returned
	// to it. Fail never chooses an exit code — the root's error protocol does —
	// so any other code means either Fail did not return to its caller (it
	// ended the process itself) or the child never reached Fail at all: a
	// -test.run that matches nothing or a skipped child exits 0, and a panic
	// exits 2.
	failReturnedExit = 7
)

// failChildMutantEnabled reports whether the Fail mutant is on in THIS
// process, and applyFailChildMutant turns it on in the CHILD.
//
// applyFailChildMutant is the one assignment to `mutants` outside a
// `//go:build mutation` file and a named control test; mutants.go carves it
// out by name as rule 3(c). It carries no restore because there is nothing to
// restore to: it runs in the re-exec'd child of runFailChild, which exits as
// soon as Fail returns.
//
// Measured when the two seams were still inert — this file shipped before
// the switch existed — and the probe drove Die: the mutant came back
// `false — SURVIVED —`, on the digest
// "exit=1 lines=7 widest=40" — exactly what the probe observed clean — because
// the child was the only process being measured and the parent's switch never
// reached it.
func failChildMutantEnabled() bool { return mutants.FailUnwrapped }

func applyFailChildMutant() { mutants.FailUnwrapped = true }

// failMessage is 195 display cells, and carries an embedded newline so the
// paragraph handling is exercised too. Measured:
//
//	ansi.StringWidth(failMessage) = 195
//	its two paragraphs            = 166 and 29 cells
//
// Before phase 0, one Fprintln of errorStyle.Render("✗ "+failMessage) emitted
// two lines of 168 cells — not 168 and 29, which is what predicting it from
// the paragraph widths gives. lipgloss block-renders a multi-line string,
// padding every line to the widest, so the second line carried its 29 cells of
// text and 139 trailing spaces.
//
// BOTH lines therefore tripped the width loop below, and together with the
// len(lines) < 3 clause that is the three violations this probe reported
// against the pre-phase-0 body.
var failMessage = "workspace \"" + strings.Repeat("a", 64) + "\" could not be created: " +
	"Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n" +
	"Is the docker daemon running?"

// TestFailChildProcess is the far side of the subprocess: it runs only when
// the parent re-execs the test binary with the environment below.
func TestFailChildProcess(t *testing.T) {
	if os.Getenv(failChildEnv) != "1" {
		t.Skip("child half of probeFail; runs only in the subprocess")
	}
	// The child-side re-apply. mutants.FailUnwrapped is a package global of a
	// FRESH process here, at its zero value however the parent was set, so
	// fail_stops_wrapping reaches the code being measured only through this
	// line. Without it the mutant is reported as survived.
	if os.Getenv(failMutantEnv) == "1" {
		applyFailChildMutant()
	}
	Fail(failMessage)
	// Fail returned, as it must. Exit with a code Fail never produces, so the
	// parent can tell a return from an exit.
	os.Exit(failReturnedExit)
}

func runFailChild(t *testing.T) (exitCode int, stdout, stderr string) {
	t.Helper()
	// No -test.v: the framework's own "=== RUN" line goes to stdout, and this
	// probe asserts that Fail puts NOTHING there.
	cmd := exec.Command(os.Args[0], "-test.run=TestFailChildProcess")
	cmd.Env = append(os.Environ(),
		failChildEnv+"=1",
		"COLUMNS="+strconv.Itoa(failColumns),
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"NO_COLOR=1",
		"WS_ASCII=",
		"RUNEWIDTH_EASTASIAN=",
	)
	// The parent-side propagation: the parent's mutants.FailUnwrapped decides
	// whether the child is told to set its own, and the environment is the
	// only channel across the process boundary.
	if failChildMutantEnabled() {
		cmd.Env = append(cmd.Env, failMutantEnv+"=1")
	}
	var out, errB strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errB
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the Fail child: %v\n%s", err, errB.String())
	}
	return code, out.String(), errB.String()
}

// failMessageLines is the part of the child's stderr that Fail wrote:
// everything before the test framework's own chatter.
func failMessageLines(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "===") ||
			strings.HasPrefix(line, "PASS") || strings.HasPrefix(line, "FAIL") ||
			strings.HasPrefix(line, "ok ") || strings.HasPrefix(line, "exit status") {
			break
		}
		if line == "" {
			continue
		}
		out = append(out, ansi.Strip(line))
	}
	return out
}

func widestLine(lines []string) int {
	w := 0
	for _, l := range lines {
		if n := ansi.StringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// probeFail is §6.1's corpus entry for Fail, taken to the exported function.
//
// The §6.1 sweep renders the fail SHAPE through renderMessage and never calls
// Fail, so a change made inside Fail is invisible to it. That was measured
// when the probe drove Die, before phase 1: each defect below was planted in
// Die's own body, and both TestAcceptanceSweep and the whole untagged package
// were run against it.
//
//	the wrap dropped             sweep ok, TestMessageContract/die_contract red
//	emit pointed at Out()        sweep ok, TestMessageContract/die_contract red
//	os.Exit(0) for os.Exit(1)    sweep ok, TestMessageContract/die_contract red
//
// In all three the only red anywhere in the package was that one subtest —
// this probe, then named die_contract.
//
// All seven clauses below were planted and observed red, rather than claimed.
// Five plants cover the seven — four in Fail itself, the last in renderMessage
// — and two of them trip two clauses at once.
//
//	Fail printing one unwrapped line   -> 2 violations, line 1 at 168 cells;
//	                                      digest exit=7 lines=2 widest=168
//	Fail's shape stripped of its state -> 1 violation, first line lacks "✗ "
//	Fail pointed at Out()              -> 2 violations: 214 bytes on stdout, and
//	                                      nothing left on stderr to measure
//	Fail calling os.Exit(1)            -> 1 violation, "the child exited 1, not 7"
//	the message cut to 150 cells       -> 1 violation, the lost-message clause
//	inside renderMessage                  alone; digest exit=7 lines=5 widest=40,
//	                                      so the mark and the budget both held
var probeFail = contractProbe{
	name: "fail_contract",
	spec: "§6.1 / §4.7 / §4.8",
	what: "Fail wraps to the stream budget, keeps its mark and its whole message, writes only to stderr, and returns to its caller",
	run: func(t *testing.T, r *results) string {
		code, stdout, stderr := runFailChild(t)
		lines := failMessageLines(stderr)
		digest := fmt.Sprintf("exit=%d lines=%d widest=%d", code, len(lines), widestLine(lines))

		if code != failReturnedExit {
			r.fail("fail_contract", "the child exited %d, not %d: Fail did not return to its caller, or the child never reached it",
				code, failReturnedExit)
		}
		if stdout != "" {
			r.fail("fail_contract", "Fail wrote %d bytes to stdout: %q", len(stdout), stdout)
		}
		if len(lines) == 0 {
			r.fail("fail_contract", "Fail wrote nothing to stderr; the child's stderr was %q", stderr)
			return digest
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w > failColumns {
				r.fail("fail_contract", "Fail line %d is %d cells against a COLUMNS budget of %d: %q",
					i+1, w, failColumns, line)
			}
		}
		if len(lines) < 3 {
			// 195 cells of message over two paragraphs cannot be laid out in
			// two lines at a budget of 40 unless something stopped wrapping.
			r.fail("fail_contract", "Fail emitted %d line(s) for a %d-cell message at a budget of %d: it is not wrapping",
				len(lines), ansi.StringWidth(failMessage), failColumns)
		}
		if want := fxMark(StateFail, GlyphUTF8) + " "; !strings.HasPrefix(lines[0], want) {
			r.fail("fail_contract", "Fail's first line %q does not start with the fail mark %q", lines[0], want)
		}
		// §4.4: wrapped, not truncated — every character survives once the
		// breaks and the hanging indent are collapsed.
		if joined := squash(strings.Join(lines, "")); !strings.Contains(joined, squash(failMessage)) {
			r.fail("fail_contract", "Fail lost part of its message; it rendered:\n%s", strings.Join(lines, "\n"))
		}
		return digest
	},
}

// messageProbes is the half of the contract registry this file owns;
// contractProbes() in stream_contract_test.go carries the other three.
// TestContractMutationHarness in mutation_test.go runs
// append(contractProbes(), messageProbes()...).
func messageProbes() []contractProbe {
	return []contractProbe{probeMessageRouting, probeFail}
}

// ------------------------------------------------------------- the tests

// TestMessageContract runs this task's probes on the shipped behaviour.
func TestMessageContract(t *testing.T) {
	for _, p := range messageProbes() {
		t.Run(p.name, func(t *testing.T) {
			r := newResults()
			digest := p.run(t, r)
			r.report(t)
			t.Logf("%s %s: %s", p.name, p.spec, p.what)
			t.Logf("observed: %s", digest)
		})
	}
}

// §4.4 / §4.3: a message is wrapped to its stream's budget with a hanging
// indent under the mark, and loses nothing on the way. On main every helper is
// a single Fprintln whatever the width, so this is the assertion that the
// phase exists to satisfy.
func TestMessageWrapsToTheStreamBudget(t *testing.T) {
	const columns = 40
	long := "workspace \"" + strings.Repeat("a", 64) + "\" could not be created: " +
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock."

	for _, h := range messageHelpers {
		t.Run(h.name, func(t *testing.T) {
			t.Setenv("COLUMNS", strconv.Itoa(columns))
			t.Setenv("NO_COLOR", "1")
			_, stderr := capture(t, func() { h.call(long) })

			lines := strings.Split(strings.TrimSuffix(ansi.Strip(stderr), "\n"), "\n")
			if len(lines) < 2 {
				t.Fatalf("%s emitted %d line(s) for a %d-cell message at a budget of %d: it is not wrapping",
					h.name, len(lines), ansi.StringWidth(long), columns)
			}
			for i, line := range lines {
				if got := ansi.StringWidth(line); got > columns {
					t.Errorf("%s line %d is %d cells against a budget of %d: %q", h.name, i+1, got, columns, line)
				}
			}
			// The hanging indent: continuation lines sit under the first
			// line's text, not under its mark, so the mark reads as a bullet
			// for the whole message rather than for its first line.
			prefixWidth := h.indent
			if h.hasMark {
				prefixWidth = ansi.StringWidth(fxMark(h.mark, GlyphUTF8)) + 1
			}
			for i, line := range lines[1:] {
				if !strings.HasPrefix(line, strings.Repeat(" ", prefixWidth)) {
					t.Errorf("%s continuation line %d is not hung under the first line's text: %q",
						h.name, i+2, line)
				}
			}
			// Wrapped, not truncated.
			if !strings.Contains(squash(strings.Join(lines, " ")), squash(long)) {
				t.Errorf("%s lost part of its message; it rendered:\n%s", h.name, strings.Join(lines, "\n"))
			}
		})
	}
}

// §4.4: message text is frequently an upstream error string, so it travels the
// same sanitising path as Problem.Cause. A captured control sequence must not
// reach the operator's terminal as an instruction.
func TestMessageSanitisesItsInput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const hostile = "build failed \x1b]0;pwned\x07\x1b[2J\x1b[31mred\x1b[0m"
	for _, h := range messageHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, stderr := capture(t, func() { h.call(hostile) })
			if strings.ContainsRune(stderr, 0x1b) {
				t.Errorf("%s let an ESC byte through: %q", h.name, stderr)
			}
			if !strings.Contains(stderr, "red") {
				t.Errorf("%s dropped the text along with the escapes: %q", h.name, stderr)
			}
		})
	}
}

// ------------------------------------------------- §4.6 colour through paint

// fxSGR encodes one declared palette value as the introducer a terminal at
// that colour level receives.
//
// It is written from the SGR rules rather than by calling lipgloss, so paint
// is compared against what the escape sequence MEANS and not against the
// library's own opinion of it. theme_test.go pins the declared values
// themselves; fxSGR is how this file pins the wiring from a declared value to
// the wire, which is the half paint owns.
func fxSGR(t *testing.T, value string, level ColourLevel) string {
	t.Helper()
	switch level {
	case ColourTrue:
		// #rrggbb -> ESC [ 38;2;R;G;B m
		if len(value) != 7 || value[0] != '#' {
			t.Fatalf("a ColourTrue palette value must be #rrggbb, got %q", value)
		}
		var rgb [3]uint64
		for i := range rgb {
			n, err := strconv.ParseUint(value[1+2*i:3+2*i], 16, 8)
			if err != nil {
				t.Fatalf("palette value %q is not hex: %v", value, err)
			}
			rgb[i] = n
		}
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
	case Colour256:
		// an index -> ESC [ 38;5;N m
		if _, err := strconv.Atoi(value); err != nil {
			t.Fatalf("a Colour256 palette value must be an index, got %q", value)
		}
		return "\x1b[38;5;" + value + "m"
	case Colour16:
		// 0-7 are the ordinary foregrounds at 30+n, 8-15 the bright ones at
		// 90+(n-8). RoleMuted declares 8, so the bright arm is reached.
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 15 {
			t.Fatalf("a Colour16 palette value must be 0-15, got %q", value)
		}
		if n < 8 {
			return fmt.Sprintf("\x1b[%dm", 30+n)
		}
		return fmt.Sprintf("\x1b[%dm", 90+n-8)
	}
	t.Fatalf("no SGR is defined for colour level %d", level)
	return ""
}

// fxReset is the sequence that closes a painted run. It is asserted rather
// than tolerated: an introducer with no reset does not end at the end of the
// string, it bleeds into whatever the terminal prints next.
const fxReset = "\x1b[0m"

// paint is the seam where a role becomes SGR, and it is the only place in the
// message path that emits any.
//
// Phase 0 shipped it with its colour arm unexercised. Every other assertion in
// the package resolves ColourNone — by NO_COLOR, by a pipe, or by a stream
// built at ColourNone — so `return text` satisfied all of them, and paint sat
// at 66.7 % statement coverage with the half that does the work unreached.
//
// The expectations are derived from colourFor's DECLARED value through fxSGR,
// not read back out of paint and not compared against "something other than
// plain text": a difference-from-plain check is satisfied by any change at
// all, including painting every role with the wrong colour.
//
// Every clause below was planted, and the counts are what the run printed
// rather than what the arithmetic suggested:
//
//	paint returns `text` unchanged         -> 18 here (6 roles x 3 levels), 29 below
//	paint applies RoleFail to every call   -> 15 here (RoleFail is correctly
//	                                          silent at all three levels), 23 below
//	the closing reset is trimmed           -> "emitted \x1b[32mtext", want …\x1b[0m
//	Colour256 returns the truecolour value -> fxSGR's shape guard, naming "#b8bb26"
//
// WHICH LAYER CATCHES WHAT, measured one plant at a time. The expectations
// here are DERIVED from colourFor, so a defect inside colourFor moves both
// sides of the comparison together and this comparison cannot see it —
// fxSGR's shape guard, listed below, is called from inside this same test and
// fails it:
//
//	paint's role selection    -> this comparison
//	colourFor, cross-level    -> fxSGR's shape guard, which refuses a value of
//	                             the wrong shape before there is anything to
//	                             compare
//	colourFor, within a level -> theme_test.go's TestPaletteDeclaresEveryLevel,
//	                             which pins the literals independently.
//	                             Measured: swapping RoleOK's and RoleWarn's 256
//	                             indices reddens that test ALONE, and leaves
//	                             this one green.
//
// For the first two plants, measured over the whole repository: these two
// tests are the only red anywhere.
//
// Why exact bytes rather than "some SGR was emitted": measured on the weak
// form over paint itself — strings.Contains(paint(RoleOK, "x"), ESC[) — which
// FAILS when the style is dropped and PASSES when every role is painted
// RoleFail. It separates an absent sequence from a present one, and not a
// right one from a wrong one.
//
// (TestNewStreamAtTakesEveryPropertyFromItsArguments is green under both
// plants, but for an unrelated reason: it renders through Style and never
// reaches paint at all. It is not evidence about weak assertions.)
func TestPaintEmitsTheDeclaredSGR(t *testing.T) {
	coloured := []Role{RoleOK, RoleWarn, RoleFail, RoleInfo, RoleMuted, RoleAccent}

	for _, level := range []ColourLevel{Colour16, Colour256, ColourTrue} {
		s := NewStreamAt(io.Discard, 80, true, level, false)
		for _, role := range coloured {
			colour, ok := colourFor(role, level)
			if !ok {
				t.Fatalf("role %d has no declared colour at level %d; the palette "+
					"is what this assertion is derived from", role, level)
			}
			value, isColour := colour.(lipgloss.Color)
			if !isColour {
				t.Fatalf("role %d at level %d declared a %T; fxSGR can only encode a lipgloss.Color", role, level, colour)
			}
			want := fxSGR(t, string(value), level) + "text" + fxReset
			if got := s.paint(role, "text"); got != want {
				t.Errorf("paint(role %d) at level %d emitted %q, want %q", role, level, got, want)
			}
		}
		// RoleDefault is the other arm at a level that HAS colour: §4.6 gives
		// it the terminal's own foreground and no SGR at all.
		if got := s.paint(RoleDefault, "text"); got != "text" {
			t.Errorf("paint(RoleDefault) at level %d emitted %q; RoleDefault emits no SGR", level, got)
		}
	}

	// The early return, which is what every other test in the package reaches.
	none := NewStreamAt(io.Discard, 80, false, ColourNone, false)
	for _, role := range append(coloured, RoleDefault) {
		if got := none.paint(role, "text"); got != "text" {
			t.Errorf("paint(role %d) on a ColourNone stream emitted %q, want plain text", role, got)
		}
	}
}

// A message carries its shape's role on EVERY line it wraps to, and closes the
// sequence on each one.
//
// The per-line part is the half a single-line case cannot see. renderMessage
// paints line by line, so an implementation that opened the sequence once and
// closed it at the end would look identical on a message that fits and would
// leave every intermediate newline inside a coloured run — which a pager, a
// `head`, or a terminal reflowing the output turns into colour bleeding down
// the screen.
//
// The expected role per shape is written out here rather than read from the
// shape: a test that took shape.role would be satisfied by any self-consistent
// re-pointing of the five shapes at one another's colours.
func TestMessageCarriesItsRoleColourOnEveryWrappedLine(t *testing.T) {
	const columns = 40
	long := "workspace \"" + strings.Repeat("a", 64) + "\" could not be created: " +
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock."

	for _, c := range []struct {
		name  string
		shape messageShape
		role  Role
	}{
		{"Info", shapeInfo, RoleInfo},
		{"Success", shapeSuccess, RoleOK},
		{"Warn", shapeWarn, RoleWarn},
		{"Detail", shapeDetail, RoleMuted},
		{"Fail", shapeFail, RoleFail},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := NewStreamAt(io.Discard, columns, true, ColourTrue, false)
			colour, ok := colourFor(c.role, ColourTrue)
			if !ok {
				t.Fatalf("role %d has no declared colour at ColourTrue", c.role)
			}
			value, isColour := colour.(lipgloss.Color)
			if !isColour {
				t.Fatalf("role %d declared a %T at ColourTrue; fxSGR can only encode a lipgloss.Color", c.role, colour)
			}
			intro := fxSGR(t, string(value), ColourTrue)

			lines := strings.Split(renderMessage(s, c.shape, long), "\n")
			if len(lines) < 2 {
				t.Fatalf("%s emitted %d line(s) at a budget of %d; this case is about wrapped output",
					c.name, len(lines), columns)
			}
			for i, line := range lines {
				if want := intro + ansi.Strip(line) + fxReset; line != want {
					t.Errorf("%s line %d is %q, want %q", c.name, i+1, line, want)
				}
			}
			// The colour is decoration: stripping it returns the same text the
			// no-colour path produces, unchanged and complete.
			if plain := squash(ansi.Strip(strings.Join(lines, ""))); !strings.Contains(plain, squash(long)) {
				t.Errorf("%s lost part of its message under colour; it rendered:\n%s", c.name, strings.Join(lines, "\n"))
			}
		})
	}
}

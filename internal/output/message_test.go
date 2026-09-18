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
// They are phase 0's entire deliverable and the dominant call volume — 137 of
// the call sites — and they were at 0.0 % coverage on main, which is why
// routing every one of them to stdout, or leaving them unwrapped, passed the
// whole suite before these assertions existed.
//
// The two assertions Task 11 plants defects against are contractProbe values
// (the type Task 4 declares), not plain tests: the mutation harness has to be
// able to ask WHICH assertion noticed, and a *testing.T reports to the
// framework rather than to the caller. TestMessageContract below is the
// ordinary entry point that runs them clean.

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
// Goes red when a helper is pointed at the wrong stream, when emit resolves
// one stream for the process and reuses it for both, or when a helper stops
// writing at all.
var probeMessageRouting = contractProbe{
	name: "message_routing",
	spec: "§4.7",
	what: "Info, Success, Warn and Detail each write to stderr and leave stdout untouched",
	run: func(t *testing.T, r *results) string {
		var seen []string
		for _, h := range messageHelpers {
			msg := "routing probe for " + h.name
			stdout, stderr := capture(t, func() { h.call(msg) })
			seen = append(seen, fmt.Sprintf("%s:out=%d,err=%d", h.name, len(stdout), len(stderr)))

			if stdout != "" {
				r.fail("message_routing", "%s wrote %d bytes to stdout (%q); §4.7 gives stdout to the answer alone",
					h.name, len(stdout), stdout)
			}
			plain := ansi.Strip(stderr)
			if !strings.Contains(plain, msg) {
				r.fail("message_routing", "%s did not write its message to stderr; stderr was %q", h.name, stderr)
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
		}
		return strings.Join(seen, " ")
	},
}

// --------------------------------------------------------------- §4.8 Die

// Die ends in os.Exit(1), so the only way to exercise the EXPORTED function —
// rather than the private body the other four share — is to run it in a
// process whose death is the expected outcome.
//
// This matters because sweeping the shared body satisfies §6.1's letter and
// not its purpose: measured, with Die alone made to print one unwrapped line,
// the entire render-side suite stays green.
const (
	dieChildEnv = "WS_TEST_DIE"
	// dieMutantEnv carries Task 11's DieUnwrapped switch ACROSS THE PROCESS
	// BOUNDARY. The harness sets mutants.DieUnwrapped in the parent; the child
	// is a fresh process whose `mutants` is at its zero value, so without this
	// the die_stops_wrapping contract mutant is vacuous and the contract
	// harness fails. See the two seams below.
	dieMutantEnv = "WS_TEST_DIE_UNWRAPPED"
	dieColumns   = 40
)

// dieChildMutantEnabled reports whether the Die mutant is on in THIS process,
// and applyDieChildMutant turns it on. Phase 0 has no mutation switches at
// all — mutants.go is Task 7 — so both are inert here and Task 11 Step 6
// replaces the two bodies with `return mutants.DieUnwrapped` and
// `mutants.DieUnwrapped = true`, in the same commit that wires the switch into
// Die itself. They are seams rather than direct reads for exactly one reason:
// this file ships three tasks before mutants.go exists.
func dieChildMutantEnabled() bool { return false }

func applyDieChildMutant() {}

// dieMessage is 195 display cells, and carries an embedded newline so the
// paragraph handling is exercised too. Measured:
//
//	ansi.StringWidth(dieMessage)                 = 195
//	its two paragraphs                           = 166 and 29 cells
//	what main emits today, one Fprintln of "✗ " + dieMessage:
//	                                   line 1 = 168 cells, line 2 = 29 cells
var dieMessage = "workspace \"" + strings.Repeat("a", 64) + "\" could not be created: " +
	"Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n" +
	"Is the docker daemon running?"

// TestDieChildProcess is the far side of the subprocess: it runs only when the
// parent re-execs the test binary with the environment below.
func TestDieChildProcess(t *testing.T) {
	if os.Getenv(dieChildEnv) != "1" {
		t.Skip("child half of probeDie; runs only in the subprocess")
	}
	// The child-side re-apply. Inert in phase 0; live from Task 11 Step 6.
	if os.Getenv(dieMutantEnv) == "1" {
		applyDieChildMutant()
	}
	Die(dieMessage)
	// Unreachable: Die exits. Reaching it is itself the finding.
	fmt.Fprintln(os.Stderr, "DIE-RETURNED")
	os.Exit(0)
}

func runDieChild(t *testing.T) (exitCode int, stdout, stderr string) {
	t.Helper()
	// No -test.v: the framework's own "=== RUN" line goes to stdout, and this
	// probe asserts that Die puts NOTHING there.
	cmd := exec.Command(os.Args[0], "-test.run=TestDieChildProcess")
	cmd.Env = append(os.Environ(),
		dieChildEnv+"=1",
		"COLUMNS="+strconv.Itoa(dieColumns),
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"NO_COLOR=1",
		"WS_ASCII=",
		"RUNEWIDTH_EASTASIAN=",
	)
	// The parent-side propagation. Inert in phase 0; live from Task 11 Step 6.
	if dieChildMutantEnabled() {
		cmd.Env = append(cmd.Env, dieMutantEnv+"=1")
	}
	var out, errB strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errB
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the Die child: %v\n%s", err, errB.String())
	}
	return code, out.String(), errB.String()
}

// dieMessageLines is the part of the child's stderr that Die wrote: everything
// before the test framework's own chatter.
func dieMessageLines(stderr string) []string {
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

// probeDie is §6.1's corpus entry for Die, taken to the exported function.
//
// The §6.1 sweep renders Die's SHAPE through renderMessage and never calls Die,
// so a change made inside Die — dropping the wrap, writing to the wrong stream,
// exiting with the wrong code — is invisible to it.
//
// Goes red when: Die stops wrapping to its stream's budget, stops carrying the
// fail mark, puts its message on stdout, or stops exiting 1.
var probeDie = contractProbe{
	name: "die_contract",
	spec: "§6.1 / §4.7 / §4.8",
	what: "Die wraps to the stream budget, keeps its mark and its whole message, writes only to stderr, and exits 1",
	run: func(t *testing.T, r *results) string {
		code, stdout, stderr := runDieChild(t)
		lines := dieMessageLines(stderr)
		digest := fmt.Sprintf("exit=%d lines=%d widest=%d", code, len(lines), widestLine(lines))

		if code != 1 {
			r.fail("die_contract", "Die exited %d; §4.8 keeps its os.Exit(1) until the last of its 53 call sites has moved", code)
		}
		if strings.Contains(stderr, "DIE-RETURNED") {
			r.fail("die_contract", "Die returned to its caller instead of exiting")
		}
		if stdout != "" {
			r.fail("die_contract", "Die wrote %d bytes to stdout: %q", len(stdout), stdout)
		}
		if len(lines) == 0 {
			r.fail("die_contract", "Die wrote nothing to stderr; the child's stderr was %q", stderr)
			return digest
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w > dieColumns {
				r.fail("die_contract", "Die line %d is %d cells against a COLUMNS budget of %d: %q",
					i+1, w, dieColumns, line)
			}
		}
		if len(lines) < 3 {
			// 195 cells of message over two paragraphs cannot be laid out in
			// two lines at a budget of 40 unless something stopped wrapping.
			r.fail("die_contract", "Die emitted %d line(s) for a %d-cell message at a budget of %d: it is not wrapping",
				len(lines), ansi.StringWidth(dieMessage), dieColumns)
		}
		if want := fxMark(StateFail, GlyphUTF8) + " "; !strings.HasPrefix(lines[0], want) {
			r.fail("die_contract", "Die's first line %q does not start with the fail mark %q", lines[0], want)
		}
		// §4.4: wrapped, not truncated — every character survives once the
		// breaks and the hanging indent are collapsed.
		if joined := squash(strings.Join(lines, "")); !strings.Contains(joined, squash(dieMessage)) {
			r.fail("die_contract", "Die lost part of its message; it rendered:\n%s", strings.Join(lines, "\n"))
		}
		return digest
	},
}

// messageProbes is the half of the contract registry this task owns. Task 4's
// contractProbes() carries the other three; Task 11 runs
// append(contractProbes(), messageProbes()...).
func messageProbes() []contractProbe {
	return []contractProbe{probeMessageRouting, probeDie}
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
// The last one reddens through fxSGR rather than through the comparison: a
// cross-LEVEL swap produces a value of the wrong SHAPE, so the encoder refuses
// it before there is anything to compare. A swap WITHIN a level is what the
// comparison itself catches.
//
// For the first two, measured over the whole repository: these two tests are
// the only red anywhere. In particular they are not caught by
// TestNewStreamAtTakesEveryPropertyFromItsArguments, which asserts that SOME
// SGR was emitted — the wrong role passes that check.
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
		{"Die", shapeFail, RoleFail},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := NewStreamAt(io.Discard, columns, true, ColourTrue, false)
			colour, ok := colourFor(c.role, ColourTrue)
			if !ok {
				t.Fatalf("role %d has no declared colour at ColourTrue", c.role)
			}
			intro := fxSGR(t, string(colour.(lipgloss.Color)), ColourTrue)

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

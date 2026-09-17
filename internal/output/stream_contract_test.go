package output

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// §4.1's per-fd stream, §4.2's width resolution and §4.6's per-fd colour
// probe, asserted as behaviour.
//
// None of this is reachable from a render test: the §6.1 sweep renders through
// NewStreamAt and never resolves a real file descriptor. With no test between
// them, probing the width of the wrong fd, regressing the COLUMNS clamp and
// resolving every stream's colour from stdout all leave the suite green.

// ------------------------------------------------------------ result sink
//
// THIS IS THE PACKAGE'S ONE `results`. Every assertion in every later test
// file reports into it — Task 7's allocator policy, Task 8's grid pairing,
// Task 9's geometry, Task 10's width sweep, Task 11's two mutation harnesses.
// Go has one package scope across every _test.go file, so a second declaration
// is a compile error rather than a duplication.
//
// Assertions report HERE rather than into *testing.T so that a mutation
// harness can run exactly the same assertion BODIES and ask which ones went
// red. A *testing.T reports to the framework, not to the caller, so a harness
// built on one has to keep a second, weaker copy of every assertion — and a
// harness that grades its own copy proves only that the copy works.

type results struct {
	names []string
	fails map[string]int
	first map[string]string
}

func newResults() *results {
	return &results{fails: map[string]int{}, first: map[string]string{}}
}

func (r *results) fail(assertion, format string, a ...any) {
	if _, seen := r.fails[assertion]; !seen {
		r.names = append(r.names, assertion)
		r.first[assertion] = fmt.Sprintf(format, a...)
	}
	r.fails[assertion]++
}

// any is consumed from Task 7 onward, by every harness that has to tell "the
// assertions stayed green" from "the assertions went red". report() below uses
// it too, and that use is not decorative: golangci-lint's `unused` counts
// _test.go files and this task's acceptance gate runs it, so a helper whose
// first external caller is three tasks away must have one here.
func (r *results) any() bool { return len(r.names) > 0 }

// redAssertions lists the assertions that went red, in a stable order.
func (r *results) redAssertions() []string {
	out := append([]string(nil), r.names...)
	sort.Strings(out)
	return out
}

// report hands the accumulated failures to the test framework. Only the FIRST
// instance of each assertion is printed in full: a sweep that goes wrong goes
// wrong at thousands of widths, and the thousandth message says nothing the
// first did not.
func (r *results) report(t *testing.T) {
	t.Helper()
	if !r.any() {
		return
	}
	for _, name := range r.redAssertions() {
		t.Errorf("%s: %d violation(s); first: %s", name, r.fails[name], r.first[name])
	}
}

// ------------------------------------------------------------ probe types

// contractProbe is an assertion bundle that needs real file descriptors and a
// real environment rather than a pass over the render corpus.
//
// It reports into the same results sink the §6.1 assertions use, and returns a
// DIGEST OF WHAT IT OBSERVED. The digest is what lets Task 11's contract
// mutation harness tell a mutant that changed behaviour from one that changed
// nothing at all — the same job the sha256 over rendered bytes does for the
// corpus harness, and for the same reason: a mutation that perturbs nothing
// cannot be killed by anything, and reporting it as killed would be the
// harness certifying coverage it does not have.
//
// A DIGEST MUST THEREFORE DEPEND ONLY ON BEHAVIOUR. Anything a digest reports
// that can move on its own — a kernel-allocated fd, a colour level that
// follows the runner's TERM or COLORTERM — turns a mutant that changed nothing
// into one that looks killed. Both exposures were found and closed here:
// resolve_width emits ownfd as a bool rather than the fd number, and
// stream_identity pins TERM, NO_COLOR, CLICOLOR and COLORTERM. Any probe added
// later owes the same check, and so does any assertion added to these two.
type contractProbe struct {
	name string
	spec string
	what string
	run  func(t *testing.T, r *results) string
}

// contractProbes is the registry Task 11 plants its contract defects against.
//
// The two MESSAGE probes are not here: probeMessageRouting and probeDie live
// in message_test.go and are registered by messageProbes(), because the
// helpers do not have their §4.7 behaviour until Task 5 lands and a probe over
// them here would be red at THIS task's own acceptance gate. Task 11 runs
// append(contractProbes(), messageProbes()...).
func contractProbes() []contractProbe {
	return []contractProbe{probeStreamIdentity, probeResolveWidth, probeGlyphMode}
}

// ------------------------------------------------------- process-wide seam

// resetStreamMemo drops the memoised Out()/Err() so the next call re-probes
// whatever os.Stdout and os.Stderr now point at. §4.1 resolves them once per
// PROCESS, which is the right lifetime for a CLI and the wrong one for a test
// binary that has to look at several configurations.
func resetStreamMemo() {
	outOnce, errOnce = sync.Once{}, sync.Once{}
	outStream, errStream = nil, nil
}

// withStdStreams points os.Stdout and os.Stderr at the given files for the
// duration of fn, with the memoisation reset on both sides so nothing leaks
// into or out of the case.
func withStdStreams(stdout, stderr *os.File, fn func()) {
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	resetStreamMemo()
	defer func() {
		os.Stdout, os.Stderr = savedOut, savedErr
		resetStreamMemo()
	}()
	fn()
}

// ------------------------------------------------------ §4.2 ResolveWidth

// probeResolveWidth is §4.2's resolution order for one fd, including accepted
// review finding #13: a COLUMNS below MinWidth is CLAMPED, never rejected.
//
// Measured on the pre-adjudication behaviour (the COLUMNS value rejected and
// the resolution falling through to the probe), with the terminal probe
// failing as it does in a pipe:
//
//	COLUMNS=1   clamped = 29   rejected = WidthUnbounded
//	COLUMNS=10  clamped = 29   rejected = WidthUnbounded
//	COLUMNS=28  clamped = 29   rejected = WidthUnbounded
//	COLUMNS=29  clamped = 29   rejected = 29
//
// A table in a pipe therefore rendered UNBOUNDED at COLUMNS=28 and at its
// floor at COLUMNS=29 — a discontinuity in the wrong direction, which is why
// the clamp is the resolution and not a nicety.
//
// The `calls` and `sawFd` counters carry the half of the contract no returned
// number can show: that COLUMNS answers without the probe running at all, and
// that the probe is handed THIS stream's fd. Probing fd 0 (stdin) is one of
// the measured defects §1.1 records, and it resolves to a perfectly plausible
// width.
//
// Goes red when: the clamp is removed, when COLUMNS stops taking precedence
// over the probe, when a garbage or non-positive COLUMNS is honoured instead
// of ignored, when a failed probe stops resolving to WidthUnbounded, or when
// the probe is pointed at another fd.
var probeResolveWidth = contractProbe{
	name: "resolve_width",
	spec: "§4.2",
	what: "COLUMNS (clamped, never rejected) then the probe on this stream's own fd then WidthUnbounded",
	run: func(t *testing.T, r *results) string {
		env := func(columns string) func(string) string {
			return func(key string) string {
				if key == "COLUMNS" {
					return columns
				}
				return ""
			}
		}
		const probedFd = uintptr(7)

		cases := []struct {
			name    string
			columns string
			probeW  int
			probeE  error
			want    int
			wantRun bool // was the terminal probe allowed to run at all?
		}{
			{"columns positive", "120", 100, nil, 120, false},
			{"columns below MinWidth is clamped, not rejected", "10", 100, nil, MinWidth, false},
			{"columns exactly MinWidth", "29", 100, nil, MinWidth, false},
			{"columns one below MinWidth", "28", 100, nil, MinWidth, false},
			{"columns one above MinWidth", "30", 100, nil, 30, false},
			{"columns zero falls through", "0", 100, nil, 100, true},
			{"columns negative falls through", "-5", 100, nil, 100, true},
			{"columns garbage falls through", "eighty", 100, nil, 100, true},
			{"columns empty falls through", "", 100, nil, 100, true},
			{"columns whitespace falls through", "   ", 100, nil, 100, true},
			{"columns padded is honoured", " 90 ", 100, nil, 90, false},
			{"probe below MinWidth is clamped", "", 12, nil, MinWidth, true},
			{"probe zero falls through to unbounded", "", 0, nil, WidthUnbounded, true},
			{"probe failure falls through to unbounded", "", 100, io.ErrUnexpectedEOF, WidthUnbounded, true},
		}
		var digest []string
		for _, c := range cases {
			calls := 0
			var sawFd uintptr = 1 << 40
			probe := func(fd uintptr) (int, error) {
				calls++
				sawFd = fd
				return c.probeW, c.probeE
			}
			got := ResolveWidth(probedFd, env(c.columns), probe)
			digest = append(digest, fmt.Sprintf("%s=%d/%d", c.name, got, calls))

			if got != c.want {
				r.fail("resolve_width", "COLUMNS=%q probe=(%d,%v): resolved %d, §4.2 resolves %d",
					c.columns, c.probeW, c.probeE, got, c.want)
			}
			if c.wantRun && calls == 0 {
				r.fail("resolve_width", "COLUMNS=%q: the terminal probe was never called", c.columns)
			}
			if !c.wantRun && calls != 0 {
				r.fail("resolve_width", "COLUMNS=%q: the terminal probe was called %d time(s) although COLUMNS answered",
					c.columns, calls)
			}
			if calls > 0 && sawFd != probedFd {
				r.fail("resolve_width", "COLUMNS=%q: the probe was handed fd %d, not the stream's own fd %d",
					c.columns, sawFd, probedFd)
			}
		}

		// The same question one level up: newStream must hand the probe the fd
		// of the file it was given, and must carry the resolved number onto the
		// Stream. A real file is used rather than a mock writer because f.Fd()
		// is the whole point of the assertion.
		f, err := os.CreateTemp(t.TempDir(), "fd")
		if err != nil {
			r.fail("resolve_width", "temp file: %v", err)
			return strings.Join(digest, " ")
		}
		defer func() { _ = f.Close() }()
		var sawFd uintptr = 1 << 40
		s := newStream(f, func(string) string { return "" }, func(fd uintptr) (int, error) {
			sawFd = fd
			return 77, nil
		})
		// ownfd, not the number: the fd is kernel-allocated and moves between
		// runs (6 and 7 were both observed), and a digest that moves for
		// environmental reasons reports a vacuous mutant as non-vacuous.
		digest = append(digest, fmt.Sprintf("newStream ownfd=%t width=%d tty=%t", sawFd == f.Fd(), s.Width(), s.IsTTY()))
		if sawFd != f.Fd() {
			r.fail("resolve_width", "newStream probed fd %d for a file whose own fd is %d", sawFd, f.Fd())
		}
		if s.Width() != 77 {
			r.fail("resolve_width", "newStream resolved width %d from a probe that returned 77", s.Width())
		}
		if s.IsTTY() {
			r.fail("resolve_width", "newStream over a regular file reports IsTTY true")
		}
		return strings.Join(digest, " ")
	},
}

// ------------------------------------------- §4.1 / §6.6 one stream per fd

// TWO PACKAGE-WIDE INVARIANTS ARE ESTABLISHED HERE. Both are environmental
// rather than behavioural, so neither is visible in a diff that breaks them.
//
// 1. NO TEST IN THIS PACKAGE MAY CALL t.Parallel().
//
// withStdStreams reassigns os.Stdout and os.Stderr, which are process-global.
// This probe runs inside it and so does TestStreamMemoisationIsRaceFree, which
// additionally fans out to 64 goroutines. Two tests running concurrently while
// one of them swaps those variables is a data race on the swap itself, and the
// tests that lose the race assert against a descriptor another test has
// already pointed elsewhere.
//
// Half of that is caught for free and half is not, which is the part worth
// knowing. Measured by planting t.Parallel() in each place:
//
//   - in THIS probe's subtest it panics immediately — "testing: test using
//     t.Setenv, t.Chdir, or cryptotest.SetGlobalRandom can not use
//     t.Parallel". That is the Go runtime's t.Setenv rule, not anything this
//     package does, and it only fires because this probe happens to call
//     t.Setenv.
//   - in TestStreamMemoisationIsRaceFree, which swaps the same globals but
//     calls no t.Setenv, it is ACCEPTED SILENTLY: the package stayed green
//     under -race. Nothing rejects it.
//
// So a future test that swaps these globals in parallel fails intermittently,
// under a different test's name, with no message naming this cause. Measured
// at this commit: t.Parallel() appears zero times in the whole repository.
//
// 2. /dev/ptmx IS A HARD REQUIREMENT OF THIS PACKAGE'S TEST SUITE.
//
// From this file on, a runner without a pty fails rather than skips: this
// probe calls r.fail and TestNoColourEmitsNoEscapes calls t.Fatalf. That is
// deliberate. Both halves of a pipe/pipe pair resolve to ColourNone whatever
// the code does, so a quiet skip would silently remove the one assertion in
// the suite that can discriminate per-fd colour probing — the defect §4.1
// exists to close — and leave the suite green while proving nothing.

// probeStreamIdentity is §6.6's layer-level rows. Out() and Err() are two
// streams over two file descriptors, each carrying the properties of its OWN
// fd, and each resolved once and memoised.
//
// The pty is what makes the colour half able to fail. Both halves of a
// pipe/pipe pair resolve to ColourNone whatever the code does, so a probe
// built on two pipes would agree with a layer that had one global colour
// level. With stdout on a pty and stderr on a pipe the two levels MUST come
// out different, and they can only do that if each was probed on its own fd —
// which is the defect §4.1 exists to close: raw SGR written into a redirected
// err.log because the palette had been chosen from stdout.
//
// Goes red when: Out() and Err() collapse onto one fd, when colour or TTY
// status is resolved from a stream other than the one being written to, when
// the memoisation stops memoising, or when a write through a memoised Stream
// stops following a later reassignment of os.Stdout / os.Stderr.
var probeStreamIdentity = contractProbe{
	name: "stream_identity",
	spec: "§4.1 / §4.7",
	what: "Out() and Err() are distinct streams over distinct fds, each probed on its own fd, each memoised",
	run: func(t *testing.T, r *results) string {
		pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
		if err != nil {
			r.fail("stream_identity", "no pty available to probe against: %v; "+
				"this assertion cannot discriminate without one and must not be skipped quietly", err)
			return "no-pty"
		}
		defer func() { _ = pty.Close() }()
		if !term.IsTerminal(pty.Fd()) {
			r.fail("stream_identity", "/dev/ptmx is not reported as a terminal; the probe cannot discriminate")
			return "no-pty"
		}
		// The whole colour environment is pinned, not just TERM. Each of these
		// collapses a pty to the no-colour profile on its own, and a collapse
		// here does not look like an environment problem — it trips "both
		// streams resolved to colour level 0", a true-looking accusation
		// against correct code. COLORTERM is pinned for the opposite reason:
		// it does not collapse anything, it RAISES the level, and an ambient
		// COLORTERM=truecolor would move this probe's digest from outlevel=2
		// to outlevel=3 without any behaviour changing.
		//
		// Measured on a live /dev/ptmx, termenv v0.16.0, as profile -> level:
		//
		//	TERM="xterm-256color"  -> 1 (ANSI256)   -> 2 (Colour256)
		//	TERM unset             -> 3 (Ascii)     -> 0 (ColourNone)
		//	TERM="dumb"            -> 3 (Ascii)     -> 0 (ColourNone)
		//	CLICOLOR="0"           -> 3 (Ascii)     -> 0 (ColourNone)
		//	COLORTERM="truecolor"  -> 0 (TrueColor) -> 3 (ColourTrue)
		t.Setenv("TERM", "xterm-256color")
		t.Setenv("NO_COLOR", "")
		t.Setenv("CLICOLOR", "")
		t.Setenv("COLORTERM", "")

		pipeR, pipeW, err := os.Pipe()
		if err != nil {
			r.fail("stream_identity", "pipe: %v", err)
			return "no-pipe"
		}
		defer func() { _ = pipeR.Close(); _ = pipeW.Close() }()

		var digest string
		withStdStreams(pty, pipeW, func() {
			out, errS := Out(), Err()
			if out == errS {
				r.fail("stream_identity", "Out() and Err() returned the same *Stream")
			}
			if !out.IsTTY() {
				r.fail("stream_identity", "Out() over a pty reports IsTTY false")
			}
			if errS.IsTTY() {
				r.fail("stream_identity", "Err() over a pipe reports IsTTY true")
			}
			if out.level == errS.level {
				r.fail("stream_identity", "both streams resolved to colour level %d although one is a pty and one a pipe; "+
					"the level is not being probed per fd", out.level)
			}
			if errS.level != ColourNone {
				r.fail("stream_identity", "Err() over a pipe resolved to colour level %d; §4.6 gives a non-TTY stream no colour",
					errS.level)
			}
			// Memoisation: the same pointer, every time.
			memoised := Out() == out && Err() == errS
			if !memoised {
				r.fail("stream_identity", "Out()/Err() returned a different *Stream on the second call; the sync.Once is not holding")
			}
			digest = fmt.Sprintf("outtty=%t outlevel=%d errtty=%t errlevel=%d memoised=%t",
				out.IsTTY(), out.level, errS.IsTTY(), errS.level, memoised)
		})

		// WHERE A WRITE LANDS, not what the writer IS.
		//
		// An earlier version of this probe asserted out.w.(*os.File).Fd() ==
		// os.Stdout.Fd(). That is the wrong assertion twice over: it passes
		// for a Stream that captured the descriptor at first call, and it
		// fails for a correct one that resolves the descriptor at write time.
		// The contract §4.7 states is about the destination of the bytes.
		//
		// The case that discriminates is a reassignment of os.Stdout /
		// os.Stderr AFTER the memoised Stream already exists, because that is
		// what a downstream package does to capture operator-facing output:
		// internal/docker/verify_fixroutes_test.go's captureStderr swaps
		// os.Stderr around the call and reads the pipe afterwards. Today's
		// helpers re-read the variable per Fprintln, so the swap works; a
		// Stream holding the original *os.File writes into the descriptor
		// nobody is reading any more and the assertion sees an empty string.
		//
		// Measured with the late binding removed: internal/docker stays GREEN
		// and so does every other package, because nothing outside this
		// package reaches Out()/Err() yet. The three assertions below are the
		// only thing in the repository that reddens, and the digest flips to
		// landed=out="",err="". The capturing suites become detectors too
		// only once the message helpers route through Err().
		dir := t.TempDir()
		mk := func(name string) *os.File {
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				r.fail("stream_identity", "create %s: %v", name, err)
				return nil
			}
			return f
		}
		earlyOut, earlyErr := mk("early-out"), mk("early-err")
		lateOut, lateErr := mk("late-out"), mk("late-err")
		landing := "unmeasured"
		if earlyOut != nil && earlyErr != nil && lateOut != nil && lateErr != nil {
			withStdStreams(earlyOut, earlyErr, func() {
				o, e := Out(), Err() // resolved and memoised against the EARLY files
				os.Stdout, os.Stderr = lateOut, lateErr
				_, _ = io.WriteString(o, "OUT")
				_, _ = io.WriteString(e, "ERR")
			})
			for _, f := range []*os.File{earlyOut, earlyErr, lateOut, lateErr} {
				_ = f.Close()
			}
			read := func(name string) string {
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					r.fail("stream_identity", "read %s: %v", name, err)
				}
				return string(b)
			}
			lateO, lateE := read("late-out"), read("late-err")
			earlyO, earlyE := read("early-out"), read("early-err")
			if lateO != "OUT" {
				r.fail("stream_identity", "a write through Out() after os.Stdout was reassigned landed %q in the new stdout, want %q", lateO, "OUT")
			}
			if lateE != "ERR" {
				r.fail("stream_identity", "a write through Err() after os.Stderr was reassigned landed %q in the new stderr, want %q; "+
					"a Stream that captured the *os.File at first call makes the swap invisible", lateE, "ERR")
			}
			if earlyO != "" || earlyE != "" {
				r.fail("stream_identity", "a write made after the swap still reached the ORIGINAL descriptors (stdout %q, stderr %q)", earlyO, earlyE)
			}
			landing = fmt.Sprintf("out=%q,err=%q", lateO, lateE)
		}
		return digest + " landed=" + landing
	},
}

// ---------------------------------------------------- §4.5 glyph selection

// probeGlyphMode is §4.5's selection rule, driven over Task 2's OWN case
// table — glyphModeCases in glyph_test.go — so the probe and
// TestGlyphModeFromEnv cannot drift apart. One table, two drivers: Task 2's
// test reports to the framework, this probe reports to a results sink so Task
// 11 can plant `IgnoreCJKTag` against it and ask what noticed.
//
// §6.4's subprocess cannot cover this. It runs under RUNEWIDTH_EASTASIAN=1,
// which the selector answers before it ever looks at the locale, so the CJK
// branch — the one that protects a ja_JP or zh_CN terminal that has NOT set
// that variable — is never reached there.
//
// Goes red when: the CJK branch is dropped, when the LC_ALL > LC_CTYPE > LANG
// precedence is reordered, when a non-UTF-8 locale stops selecting ASCII, or
// when an empty environment stops defaulting to the safe mode.
var probeGlyphMode = contractProbe{
	name: "glyph_mode_selection",
	spec: "§4.5",
	what: "WS_ASCII, RUNEWIDTH_EASTASIAN, the CJK language tags and a non-UTF-8 locale each select the ASCII glyph mode",
	run: func(_ *testing.T, r *results) string {
		var digest []string
		for _, c := range glyphModeCases {
			got := glyphModeFromEnv(glyphModeEnv(c.env))
			digest = append(digest, strconv.Itoa(int(got)))
			if got != c.want {
				r.fail("glyph_mode_selection", "%s: selected glyph mode %d, §4.5 selects %d", c.name, got, c.want)
			}
		}
		return strings.Join(digest, "")
	},
}

// ------------------------------------------------------------ the tests

// TestStreamContract runs every probe on the shipped behaviour. It is the
// ordinary entry point; Task 11 runs the same bodies with a defect planted.
func TestStreamContract(t *testing.T) {
	for _, p := range contractProbes() {
		t.Run(p.name, func(t *testing.T) {
			r := newResults()
			digest := p.run(t, r)
			r.report(t)
			t.Logf("%s %s: %s", p.name, p.spec, p.what)
			t.Logf("observed: %s", digest)
		})
	}
}

// The two width constants are pinned to their literal values, because nothing
// else in the suite does.
//
// Every other assertion is written in terms of the constants, so it moves with
// them. probeResolveWidth's table does bound MinWidth to {29, 30} — the
// "28 -> MinWidth" row forces it to at least 29 and the "30 -> 30" row forces
// it to at most 30 — but MinWidth = 30 passes the whole suite, and
// WidthUnbounded is only ever asserted against itself.
//
// 29 is a product commitment, not an implementation detail: it is the
// narrowest terminal the CLI undertakes to serve, and the constant's own
// comment defends that number against being mistaken for the natural floor of
// the widest block. A commitment that no test states can be edited by anyone
// who finds it inconvenient.
func TestWidthConstantsArePinnedToTheirValues(t *testing.T) {
	if MinWidth != 29 {
		t.Errorf("MinWidth = %d, want the committed product floor 29", MinWidth)
	}
	if WidthUnbounded != math.MaxInt32 {
		t.Errorf("WidthUnbounded = %d, want math.MaxInt32 (%d)", WidthUnbounded, math.MaxInt32)
	}
}

// Width() reports the resolved number unclamped; the MinWidth floor belongs to
// the render budget. Keeping them separate is what lets a caller see that the
// terminal really is 20 columns while every render still lays out at 29.
func TestBudgetFloorIsSeparateFromWidth(t *testing.T) {
	s := NewStreamAt(io.Discard, 20, false, ColourNone, false)
	if s.Width() != 20 {
		t.Errorf("Width() = %d, want the unclamped 20", s.Width())
	}
	if s.budget() != MinWidth {
		t.Errorf("budget() = %d for a 20-column stream, want the MinWidth floor %d", s.budget(), MinWidth)
	}
	wide := NewStreamAt(io.Discard, 120, false, ColourNone, false)
	if wide.budget() != 120 {
		t.Errorf("budget() = %d for a 120-column stream, want 120", wide.budget())
	}
}

// A stream with no colour emits no SGR for any role, and NO_COLOR alone is
// enough to produce one even on a terminal (§4.6).
//
// THE ENVIRONMENT IS PINNED AND THERE IS A CONTROL. Neither is decoration.
//
// Both assertions below expect ColourNone. Delete either guard from
// probeColour and the call falls through to termenv, which resolves even a
// pty to the no-colour profile whenever TERM is unset, "dumb", or names no
// colour capability — and .github/workflows/ci.yml sets no TERM at all, so CI
// is exactly that case. The assertions would then be satisfied by the runner
// rather than by the code.
//
// That is not a worry, it is measured. With TERM unset, against the version of
// this test that did not pin it: deleting the NO_COLOR guard, deleting the
// !tty guard, and deleting the whole clause ALL left this test green. The same
// three mutations are killed once TERM is pinned. The identical hazard is
// spelled out at probeStreamIdentity, which has always pinned TERM; this test
// did not, and inherited its result from the developer's shell.
//
// The control is what keeps the pin honest. It asserts that colour IS
// reachable on this fd in this environment before the two suppression
// assertions run, so a pin that stops working reddens the control with a
// message naming that cause, instead of quietly making the rest vacuous.
//
// Shipped behaviour was never at risk: with getenv == os.Getenv and a real
// *os.File, termenv enforces NO_COLOR and the non-TTY case independently of
// probeColour's own guards. The defect was in this assertion's power.
func TestNoColourEmitsNoEscapes(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("COLORTERM", "")

	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("no pty available: %v", err)
	}
	defer func() { _ = pty.Close() }()

	if got := probeColour(pty, true, func(string) string { return "" }); got == ColourNone {
		t.Fatalf("control: a TTY with TERM=xterm-256color and no suppressing variable resolved to " +
			"ColourNone, so this environment cannot produce colour at all and the two assertions " +
			"below would pass without the code doing anything")
	}

	if got := probeColour(pty, true, func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}); got != ColourNone {
		t.Errorf("NO_COLOR on a terminal resolved to colour level %d, want ColourNone", got)
	}
	if got := probeColour(pty, false, func(string) string { return "" }); got != ColourNone {
		t.Errorf("a non-TTY stream resolved to colour level %d, want ColourNone", got)
	}

	s := NewStreamAt(io.Discard, 80, false, ColourNone, false)
	for role := RoleDefault; role <= RoleAccent; role++ {
		if got := s.Style(role).Render("text"); got != "text" {
			t.Errorf("role %d on a ColourNone stream rendered %q; it must emit no SGR at all", role, got)
		}
	}
}

// NewStreamAt is the injected constructor the §6 sweep renders through: every
// property is taken from its arguments and nothing is re-probed from the
// process. Without it a lipgloss renderer over a non-*os.File writer resolves
// to the no-colour profile and cannot be raised, so the sweep could not run at
// any colour level above none (§4.1).
func TestNewStreamAtTakesEveryPropertyFromItsArguments(t *testing.T) {
	var buf strings.Builder
	s := NewStreamAt(&buf, 137, true, ColourTrue, true)
	if s.Width() != 137 {
		t.Errorf("Width() = %d, want 137", s.Width())
	}
	if !s.IsTTY() {
		t.Error("IsTTY() = false for a stream constructed as a TTY")
	}
	if s.Mode() != GlyphASCII {
		t.Errorf("Mode() = %d, want GlyphASCII", s.Mode())
	}
	if got := s.Style(RoleOK).Render("ok"); !strings.Contains(got, "\x1b[") {
		t.Errorf("a ColourTrue stream rendered RoleOK as %q; no SGR was emitted", got)
	}
	if got := ansi.Strip(s.Style(RoleOK).Render("ok")); got != "ok" {
		t.Errorf("the styled text is %q once SGR is stripped, want %q", got, "ok")
	}
	// Write makes the Stream an io.Writer over the same destination, which is
	// what lets a component that owns the stream for its lifetime be pointed
	// at it directly (§4.7).
	if _, err := s.Write([]byte("direct")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.HasSuffix(buf.String(), "direct") {
		t.Errorf("Write put %q on the underlying writer", buf.String())
	}
}

// §4.1's memoisation is a sync.Once and not a nil check, and this is the test
// that says why: cmd/workspace.go:242 calls output.Warn from inside the closure
// handed to output.RunWithSpinner, and huh/spinner runs that closure on its own
// goroutine while the spinner redraws. Two goroutines therefore reach Err()
// concurrently the moment a step function logs. No file that launches a
// goroutine imports internal/output today — internal/mcp is the only package
// with a `go func` and it does not — so the race is latent rather than live,
// which is exactly why it would otherwise be found late.
//
// Pointer identity is asserted here; the data race itself is reported by the
// -race detector, which is how CI runs the suite.
func TestStreamMemoisationIsRaceFree(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	const goroutines = 64
	withStdStreams(devNull, devNull, func() {
		outs := make([]*Stream, goroutines)
		errs := make([]*Stream, goroutines)
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(i int) {
				defer wg.Done()
				outs[i], errs[i] = Out(), Err()
			}(i)
		}
		wg.Wait()
		if outs[0] == nil || errs[0] == nil {
			t.Fatal("Out()/Err() returned nil")
		}
		for i := 1; i < goroutines; i++ {
			if outs[i] != outs[0] {
				t.Fatalf("Out() returned %p to goroutine %d and %p to goroutine 0", outs[i], i, outs[0])
			}
			if errs[i] != errs[0] {
				t.Fatalf("Err() returned %p to goroutine %d and %p to goroutine 0", errs[i], i, errs[0])
			}
		}
		t.Logf("%d goroutines received one *Stream each for stdout (%p) and stderr (%p)",
			goroutines, outs[0], errs[0])
	})
}

package output

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
)

// TestPromptReadyRule: a prompt may ask only when stdin and stderr are both
// terminals; otherwise the error is ErrNoTTY, and its text names the stream,
// or the two streams, that are not.
func TestPromptReadyRule(t *testing.T) {
	for _, tc := range []struct {
		stdin, stderr bool
		want          string
	}{
		{true, true, ""},
		{false, true, "stdin is not a terminal"},
		{true, false, "stderr is not a terminal"},
		{false, false, "stdin and stderr are not terminals"},
	} {
		err := promptReady(tc.stdin, tc.stderr)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("stdin=%t stderr=%t: %v; want nil", tc.stdin, tc.stderr, err)
		case tc.want != "" && (!errors.Is(err, ErrNoTTY) || err.Error() != tc.want):
			t.Errorf("stdin=%t stderr=%t: %v; want ErrNoTTY reading %q", tc.stdin, tc.stderr, err, tc.want)
		}
	}
}

// TestPromptReadyProbesStdinAndStderr: PromptReady reads its answer from the
// process's own stdin and stderr — each of the four combinations of a
// terminal (a pty's master) and the null device, with stdout left alone.
func TestPromptReadyProbesStdinAndStderr(t *testing.T) {
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("no pty to probe against: %v", err)
	}
	defer func() { _ = pty.Close() }()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = null.Close() }()

	files := map[bool]*os.File{true: pty, false: null}
	for _, tc := range []struct {
		stdin, stderr bool
		want          string
	}{
		{true, true, ""},
		{false, true, "stdin is not a terminal"},
		{true, false, "stderr is not a terminal"},
		{false, false, "stdin and stderr are not terminals"},
	} {
		var got error
		savedIn := os.Stdin
		os.Stdin = files[tc.stdin]
		withStdStreams(os.Stdout, files[tc.stderr], func() { got = PromptReady() })
		os.Stdin = savedIn
		gotText := ""
		if got != nil {
			gotText = got.Error()
		}
		if gotText != tc.want || (got != nil) != errors.Is(got, ErrNoTTY) {
			t.Errorf("stdin terminal=%t, stderr terminal=%t: PromptReady() = %v; want %q", tc.stdin, tc.stderr, got, tc.want)
		}
	}
}

// TestCancelledCarriesItsSignal: a Cancelled is found through a wrap, and
// carries the signal the process is to die of.
func TestCancelledCarriesItsSignal(t *testing.T) {
	err := error(&Cancelled{Signal: syscall.SIGTERM})
	var c *Cancelled
	if !errors.As(errors.Join(errors.New("context"), err), &c) || c.Signal != syscall.SIGTERM {
		t.Fatalf("errors.As found %v; want the Cancelled with SIGTERM", c)
	}
	if got, want := err.Error(), "prompt cancelled: terminated"; got != want {
		t.Errorf("Error() = %q; want %q", got, want)
	}
}

// ------------------------------------------------------------ the core

// coreFor is a core that is ready to ask, records from sigs — stopping
// closes it — knows no signal ignored at start, and runs forms with run.
// Its watchdog waits a minute, restores nothing, writes into nothing and
// dies of nothing.
func coreFor(sigs chan os.Signal, run func(f *huh.Form) error) *prompter {
	return &prompter{
		stream:   NewStreamAt(io.Discard, 80, true, ColourNone, false),
		ready:    func() error { return nil },
		signals:  func(...os.Signal) (<-chan os.Signal, func()) { return sigs, func() { close(sigs) } },
		ignored:  func(os.Signal) bool { return false },
		run:      run,
		saveTerm: func() func() error { return func() error { return nil } },
		die:      func(os.Signal) {},
		grace:    time.Minute,
	}
}

// answering is a runner that answers the focused field with line, through
// the field's own line reader, and completes the form.
func answering(line string) func(f *huh.Form) error {
	return func(f *huh.Form) error {
		_ = f.GetFocusedField().RunAccessible(io.Discard, strings.NewReader(line+"\n"))
		f.State = huh.StateCompleted
		return nil
	}
}

// neverRuns is a runner that fails the test if a form reaches it.
func neverRuns(t *testing.T) func(f *huh.Form) error {
	return func(*huh.Form) error {
		t.Error("the form ran")
		return nil
	}
}

func threeWorkspaces() []SelectOption {
	return []SelectOption{{Label: "api", Value: "api"}, {Label: "ops", Value: "ops"}, {Label: "web", Value: "web"}}
}

func oneFieldForm() *huh.Form {
	return huh.NewForm(huh.NewGroup(huh.NewInput().Title("Name")))
}

// cancelledBy is the signal err's Cancelled carries, nil when it carries none.
func cancelledBy(err error) os.Signal {
	var c *Cancelled
	if errors.As(err, &c) {
		return c.Signal
	}
	return nil
}

// TestCoreReturnsTheAnswer: rule 3 — a run that ends without an error and
// with the form completed is the answer, of each kind: Confirm's yes and no,
// Select's choice, RunForm's nil.
func TestCoreReturnsTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{{"y", true}, {"n", false}} {
		got, err := coreFor(make(chan os.Signal, 3), answering(tc.line)).confirm(`Delete workspace "ops"?`, "")
		if err != nil || got != tc.want {
			t.Errorf("Confirm answered %q: (%t, %v); want (%t, nil)", tc.line, got, err, tc.want)
		}
	}
	got, err := coreFor(make(chan os.Signal, 3), answering("2")).choose("Select workspace:", threeWorkspaces())
	if err != nil || got != "ops" {
		t.Errorf("Select answered 2: (%q, %v); want (\"ops\", nil)", got, err)
	}
	form, ran := oneFieldForm(), false
	err = coreFor(make(chan os.Signal, 3), func(f *huh.Form) error {
		ran = f == form
		f.State = huh.StateCompleted
		return nil
	}).ask(form)
	if err != nil || !ran {
		t.Errorf("RunForm: %v, ran its form %t; want nil after running it", err, ran)
	}
}

// TestCoreRefusesWithoutATerminal: the predicate's error is the prompt's
// error, and the form never runs — for Confirm, Select and RunForm alike.
func TestCoreRefusesWithoutATerminal(t *testing.T) {
	p := coreFor(make(chan os.Signal, 3), neverRuns(t))
	p.ready = func() error { return promptReady(false, true) }
	_, errConfirm := p.confirm("Delete?", "")
	_, errSelect := p.choose("Select workspace:", threeWorkspaces())
	errForm := p.ask(oneFieldForm())
	for name, err := range map[string]error{"Confirm": errConfirm, "Select": errSelect, "RunForm": errForm} {
		if !errors.Is(err, ErrNoTTY) || err.Error() != "stdin is not a terminal" {
			t.Errorf("%s: %v; want ErrNoTTY naming stdin", name, err)
		}
	}
}

// TestPromptWritesNothingWithoutATerminal: the exported functions, with the
// process's stdout and stderr pipes, return ErrNoTTY and write nothing to
// either — the real huh is never reached.
func TestPromptWritesNothingWithoutATerminal(t *testing.T) {
	var errs []error
	stdout, stderr := capture(t, func() {
		_, err := Confirm(`Delete workspace "ops"?`, "This cannot be undone.")
		errs = append(errs, err)
		_, err = ConfirmDestructive(false, `Delete workspace "ops"?`, "This cannot be undone.")
		errs = append(errs, err)
		_, err = Select("Select workspace:", threeWorkspaces())
		errs = append(errs, err)
		errs = append(errs, RunForm(oneFieldForm()))
	})
	for i, err := range errs {
		if !errors.Is(err, ErrNoTTY) {
			t.Errorf("prompt %d: %v; want ErrNoTTY", i, err)
		}
	}
	if stdout != "" || stderr != "" {
		t.Errorf("without a terminal a prompt wrote stdout %q and stderr %q; want nothing", stdout, stderr)
	}
}

// TestConfirmDestructiveForceAsksNothing: with force set the answer is yes,
// without probing for a terminal and without running a form; so it is
// through the exported function, whose probes fail under go test.
func TestConfirmDestructiveForceAsksNothing(t *testing.T) {
	p := coreFor(make(chan os.Signal, 3), neverRuns(t))
	p.ready = func() error {
		t.Error("the predicate was asked")
		return promptReady(false, false)
	}
	if ok, err := p.confirmDestructive(true, "Delete?", ""); !ok || err != nil {
		t.Errorf("confirmDestructive(force): (%t, %v); want (true, nil)", ok, err)
	}
	if ok, err := ConfirmDestructive(true, "Delete everything?", "This cannot be undone."); !ok || err != nil {
		t.Errorf("ConfirmDestructive(force): (%t, %v); want (true, nil)", ok, err)
	}
}

// TestCoreSignalWins: rule 1 — a signal recorded during the run is the
// outcome, on a run that reports the form completed as on one that does not.
func TestCoreSignalWins(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		for _, completed := range []bool{true, false} {
			sigs := make(chan os.Signal, 3)
			_, err := coreFor(sigs, func(f *huh.Form) error {
				sigs <- sig
				if completed {
					f.State = huh.StateCompleted
				}
				return nil
			}).confirm("Delete?", "")
			if got := cancelledBy(err); got != sig {
				t.Errorf("%v recorded, form completed %t: %v; want a Cancelled by %v", sig, completed, err, sig)
			}
		}
	}
}

// TestCoreSignalAfterTheRun: a signal delivered after the form returned and
// before the source stopped — Bubble Tea has stopped its own channel by then,
// and the recorder alone holds it — is still recorded, and still the outcome.
func TestCoreSignalAfterTheRun(t *testing.T) {
	sigs := make(chan os.Signal, 3)
	p := coreFor(sigs, func(f *huh.Form) error {
		f.State = huh.StateCompleted
		return nil
	})
	p.signals = func(...os.Signal) (<-chan os.Signal, func()) {
		return sigs, func() {
			sigs <- syscall.SIGTERM
			close(sigs)
		}
	}
	if _, err := p.confirm("Delete?", ""); cancelledBy(err) != syscall.SIGTERM {
		t.Errorf("a SIGTERM after the run: %v; want a Cancelled by SIGTERM", err)
	}
}

// TestCoreFirstSignalButHangup: the process dies of the first recorded
// signal, except that a SIGHUP decides whatever came before it.
func TestCoreFirstSignalButHangup(t *testing.T) {
	for _, tc := range []struct {
		sent []os.Signal
		want os.Signal
	}{
		{[]os.Signal{os.Interrupt, syscall.SIGTERM}, os.Interrupt},
		{[]os.Signal{syscall.SIGTERM, os.Interrupt}, syscall.SIGTERM},
		{[]os.Signal{syscall.SIGTERM, syscall.SIGHUP}, syscall.SIGHUP},
	} {
		sigs := make(chan os.Signal, 3)
		_, err := coreFor(sigs, func(*huh.Form) error {
			for _, sig := range tc.sent {
				sigs <- sig
			}
			return nil
		}).confirm("Delete?", "")
		if got := cancelledBy(err); got != tc.want {
			t.Errorf("%v recorded: %v; want a Cancelled by %v", tc.sent, err, tc.want)
		}
	}
}

// TestCoreUserAbortIsSIGINT: rule 2 — huh's ErrUserAborted, a Ctrl-C typed
// in raw mode, is a cancellation by SIGINT.
func TestCoreUserAbortIsSIGINT(t *testing.T) {
	_, err := coreFor(make(chan os.Signal, 3), func(*huh.Form) error { return huh.ErrUserAborted }).confirm("Delete?", "")
	if cancelledBy(err) != os.Interrupt {
		t.Errorf("ErrUserAborted: %v; want a Cancelled by SIGINT", err)
	}
}

// TestCoreUnansweredIsAnError: rule 4 — no error, the form not completed and
// no signal recorded: an error, neither a cancellation nor an answer.
func TestCoreUnansweredIsAnError(t *testing.T) {
	ok, err := coreFor(make(chan os.Signal, 3), func(*huh.Form) error { return nil }).confirm("Delete?", "")
	if ok || err == nil || err.Error() != "prompt ended without an answer" || cancelledBy(err) != nil {
		t.Errorf("an unanswered run: (%t, %v); want (false, prompt ended without an answer)", ok, err)
	}
}

// TestCoreFailureIsWrapped: rule 5 — any other error is a failed prompt that
// wraps it.
func TestCoreFailureIsWrapped(t *testing.T) {
	cause := errors.New("huh: could not open a new TTY")
	_, err := coreFor(make(chan os.Signal, 3), func(*huh.Form) error { return cause }).choose("Select workspace:", threeWorkspaces())
	if !errors.Is(err, cause) || err.Error() != "prompt failed: huh: could not open a new TTY" {
		t.Errorf("a failed run: %v; want prompt failed wrapping the cause", err)
	}
}

// TestCorePanicIsAFailedPrompt: rule 6 — a panic in the run is recovered as a
// failed prompt; a signal recorded before it still decides.
func TestCorePanicIsAFailedPrompt(t *testing.T) {
	_, err := coreFor(make(chan os.Signal, 3), func(*huh.Form) error {
		panic("interface conversion: tea.Model is nil, not *huh.Form")
	}).confirm("Delete?", "")
	if err == nil || err.Error() != "prompt failed: interface conversion: tea.Model is nil, not *huh.Form" {
		t.Errorf("a panicking run: %v; want prompt failed with the panic", err)
	}
	sigs := make(chan os.Signal, 3)
	_, err = coreFor(sigs, func(*huh.Form) error {
		sigs <- syscall.SIGTERM
		panic("caught panic")
	}).confirm("Delete?", "")
	if cancelledBy(err) != syscall.SIGTERM {
		t.Errorf("a SIGTERM, then a panic: %v; want a Cancelled by SIGTERM", err)
	}
}

// TestCoreRefusesAnEmptyForm: a form with no group — huh would return nil
// for it, an answer to nothing — is refused with rule 4's error before
// anything runs. (A group with no field cannot be built: huh.NewGroup
// panics on one.)
func TestCoreRefusesAnEmptyForm(t *testing.T) {
	if err := coreFor(make(chan os.Signal, 3), neverRuns(t)).ask(huh.NewForm()); err == nil || err.Error() != "prompt ended without an answer" {
		t.Errorf("a form with no group: %v; want prompt ended without an answer", err)
	}
}

// TestCoreRefusesInsideARun: inside output.Run a prompt would write through
// the runner's frame, so the core refuses and runs nothing.
func TestCoreRefusesInsideARun(t *testing.T) {
	var err error
	r := testRunner(NewStreamAt(&syncBuffer{}, 80, false, ColourNone, false), &fakeClock{}, nil)
	_ = r.run([]Task{{Title: "Deleting workspace", Run: func(*Log) error {
		_, err = coreFor(make(chan os.Signal, 3), neverRuns(t)).confirm("Delete?", "")
		return nil
	}}})
	if err == nil || err.Error() != "prompt refused: a step is running" {
		t.Errorf("a prompt inside a step: %v; want the refusal", err)
	}
	if _, err := coreFor(make(chan os.Signal, 3), answering("y")).confirm("Delete?", ""); err != nil {
		t.Errorf("a prompt after the step: %v; want the answer", err)
	}
}

// TestCoreRecordsSignals: a run records SIGINT and SIGTERM whatever the start
// snapshot says, and SIGHUP unless the snapshot says it was ignored.
func TestCoreRecordsSignals(t *testing.T) {
	for _, tc := range []struct {
		ignored bool
		want    string
	}{{false, "[interrupt terminated hangup]"}, {true, "[interrupt terminated]"}} {
		var asked []os.Signal
		p := coreFor(make(chan os.Signal, 3), answering("y"))
		p.ignored = func(os.Signal) bool { return tc.ignored }
		sigs := make(chan os.Signal, 3)
		p.signals = func(s ...os.Signal) (<-chan os.Signal, func()) {
			asked = s
			return sigs, func() { close(sigs) }
		}
		_, _ = p.confirm("Delete?", "")
		if got := fmt.Sprint(asked); got != tc.want {
			t.Errorf("everything ignored at start %t: recorded %s; want %s", tc.ignored, got, tc.want)
		}
	}
}

// TestNotifySignalsDeliversAndCloses: the production source delivers what it
// asked for and is closed once stopped. SIGUSR1, which a Go program takes no
// action on, stands for the three.
func TestNotifySignalsDeliversAndCloses(t *testing.T) {
	sigs, stop := notifySignals(syscall.SIGUSR1)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case sig := <-sigs:
		if sig != syscall.SIGUSR1 {
			t.Errorf("the source delivered %v; want SIGUSR1", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the source delivered nothing")
	}
	stop()
	select {
	case _, open := <-sigs:
		if open {
			t.Error("the source delivered a signal after it was stopped")
		}
	case <-time.After(5 * time.Second):
		t.Error("the source's channel was not closed when it was stopped")
	}
}

// ------------------------------------------------------------ the watchdog

// hungRun is a run that records its signal, says when, and then hangs until
// released — Bubble Tea's handler blocked on its send — with the core's
// death, its restore of the terminal state and its stream observed.
type hungRun struct {
	release  chan struct{}
	sent     chan time.Time
	died     chan os.Signal
	restored chan struct{}
	result   chan error
}

// startHungRun runs a Confirm on a core whose run hangs after recording
// recorded, in order, drawn on w, with the watchdog's grace set to grace.
func startHungRun(w io.Writer, grace time.Duration, recorded ...os.Signal) *hungRun {
	h := &hungRun{
		release:  make(chan struct{}),
		sent:     make(chan time.Time, 1),
		died:     make(chan os.Signal, 2),
		restored: make(chan struct{}, 2),
		result:   make(chan error, 1),
	}
	sigs := make(chan os.Signal, 3)
	p := coreFor(sigs, func(*huh.Form) error {
		for _, sig := range recorded {
			sigs <- sig
		}
		h.sent <- time.Now()
		<-h.release
		return nil
	})
	p.stream = NewStreamAt(w, 80, true, ColourNone, false)
	p.saveTerm = func() func() error {
		return func() error {
			h.restored <- struct{}{}
			return nil
		}
	}
	p.die = func(s os.Signal) { h.died <- s }
	p.grace = grace
	go func() {
		_, err := p.confirm("Delete?", "")
		h.result <- err
	}()
	return h
}

// death waits a bounded time for the run to record its signal and for the
// core's death, and returns the signal and how long after the record it came.
func (h *hungRun) death(t *testing.T) (os.Signal, time.Duration) {
	t.Helper()
	var sent time.Time
	select {
	case sent = <-h.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never recorded its signal")
	}
	select {
	case sig := <-h.died:
		return sig, time.Since(sent)
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog never called the death")
	}
	return nil, 0
}

// finish releases the hung run and returns the core's outcome.
func (h *hungRun) finish(t *testing.T) error {
	t.Helper()
	close(h.release)
	select {
	case err := <-h.result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the core did not return after the run was released")
	}
	return nil
}

// TestWatchdogEndsAHungRun: a recorded SIGINT or SIGTERM with a run that
// never returns ends, once the grace is out, in the saved terminal state
// restored, the undo sequences written to the prompt's stream, and the death
// by that signal.
func TestWatchdogEndsAHungRun(t *testing.T) {
	const grace = 200 * time.Millisecond
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		var buf syncBuffer
		h := startHungRun(&buf, grace, sig)
		got, took := h.death(t)
		if got != sig {
			t.Errorf("%v: the watchdog died of %v", sig, got)
		}
		if took < grace {
			t.Errorf("%v: the watchdog fired %v after the signal; want the grace, %v, first", sig, took, grace)
		}
		select {
		case <-h.restored:
		default:
			t.Errorf("%v: the death came before the terminal state was restored", sig)
		}
		if out := buf.String(); out != undoSequences {
			t.Errorf("%v: the prompt's stream holds %q; want the undo sequences %q", sig, out, undoSequences)
		}
		if err := h.finish(t); cancelledBy(err) != sig {
			t.Errorf("%v: the core returned %v; want a Cancelled by it", sig, err)
		}
	}
}

// TestWatchdogHangupHasNoGrace: a recorded SIGHUP fires the watchdog at once,
// whatever the grace, since nothing else would end the run on a hang-up.
func TestWatchdogHangupHasNoGrace(t *testing.T) {
	var buf syncBuffer
	h := startHungRun(&buf, time.Hour, syscall.SIGHUP)
	got, took := h.death(t)
	if got != syscall.SIGHUP {
		t.Errorf("the watchdog died of %v; want SIGHUP", got)
	}
	if took > time.Second {
		t.Errorf("the watchdog fired %v after a SIGHUP; want at once", took)
	}
	if out := buf.String(); out != undoSequences {
		t.Errorf("the prompt's stream holds %q; want the undo sequences", out)
	}
	if err := h.finish(t); cancelledBy(err) != syscall.SIGHUP {
		t.Errorf("the core returned %v; want a Cancelled by SIGHUP", err)
	}
}

// TestWatchdogHangupAfterASignal: a SIGHUP that follows a SIGTERM re-arms the
// watchdog with no grace, and the death is by SIGHUP.
func TestWatchdogHangupAfterASignal(t *testing.T) {
	h := startHungRun(io.Discard, time.Hour, syscall.SIGTERM, syscall.SIGHUP)
	got, took := h.death(t)
	if got != syscall.SIGHUP || took > time.Second {
		t.Errorf("the watchdog died of %v %v after a SIGTERM and a SIGHUP; want SIGHUP, at once", got, took)
	}
	if err := h.finish(t); cancelledBy(err) != syscall.SIGHUP {
		t.Errorf("the core returned %v; want a Cancelled by SIGHUP", err)
	}
}

// stuckWriter is a stream that never drains: a write blocks until released.
type stuckWriter struct{ release chan struct{} }

func (w stuckWriter) Write(p []byte) (int, error) {
	<-w.release
	return len(p), nil
}

// TestWatchdogBoundsTheRestore: a prompt's stream that never drains holds the
// death back by no more than the restore's bound; stdin's state is restored
// all the same, before the write.
func TestWatchdogBoundsTheRestore(t *testing.T) {
	w := stuckWriter{release: make(chan struct{})}
	defer close(w.release)
	h := startHungRun(w, time.Hour, syscall.SIGHUP)
	_, took := h.death(t)
	if took > restoreBound+500*time.Millisecond {
		t.Errorf("a stream that never drains held the death back %v; want no more than the restore's bound, %v", took, restoreBound)
	}
	select {
	case <-h.restored:
	default:
		t.Error("stdin's state was not restored when the stream did not drain")
	}
	_ = h.finish(t)
}

// TestWatchdogQuietWhenTheRunReturns: a run that returns within the grace
// never fires the watchdog — no restore, no undo, no death.
func TestWatchdogQuietWhenTheRunReturns(t *testing.T) {
	var buf syncBuffer
	sigs := make(chan os.Signal, 3)
	p := coreFor(sigs, func(*huh.Form) error {
		sigs <- syscall.SIGTERM
		return nil
	})
	p.stream = NewStreamAt(&buf, 80, true, ColourNone, false)
	died, restored := make(chan os.Signal, 1), make(chan struct{}, 1)
	p.saveTerm = func() func() error {
		return func() error {
			restored <- struct{}{}
			return nil
		}
	}
	p.die = func(s os.Signal) { died <- s }
	p.grace = 100 * time.Millisecond
	if _, err := p.confirm("Delete?", ""); cancelledBy(err) != syscall.SIGTERM {
		t.Fatalf("the core returned %v; want a Cancelled by SIGTERM", err)
	}
	select {
	case sig := <-died:
		t.Errorf("the watchdog died of %v after the run had returned", sig)
	case <-restored:
		t.Error("the watchdog restored the terminal after the run had returned")
	case <-time.After(400 * time.Millisecond):
	}
	if out := buf.String(); out != "" {
		t.Errorf("the watchdog wrote %q after the run had returned", out)
	}
}

// TestPromptCoreProductionValues: the production core's grace is the step
// runner's second, its restore is bounded to 250 ms, and its undo sequences
// are the cursor, bracketed paste, focus reporting and the three mouse modes.
func TestPromptCoreProductionValues(t *testing.T) {
	if g := newPrompter().grace; g != time.Second {
		t.Errorf("the production grace is %v; want 1s", g)
	}
	if restoreBound != 250*time.Millisecond {
		t.Errorf("the restore's bound is %v; want 250ms", restoreBound)
	}
	if want := "\x1b[?25h\x1b[?2004l\x1b[?1004l\x1b[?1002l\x1b[?1003l\x1b[?1006l"; undoSequences != want {
		t.Errorf("the undo sequences are %q; want %q", undoSequences, want)
	}
}

// TestSaveStdinStateRestores: what saveStdinState returns puts stdin's
// terminal state back as it was saved, after a change to raw mode.
func TestSaveStdinStateRestores(t *testing.T) {
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("no pty: %v", err)
	}
	defer func() { _ = pty.Close() }()
	saved := os.Stdin
	os.Stdin = pty
	defer func() { os.Stdin = saved }()

	before, err := term.GetState(pty.Fd())
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	restore := saveStdinState()
	if _, err := term.MakeRaw(pty.Fd()); err != nil {
		t.Fatalf("make raw: %v", err)
	}
	if raw, _ := term.GetState(pty.Fd()); reflect.DeepEqual(raw, before) {
		t.Fatal("raw mode changed nothing; this test cannot see a restore")
	}
	if err := restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if after, _ := term.GetState(pty.Fd()); !reflect.DeepEqual(after, before) {
		t.Error("the state after the restore is not the one saved")
	}
}

const promptChildEnv = "WS_TEST_PROMPT_CHILD"

// TestPromptChild is the far side of TestPromptReadsTheStartSnapshot: two
// prompts' recorded signals, with SIGHUP asked for and stopped in between.
func TestPromptChild(t *testing.T) {
	if os.Getenv(promptChildEnv) == "" {
		t.Skip("child half of TestPromptReadsTheStartSnapshot; runs only in the subprocess")
	}
	first := newPrompter().recordedSignals()
	_, stop := notifySignals(first...)
	stop()
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	signal.Stop(c)
	fmt.Println(first, newPrompter().recordedSignals())
	os.Exit(0)
}

// TestPromptReadsTheStartSnapshot: the production core decides SIGHUP by the
// start snapshot — in a process started with it ignored, a second prompt
// still skips it after something asked for it, when signal.Ignored would no
// longer say it was ignored.
func TestPromptReadsTheStartSnapshot(t *testing.T) {
	for _, tc := range []struct {
		ignored bool
		want    string
	}{
		{true, "[interrupt terminated] [interrupt terminated]"},
		{false, "[interrupt terminated hangup] [interrupt terminated hangup]"},
	} {
		var cmd *exec.Cmd
		if tc.ignored {
			cmd = exec.Command("sh", "-c", `trap '' HUP; exec "$0" -test.run='^TestPromptChild$'`, os.Args[0])
		} else {
			cmd = exec.Command(os.Args[0], "-test.run=^TestPromptChild$")
		}
		cmd.Env = append(os.Environ(), promptChildEnv+"=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("SIGHUP ignored %t: the child failed: %v", tc.ignored, err)
		}
		if got := strings.TrimSpace(string(out)); got != tc.want {
			t.Errorf("SIGHUP ignored %t: the child recorded %s; want %s", tc.ignored, got, tc.want)
		}
	}
}

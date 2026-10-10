package output

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
)

// §4.10 The interaction contract.
//
// A prompt asks only when stdin and stderr are both terminals. What it
// returns tells four outcomes apart, and a command turns each into its own
// exit: the operator's answer, nil — a "no" among them, which the command
// reports as Aborted with rc 0; no terminal to ask on, ErrNoTTY, which the
// command turns into a Problem naming the flag or argument that skips the
// prompt, rc 1; a cancellation, *Cancelled, which the root turns into a
// death by its signal; and a prompt that failed to run, any other error, a
// Problem with the cause, rc 1.

// ErrNoTTY: the prompt could not ask — stdin or stderr is not a terminal.
// A prompt returns it wrapped with the stream that is not one; errors.Is
// sees through the wrap.
var ErrNoTTY = errors.New("no terminal available")

// Cancelled: the prompt ended without an answer — Ctrl-C, a signal, or the
// end of input in the screen-reader mode. Signal is what the process dies of.
type Cancelled struct{ Signal os.Signal }

func (c *Cancelled) Error() string { return "prompt cancelled: " + c.Signal.String() }

// noTerminal is ErrNoTTY naming the stream, or the two streams, that are not
// terminals. Its text is the cause a Problem prints under its title.
type noTerminal struct{ streams string }

func (e *noTerminal) Error() string { return e.streams }

func (e *noTerminal) Unwrap() error { return ErrNoTTY }

// PromptReady reports whether a prompt may ask: nil when stdin and stderr are
// both terminals, otherwise ErrNoTTY naming what is not one. stdin is probed
// with the call the streams use on their own descriptors; stderr's answer is
// Err()'s. Every prompt asks it first, and a command asks it itself only to
// refuse before work that precedes its prompt.
func PromptReady() error {
	return promptReady(term.IsTerminal(os.Stdin.Fd()), Err().IsTTY())
}

func promptReady(stdin, stderr bool) error {
	switch {
	case !stdin && !stderr:
		return &noTerminal{"stdin and stderr are not terminals"}
	case !stdin:
		return &noTerminal{"stdin is not a terminal"}
	case !stderr:
		return &noTerminal{"stderr is not a terminal"}
	}
	return nil
}

// Confirm asks a yes-or-no question, No by default: the operator's answer,
// or the contract's error — ErrNoTTY, a *Cancelled, or a prompt that failed.
func Confirm(title, description string) (bool, error) {
	return newPrompter().confirm(title, description)
}

// ConfirmDestructive is Confirm behind a force flag: with force set it
// returns true without asking and without probing anything, so that --force
// works with no terminal.
func ConfirmDestructive(force bool, title, description string) (bool, error) {
	return newPrompter().confirmDestructive(force, title, description)
}

// Select asks for one of options and returns its value, or the contract's
// error. An empty option list is the caller's mistake, and an error.
func Select(title string, options []SelectOption) (string, error) {
	return newPrompter().choose(title, options)
}

// RunForm runs a form a command builds itself — the profile wizard's —
// through the same core: nil when the operator answered, otherwise the
// contract's error.
func RunForm(f *huh.Form) error { return newPrompter().ask(f) }

// errNoAnswer is a form that ended without an answer and without a signal,
// or that held no field to ask.
var errNoAnswer = errors.New("prompt ended without an answer")

// errInsideRun is a prompt asked while output.Run owns the streams: it would
// write through the runner's frame.
var errInsideRun = errors.New("prompt refused: a step is running")

// prompter is the core every prompt runs through. Its terminal probe, its
// signal source and its form runner are fields, as the step runner's signal
// source is: the exported functions use the production values, the tests
// their own, so that every rule is asserted in process, without a terminal.
type prompter struct {
	// ready is the predicate: PromptReady.
	ready func() error
	// signals starts recording sigs and returns their channel, with the
	// function that stops recording and closes it.
	signals func(sigs ...os.Signal) (<-chan os.Signal, func())
	// ignored is the start snapshot: was the process started with sig
	// ignored.
	ignored func(sig os.Signal) bool
	// run runs a form in the normal mode: huh's own Run.
	run func(f *huh.Form) error
}

func newPrompter() *prompter {
	return &prompter{
		ready:   PromptReady,
		signals: notifySignals,
		ignored: ignoredAtStart,
		run:     (*huh.Form).Run,
	}
}

func (p *prompter) confirm(title, description string) (bool, error) {
	var yes bool
	field := huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative("Yes").
		Negative("No").
		Value(&yes)
	if err := p.ask(huh.NewForm(huh.NewGroup(field)).WithShowHelp(false)); err != nil {
		return false, err
	}
	return yes, nil
}

func (p *prompter) confirmDestructive(force bool, title, description string) (bool, error) {
	if force {
		return true, nil
	}
	return p.confirm(title, description)
}

func (p *prompter) choose(title string, options []SelectOption) (string, error) {
	if len(options) == 0 {
		return "", errors.New("no items to select")
	}
	huhOpts := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		huhOpts = append(huhOpts, huh.NewOption(o.Label, o.Value))
	}
	// Value is bound after Options, so it holds the highlighted option from
	// the start; only a completed run makes it the answer.
	var selected string
	field := huh.NewSelect[string]().
		Title(title).
		Options(huhOpts...).
		Value(&selected)
	if err := p.ask(huh.NewForm(huh.NewGroup(field)).WithShowHelp(false)); err != nil {
		return "", err
	}
	return selected, nil
}

// ask is the core. It refuses, writing nothing, when the predicate does — huh
// never runs, so /dev/tty is never opened — when f holds no field to ask,
// and inside a running step; otherwise it runs f and decides how the run
// ended.
func (p *prompter) ask(f *huh.Form) error {
	if err := p.ready(); err != nil {
		return err
	}
	if !asksSomething(f) {
		return errNoAnswer
	}
	if stepRunning() {
		return errInsideRun
	}
	return p.askNormal(f)
}

// asksSomething reports whether f holds a field to ask. huh returns nil for
// a form with no group, before it looks at its mode, which would read as an
// answer. A form exports no count of its groups, only its focused field,
// which panics when there is no group; a group with no field cannot be
// built.
func asksSomething(f *huh.Form) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return f.GetFocusedField() != nil
}

// stepRunning reports whether output.Run owns the streams.
func stepRunning() bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.r != nil
}

// askNormal runs f in Bubble Tea and records SIGINT, SIGTERM and SIGHUP for
// as long as it runs. The recorder only records: Bubble Tea's own handler
// still ends the form on a SIGINT or a SIGTERM, every channel that asks for
// a signal getting its own copy. The first rule that matches decides:
//
//  1. a signal was recorded: the prompt was cancelled by it — even on a run
//     that reports the form completed, as a SIGTERM racing the operator's
//     Enter arrives as the same quit;
//  2. huh.ErrUserAborted: a Ctrl-C keystroke, which raw mode delivers as a
//     key — cancelled by SIGINT;
//  3. no error, the form completed: the answer;
//  4. no error, the form not completed: an error, not an answer;
//  5. any other error: the prompt failed;
//  6. the run panicked — Bubble Tea recovers a panic in Update or View and
//     huh panics again on the model it gets back: recovered, the prompt
//     failed.
//
// A run that panicked returned nothing, so 2 to 5 never match it.
func (p *prompter) askNormal(f *huh.Form) error {
	f.WithAccessible(false)
	sigs, stop := p.signals(p.recordedSignals()...)
	rec := record(sigs)
	panicked, err := p.runRecovered(f)
	stop()
	sig := rec.outcome()
	switch {
	case sig != nil:
		return &Cancelled{Signal: sig}
	case panicked != nil:
		return fmt.Errorf("prompt failed: %v", panicked)
	case errors.Is(err, huh.ErrUserAborted):
		return &Cancelled{Signal: os.Interrupt}
	case err != nil:
		return fmt.Errorf("prompt failed: %w", err)
	case f.State != huh.StateCompleted:
		return errNoAnswer
	}
	return nil
}

// runRecovered runs f, recovering a panic.
func (p *prompter) runRecovered(f *huh.Form) (panicked any, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicked = r
		}
	}()
	return nil, p.run(f)
}

// recordedSignals is what a run records: SIGINT and SIGTERM whatever their
// disposition at start — Bubble Tea asks for both on every run, which
// un-ignores a SIGINT the process was started with ignored, so at a prompt
// neither is ever deaf — and SIGHUP unless the start snapshot says it was
// ignored (nohup), since nothing but the recorder asks for it.
func (p *prompter) recordedSignals() []os.Signal {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !p.ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return sigs
}

// notifySignals asks os/signal for sigs on a channel with room for all three
// a run records — os/signal drops a copy that finds no room — and returns it
// with the function that stops asking and closes it, which is safe once
// signal.Stop has returned: nothing is sent on the channel after that.
func notifySignals(sigs ...os.Signal) (<-chan os.Signal, func()) {
	c := make(chan os.Signal, 3)
	signal.Notify(c, sigs...)
	return c, func() {
		signal.Stop(c)
		close(c)
	}
}

// recorder holds the signals one run recorded. One goroutine receives them
// in order until the source is stopped and its channel closed, so it takes
// every signal delivered before the stop — one that came after the form
// returned included: Bubble Tea stops its own channel before its run
// returns, and from then on the recorder alone holds a SIGINT or a SIGTERM,
// with no default action to end the process.
type recorder struct {
	done   chan struct{}
	first  os.Signal
	hangup bool
}

func record(sigs <-chan os.Signal) *recorder {
	r := &recorder{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for sig := range sigs {
			if r.first == nil {
				r.first = sig
			}
			if sig == syscall.SIGHUP {
				r.hangup = true
			}
		}
	}()
	return r
}

// outcome waits for the recording to end — the source stopped, the goroutine
// finished — and returns the signal the process is to die of: SIGHUP when
// one came, whatever came before it; otherwise the first signal; nil when
// none came.
func (r *recorder) outcome() os.Signal {
	<-r.done
	if r.hangup {
		return syscall.SIGHUP
	}
	return r.first
}

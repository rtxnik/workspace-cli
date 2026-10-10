package output

import (
	"errors"
	"os"

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

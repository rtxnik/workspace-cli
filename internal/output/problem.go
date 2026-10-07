package output

import "errors"

// How an error carries a Problem to the one place that prints errors.
//
// The root renders every error it prints as a Problem (§4.8): the Problem
// the error carries when it carries one, and otherwise a Problem whose title
// is the error's message, which Problem.Render draws byte for byte as the
// line Fail prints. A command that has more to say than a message — facts, a
// cause, the steps that recover — returns a ProblemError and prints nothing
// itself.

// problemCarrier is an error that renders as a Problem. ProblemError is one,
// and so is TaskError, the error the step runner returns for a failed task.
type problemCarrier interface {
	AsProblem() Problem
}

// ProblemError is an error that renders as P.
type ProblemError struct {
	P   Problem
	Err error // what errors.Is and errors.As see
}

// Error is Err's message, so wrapping an error in a ProblemError changes no
// message; P.Title when Err is nil.
func (e *ProblemError) Error() string {
	if e.Err == nil {
		return e.P.Title
	}
	return e.Err.Error()
}

// Unwrap is Err, so errors.Is and errors.As see through a ProblemError.
func (e *ProblemError) Unwrap() error { return e.Err }

// AsProblem is P.
func (e *ProblemError) AsProblem() Problem { return e.P }

// ProblemOf returns the Problem of the first error in err's chain that
// carries one, and false when none does. The chain is walked with errors.As,
// so an error wrapped with fmt.Errorf's %w is seen through; a wrapper without
// an Unwrap method hides everything under it.
func ProblemOf(err error) (Problem, bool) {
	var c problemCarrier
	if errors.As(err, &c) {
		return c.AsProblem(), true
	}
	return Problem{}, false
}

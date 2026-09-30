package output

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"testing"
)

// TestProblemErrorKeepsTheErrorItWraps: a ProblemError changes no message and
// hides nothing from errors.Is and errors.As.
func TestProblemErrorKeepsTheErrorItWraps(t *testing.T) {
	inner := fmt.Errorf("switch to %q failed (previous=%q): %w", "backup", "primary", fs.ErrNotExist)
	e := &ProblemError{P: Problem{Title: "Switch to \"backup\" failed"}, Err: inner}
	if e.Error() != inner.Error() {
		t.Errorf("Error() = %q; want the wrapped error's message %q", e.Error(), inner.Error())
	}
	if !errors.Is(e, fs.ErrNotExist) {
		t.Error("errors.Is does not see through a ProblemError")
	}
	var pathErr *fs.PathError
	wrappedPath := &ProblemError{P: Problem{Title: "t"}, Err: &fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}}
	if !errors.As(wrappedPath, &pathErr) || pathErr.Path != "/x" {
		t.Errorf("errors.As does not see through a ProblemError: %v", pathErr)
	}
	if got := (&ProblemError{P: Problem{Title: "only a title"}}).Error(); got != "only a title" {
		t.Errorf("a ProblemError with no Err reads %q; want its title", got)
	}
}

// TestProblemOfFindsTheFirstCarrier: ProblemOf sees through wrapping, takes
// the outermost carrier, and reports none for an error that carries none.
func TestProblemOfFindsTheFirstCarrier(t *testing.T) {
	inner := Problem{Title: "inner", Cause: "the inner cause"}
	outer := Problem{Title: "outer", Steps: []Remedy{{"Retry", "ws proxy up"}}}
	for _, c := range []struct {
		name string
		err  error
		want Problem
		ok   bool
	}{
		{"nil", nil, Problem{}, false},
		{"a plain error", errors.New("plain"), Problem{}, false},
		{"a carrier", &ProblemError{P: inner, Err: errors.New("x")}, inner, true},
		{"a carrier wrapped with %w", fmt.Errorf("context: %w", &ProblemError{P: inner}), inner, true},
		{"two carriers: the outer one", &ProblemError{P: outer, Err: &ProblemError{P: inner}}, outer, true},
		{"a carrier wrapped with %v is hidden", fmt.Errorf("context: %v", &ProblemError{P: inner}), Problem{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ProblemOf(c.err)
			if ok != c.ok || !reflect.DeepEqual(got, c.want) {
				t.Errorf("ProblemOf = (%+v, %t); want (%+v, %t)", got, ok, c.want, c.ok)
			}
		})
	}
}

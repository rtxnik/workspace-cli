package output

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
)

// SelectOption represents a selectable item with a display label and value.
type SelectOption struct {
	Label string
	Value string
}

// Select displays an interactive selector and returns the chosen value.
//
// ok is false when the selector did not produce a choice — the operator
// cancelled it, or it could not run — with no terminal to draw on (stdin
// is not a terminal and there is no controlling terminal to open), huh
// fails. A blank line has already been written to stderr and the caller
// returns without an error. An empty option list is the caller's mistake
// and comes back as one.
func Select(title string, options []SelectOption) (string, bool, error) {
	if len(options) == 0 {
		return "", false, errors.New("no items to select")
	}

	huhOpts := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		huhOpts = append(huhOpts, huh.NewOption(o.Label, o.Value))
	}

	var selected string
	err := huh.NewSelect[string]().
		Title(title).
		Options(huhOpts...).
		Value(&selected).
		Run()

	if err != nil {
		fmt.Fprintln(os.Stderr)
		return "", false, nil
	}
	return selected, true, nil
}

// StatusLabel formats a workspace status for display in the selector.
func StatusLabel(name, status string) string {
	return fmt.Sprintf("%s  %s", name, StatusText(status))
}

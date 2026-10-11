package output

import (
	"github.com/charmbracelet/huh"
)

// The five message helpers moved to message.go, where they resolve their
// stream, their colour level and their width budget per file descriptor.
// ConfirmBool below is untouched by that move.

// ConfirmBool shows an interactive confirmation dialog. Returns true only if
// the user explicitly confirms. Default is No (safe default). Its false is
// also every prompt that could not run; the name keeps Confirm free for a
// form that tells the two apart, and goes with the last caller.
func ConfirmBool(title string, description string) bool {
	var confirmed bool
	err := huh.NewConfirm().
		Title("⚠ " + title).
		Description(description).
		Affirmative("Yes").
		Negative("No").
		Value(&confirmed).
		Run()

	if err != nil {
		return false
	}
	return confirmed
}

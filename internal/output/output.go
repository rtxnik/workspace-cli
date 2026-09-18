package output

import (
	"github.com/charmbracelet/huh"
)

// The five message helpers moved to message.go, where they resolve their
// stream, their colour level and their width budget per file descriptor.
// Confirm and ConfirmDestructive below are untouched by that move.
//
// Three style aliases went with the helpers, because they had no other
// caller. The three that stay have one each, measured rather than assumed:
// spinner.go:23 and :29 render through errorStyle and successStyle, and
// SectionStyle is read from cmd (root.go x4, profile.go, vault_status.go).
// §4.6 replaces all three, in the phase that reaches those call sites.
var (
	SectionStyle = StyleHeader

	successStyle = StyleSuccess
	errorStyle   = StyleError
)

// Confirm shows an interactive confirmation dialog. Returns true only if
// the user explicitly confirms. Default is No (safe default).
func Confirm(title string, description string) bool {
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

// ConfirmDestructive gates a destructive action behind an interactive
// confirmation unless force is set. When force is true it returns true
// immediately without prompting; otherwise it defers to Confirm (default No).
// A false return means the operator declined — callers treat that as a
// successful no-op, not an error.
func ConfirmDestructive(force bool, title, description string) bool {
	if force {
		return true
	}
	return Confirm(title, description)
}

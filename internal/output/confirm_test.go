package output

import "testing"

// When force is set, the destructive action proceeds immediately without any
// interactive prompt.
func TestConfirmDestructive_ForceBypassesPrompt(t *testing.T) {
	if !ConfirmDestructive(true, "Delete everything?", "This cannot be undone.") {
		t.Fatal("ConfirmDestructive(force=true) must return true without prompting")
	}
}

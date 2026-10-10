package output

import "testing"

// When force is set, the destructive action proceeds immediately without any
// interactive prompt.
func TestConfirmDestructiveBool_ForceBypassesPrompt(t *testing.T) {
	if !ConfirmDestructiveBool(true, "Delete everything?", "This cannot be undone.") {
		t.Fatal("ConfirmDestructiveBool(force=true) must return true without prompting")
	}
}

package mcp

import "testing"

// TestL3_06_FailureEnvelopeWithEmptyCodeMapsToExitZero: a failure envelope
// ({"ok":false,"error":{"code":"","message":...}}) must NOT map to exit 0.
// An empty code on a failure path is contract drift and fails closed
// (exit 1), matching the unknown-code guard.
func TestL3_06_FailureEnvelopeWithEmptyCodeMapsToExitZero(t *testing.T) {
	env := &Envelope{OK: false, Error: &EnvelopeError{Code: "", Message: "boom"}}
	var got int
	quietStderr(t, func() { got = env.ExitCode() })
	if got != 1 {
		t.Fatalf("ok=false envelope with empty error code: ExitCode() = %d; want 1", got)
	}
}

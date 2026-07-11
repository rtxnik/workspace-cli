package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

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

// TestL3_07_HealthScoreTreatsOKFalseAsSuccess: a non-OK envelope without an
// error block from either upstream tool must surface
// ErrUpstreamHealthSignalFailed — not compose a fabricated score out of
// zeroed pillars (orphan pillar defaults to 100 → composite 20 presented
// as a real measurement).
func TestL3_07_HealthScoreTreatsOKFalseAsSuccess(t *testing.T) {
	cases := []struct {
		name      string
		responses map[string]*Envelope
	}{
		{
			name: "get_coverage_report ok=false",
			responses: map[string]*Envelope{
				"get_coverage_report": {OK: false},
			},
		},
		{
			name: "get_orphans ok=false",
			responses: map[string]*Envelope{
				"get_coverage_report": {OK: true, Data: json.RawMessage(`{"coverage_pct":90,"content_sufficiency_pct":90,"link_integrity_pct":90}`)},
				"get_orphans":         {OK: false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, err := ComputeVaultHealthScore(context.Background(), &stubHealthCaller{responses: tc.responses})
			if err == nil {
				t.Fatalf("ok=false envelope accepted; fabricated score=%d returned instead of ErrUpstreamHealthSignalFailed", score)
			}
			if !errors.Is(err, ErrUpstreamHealthSignalFailed) {
				t.Fatalf("err = %v; want errors.Is(_, ErrUpstreamHealthSignalFailed)", err)
			}
		})
	}
}

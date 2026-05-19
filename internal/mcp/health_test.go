package mcp

// health_test.go — unit tests for ComputeVaultHealthScore per CONTEXT D-21
// §OQ-1+OQ-3 Amendment. Asserts the 40/30/20/10 weight formula matches
// ADR-obs-05 byte-explicit, and that the band semantics (≥70 green,
// 50-69 yellow, <50 red) hold.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubHealthCaller satisfies the healthCaller interface used by
// ComputeVaultHealthScore. Tests inject canned envelopes per tool name so
// the composition logic is exercised without real MCP transport.
type stubHealthCaller struct {
	responses map[string]*Envelope
	errors    map[string]error
}

func (s *stubHealthCaller) Call(_ context.Context, tool string, _ any) (*Envelope, error) {
	if e, ok := s.errors[tool]; ok {
		return nil, e
	}
	if env, ok := s.responses[tool]; ok {
		return env, nil
	}
	return &Envelope{OK: true, Data: json.RawMessage(`{}`)}, nil
}

// makeCoverageEnvelope builds a get_coverage_report envelope using the
// metric fields ComputeVaultHealthScore reads (coverage_pct,
// content_sufficiency_pct, link_integrity_pct — all 0-100). ADR-obs-05
// defines these in §Component definitions; the composition consumes them
// verbatim.
func makeCoverageEnvelope(coverage, sufficiency, linkIntegrity float64) *Envelope {
	body, _ := json.Marshal(map[string]any{
		"coverage_pct":            coverage,
		"content_sufficiency_pct": sufficiency,
		"link_integrity_pct":      linkIntegrity,
	})
	return &Envelope{OK: true, Data: body}
}

// makeOrphansEnvelope builds a get_orphans envelope with `n` orphan rows.
// Composition derives orphan_score as inverse-scaled (1 - min(1, n/100)).
func makeOrphansEnvelope(n int) *Envelope {
	rows := make([]map[string]any, n)
	for i := 0; i < n; i++ {
		rows[i] = map[string]any{"id": "orphan-" + string(rune('a'+i%26)), "type": "concept", "inbound_count": 0}
	}
	body, _ := json.Marshal(rows)
	return &Envelope{OK: true, Data: body}
}

// TestComputeVaultHealthScoreFullGreen — all sub-metrics 100%, 0 orphans.
// Score MUST be exactly 100 (sanity check that no clamping or rounding
// drift occurs at the upper bound).
func TestComputeVaultHealthScoreFullGreen(t *testing.T) {
	stub := &stubHealthCaller{
		responses: map[string]*Envelope{
			"get_coverage_report": makeCoverageEnvelope(100, 100, 100),
			"get_orphans":         makeOrphansEnvelope(0),
		},
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score != 100 {
		t.Errorf("full green expected 100, got %d", score)
	}
}

// TestComputeVaultHealthScoreFullRed — all sub-metrics 0%, 1000 orphans.
// Score MUST be < 50 (red band per ADR-obs-05).
func TestComputeVaultHealthScoreFullRed(t *testing.T) {
	stub := &stubHealthCaller{
		responses: map[string]*Envelope{
			"get_coverage_report": makeCoverageEnvelope(0, 0, 0),
			"get_orphans":         makeOrphansEnvelope(1000),
		},
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score >= 50 {
		t.Errorf("full red expected score < 50, got %d", score)
	}
	if score != 0 {
		t.Logf("note: composition yields %d on fully-red input (allowed; lower-clamp would be 0)", score)
	}
}

// TestComputeVaultHealthScoreMidYellow — mid values land in the yellow band.
// Coverage 70 + sufficiency 60 + 30 orphans + link-integrity 65:
//
//	score = 40*0.70 + 30*0.60 + 20*(1 - 30/100) + 10*0.65
//	      = 28      + 18      + 14              + 6.5
//	      = 66.5 → 67 (rounded)
//
// Must fall in [50, 69] yellow band.
func TestComputeVaultHealthScoreMidYellow(t *testing.T) {
	stub := &stubHealthCaller{
		responses: map[string]*Envelope{
			"get_coverage_report": makeCoverageEnvelope(70, 60, 65),
			"get_orphans":         makeOrphansEnvelope(30),
		},
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score < 50 || score > 69 {
		t.Errorf("mid yellow expected score in [50,69]; got %d", score)
	}
}

// TestComputeVaultHealthScoreUpstreamError — upstream MCP tool returns
// envelope.Error → composition surfaces a wrapped error citing the failed
// tool by name.
func TestComputeVaultHealthScoreUpstreamError(t *testing.T) {
	stub := &stubHealthCaller{
		responses: map[string]*Envelope{
			"get_coverage_report": {
				OK:    false,
				Error: &EnvelopeError{Code: "BUDGET_EXCEEDED", Message: "monthly cost ceiling reached"},
			},
			"get_orphans": makeOrphansEnvelope(0),
		},
	}
	_, err := ComputeVaultHealthScore(context.Background(), stub)
	if err == nil {
		t.Fatal("expected error on upstream BUDGET_EXCEEDED envelope")
	}
	if !errors.Is(err, ErrUpstreamHealthSignalFailed) {
		t.Errorf("expected ErrUpstreamHealthSignalFailed; got %v", err)
	}
	if !strings.Contains(err.Error(), "get_coverage_report") {
		t.Errorf("error must cite failed upstream tool name; got %q", err.Error())
	}
}

// TestComputeVaultHealthScoreUpstreamTransportError — Call itself errors
// (subprocess crashed mid-roundtrip) → composition surfaces wrapped error.
func TestComputeVaultHealthScoreUpstreamTransportError(t *testing.T) {
	stub := &stubHealthCaller{
		responses: map[string]*Envelope{
			"get_orphans": makeOrphansEnvelope(0),
		},
		errors: map[string]error{
			"get_coverage_report": errors.New("EOF reading subprocess stdout"),
		},
	}
	_, err := ComputeVaultHealthScore(context.Background(), stub)
	if err == nil {
		t.Fatal("expected error on transport failure")
	}
	if !errors.Is(err, ErrUpstreamHealthSignalFailed) {
		t.Errorf("expected ErrUpstreamHealthSignalFailed; got %v", err)
	}
}

// TestComputeVaultHealthScoreWeights — exact-value canary for the
// 40/30/20/10 weights per ADR-obs-05. Coverage 100, sufficiency 0,
// orphans=0 (→ orphan_score 100), link_integrity 0:
//
//	expected = 40*1.0 + 30*0.0 + 20*1.0 + 10*0.0 = 60
//
// Any drift from the weight formula would fail this test on the next
// CI run (T-18-OQ1 mitigation per threat register).
func TestComputeVaultHealthScoreWeights(t *testing.T) {
	stub := &stubHealthCaller{
		responses: map[string]*Envelope{
			"get_coverage_report": makeCoverageEnvelope(100, 0, 0),
			"get_orphans":         makeOrphansEnvelope(0),
		},
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score != 60 {
		t.Fatalf("weight canary expected 60 (40*1.0 + 30*0.0 + 20*1.0 + 10*0.0); got %d — formula drift from ADR-obs-05", score)
	}
}

// TestComputeVaultHealthScoreClampUpper — over-100 inputs (defensive)
// clamp to 100.
func TestComputeVaultHealthScoreClampUpper(t *testing.T) {
	stub := &stubHealthCaller{
		responses: map[string]*Envelope{
			"get_coverage_report": makeCoverageEnvelope(120, 120, 120),
			"get_orphans":         makeOrphansEnvelope(0),
		},
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score != 100 {
		t.Errorf("clamp upper expected 100; got %d", score)
	}
}

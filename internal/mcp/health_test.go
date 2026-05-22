package mcp

// health_test.go — unit tests for ComputeVaultHealthScore per CONTEXT D-21
// §OQ-1+OQ-3 Amendment. Asserts the 40/30/20/10 weight formula matches
// ADR-obs-05 byte-explicit, and that the band semantics (≥70 green,
// 50-69 yellow, <50 red) hold.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// stubHealthCallerWithRoot extends stubHealthCaller with the optional
// repoRootProvider seam introduced in Phase 21 Plan 21-c so the sentinel-
// presence path (dr-drill-stale.warn -> -5 orphan_compliance_pct deduction)
// is exercised in tests without a real vault-ai checkout.
type stubHealthCallerWithRoot struct {
	stubHealthCaller
	root string
}

func (s *stubHealthCallerWithRoot) RepoRoot() string { return s.root }

// seedDrillStaleSentinel creates `<root>/_tooling/state/dr-drill-stale.warn`
// inside a fresh t.TempDir() and returns the directory path. Used by the
// HARD-14 sub-signal 1/3 test trio.
func seedDrillStaleSentinel(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "_tooling", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "dr-drill-stale.warn"), []byte("synthetic\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	return root
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

// Phase 21 Plan 21-c HARD-14 sub-signal 1/3 tests --------------------------

// TestComputeVaultHealthScoreWithDrillStaleSentinel — sentinel PRESENT in
// the mocked vault root; ComputeVaultHealthScore MUST deduct 5 percentage
// points from orphan_compliance_pct per ADR-obs-05 v2.2 Amendment Delta 2.
// Fixture: orphan_compliance_pct starts at 100 (0 orphans -> orphan_score 1.0
// -> orphan_pct 100); sentinel deducts to 95 (orphan_pct 95 -> orphan_score 0.95).
//
//	without sentinel: 40*1.0 + 30*0.0 + 20*1.00 + 10*0.0 = 60
//	with    sentinel: 40*1.0 + 30*0.0 + 20*0.95 + 10*0.0 = 59
func TestComputeVaultHealthScoreWithDrillStaleSentinel(t *testing.T) {
	root := seedDrillStaleSentinel(t)
	stub := &stubHealthCallerWithRoot{
		stubHealthCaller: stubHealthCaller{
			responses: map[string]*Envelope{
				"get_coverage_report": makeCoverageEnvelope(100, 0, 0),
				"get_orphans":         makeOrphansEnvelope(0),
			},
		},
		root: root,
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score != 59 {
		t.Errorf("sentinel-present expected 59 (60 - 5pt orphan deduction at weight 0.20); got %d", score)
	}
}

// TestComputeVaultHealthScoreWithoutDrillStaleSentinel — sentinel ABSENT.
// Same fixture as above WITHOUT the sentinel file. Score MUST be the
// pre-deduction 60 (matches the existing TestComputeVaultHealthScoreWeights).
func TestComputeVaultHealthScoreWithoutDrillStaleSentinel(t *testing.T) {
	root := t.TempDir() // sentinel file deliberately NOT created
	stub := &stubHealthCallerWithRoot{
		stubHealthCaller: stubHealthCaller{
			responses: map[string]*Envelope{
				"get_coverage_report": makeCoverageEnvelope(100, 0, 0),
				"get_orphans":         makeOrphansEnvelope(0),
			},
		},
		root: root,
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score != 60 {
		t.Errorf("no-sentinel expected 60 (pre-deduction baseline); got %d", score)
	}
}

// TestVaultHealthScoreNeverNegativeUnderDrillStaleSentinel — tripwire test.
// Even when orphan_compliance_pct is already 0 (1000 orphans -> orphan_score 0),
// the -5 deduction must NOT push the orphan contribution below 0 (math.Max
// guard) and the composite score must NOT go negative. Per HARD-14 invariant.
func TestVaultHealthScoreNeverNegativeUnderDrillStaleSentinel(t *testing.T) {
	root := seedDrillStaleSentinel(t)
	stub := &stubHealthCallerWithRoot{
		stubHealthCaller: stubHealthCaller{
			responses: map[string]*Envelope{
				// Drive every sub-metric to 0 + 1000 orphans (orphan_score = 0).
				"get_coverage_report": makeCoverageEnvelope(0, 0, 0),
				"get_orphans":         makeOrphansEnvelope(1000),
			},
		},
		root: root,
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score < 0 {
		t.Fatalf("score MUST never be negative under sentinel deduction; got %d", score)
	}
	if score != 0 {
		// The composite formula clamps at 0 for the lower bound; we expect
		// 0 here because every sub-metric pre-deduction is 0.
		t.Errorf("expected 0 (lower clamp); got %d", score)
	}
}

// Phase 21 Plan 21-d HARD-14 sub-signals 2/3 + 3/3 tests --------------------

// seedSentinels creates an empty vault root and touches each named
// sentinel under <root>/_tooling/state/. Helper for the 8-permutation
// TestComputeVaultHealthScoreWithSentinels matrix.
func seedSentinels(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "_tooling", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte("synthetic\n"), 0o644); err != nil {
			t.Fatalf("write sentinel %s: %v", name, err)
		}
	}
	return root
}

// TestComputeVaultHealthScoreWithSentinels — 2^3 = 8 permutation matrix
// covering every combination of the 3 fold-as-anti-signal sentinels
// per ADR-obs-05 §v2.2 Amendment Delta 2. Each permutation seeds the
// vault root with the listed sentinels and asserts the composite
// score matches the formula:
//
//	coverage_pct      = 100 - (3 if cost-reconcile-drift else 0)
//	sufficiency_pct   = 100 - (5 if triage-drift           else 0)
//	orphan_compliance = 100 - (5 if dr-drill-stale         else 0)
//	link_integrity    = 100  (unaffected)
//	composite         = 40*cov/100 + 30*suf/100 + 20*orph/100 + 10*1.0
//
// Fixture: all sub-metrics 100, 0 orphans. Worst case (all 3
// sentinels present): 40*0.97 + 30*0.95 + 20*0.95 + 10*1.0 = 96.3 -> 96.
func TestComputeVaultHealthScoreWithSentinels(t *testing.T) {
	type tc struct {
		name      string
		sentinels []string
		expected  int
	}
	// 2^3 = 8 cases enumerated explicitly so each row's expected score
	// is grep-addressable and self-documenting. Note Go's `math.Round`
	// rounds half-AWAY-from-zero (not banker's rounding), so 98.5 -> 99
	// (e.g., triage-only and cost-only land at 99 not 98).
	// Per-pillar deduction maths:
	//   drill-only:    40*1.00 + 30*1.00 + 20*0.95 + 10*1.00 = 99.0  -> 99
	//   cost-only:     40*0.97 + 30*1.00 + 20*1.00 + 10*1.00 = 98.8  -> 99
	//   triage-only:   40*1.00 + 30*0.95 + 20*1.00 + 10*1.00 = 98.5  -> 99
	//   drill+cost:    40*0.97 + 30*1.00 + 20*0.95 + 10*1.00 = 97.8  -> 98
	//   drill+triage:  40*1.00 + 30*0.95 + 20*0.95 + 10*1.00 = 97.5  -> 98
	//   cost+triage:   40*0.97 + 30*0.95 + 20*1.00 + 10*1.00 = 97.3  -> 97
	//   all-three:     40*0.97 + 30*0.95 + 20*0.95 + 10*1.00 = 96.3  -> 96
	cases := []tc{
		{"none", []string{}, 100},
		{"drill-only", []string{"dr-drill-stale.warn"}, 99},
		{"cost-only", []string{"cost-reconcile-drift.warn"}, 99},
		{"triage-only", []string{"triage-drift.warn"}, 99},
		{"drill+cost", []string{"dr-drill-stale.warn", "cost-reconcile-drift.warn"}, 98},
		{"drill+triage", []string{"dr-drill-stale.warn", "triage-drift.warn"}, 98},
		{"cost+triage", []string{"cost-reconcile-drift.warn", "triage-drift.warn"}, 97},
		{"all-three", []string{"dr-drill-stale.warn", "cost-reconcile-drift.warn", "triage-drift.warn"}, 96},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			root := seedSentinels(t, c.sentinels...)
			stub := &stubHealthCallerWithRoot{
				stubHealthCaller: stubHealthCaller{
					responses: map[string]*Envelope{
						"get_coverage_report": makeCoverageEnvelope(100, 100, 100),
						"get_orphans":         makeOrphansEnvelope(0),
					},
				},
				root: root,
			}
			score, err := ComputeVaultHealthScore(context.Background(), stub)
			if err != nil {
				t.Fatalf("ComputeVaultHealthScore: %v", err)
			}
			if score != c.expected {
				t.Errorf("permutation %q expected %d; got %d", c.name, c.expected, score)
			}
		})
	}
}

// TestVaultHealthScoreNeverNegativeUnderAllSentinels — HARD-14 tripwire.
// Drives every sub-metric to 0 + 1000 orphans + ALL 3 sentinels present.
// Each pillar's max(0, raw - deduction) floor guarantees no pillar goes
// negative; the composite is then clamped at 0 by the outer score-clamp.
// If this tripwire ever fires, the fold-as-anti-signal floor logic has
// regressed.
func TestVaultHealthScoreNeverNegativeUnderAllSentinels(t *testing.T) {
	root := seedSentinels(t,
		"dr-drill-stale.warn",
		"cost-reconcile-drift.warn",
		"triage-drift.warn",
	)
	stub := &stubHealthCallerWithRoot{
		stubHealthCaller: stubHealthCaller{
			responses: map[string]*Envelope{
				"get_coverage_report": makeCoverageEnvelope(0, 0, 0),
				"get_orphans":         makeOrphansEnvelope(1000),
			},
		},
		root: root,
	}
	score, err := ComputeVaultHealthScore(context.Background(), stub)
	if err != nil {
		t.Fatalf("ComputeVaultHealthScore: %v", err)
	}
	if score < 0 {
		t.Fatalf("score MUST never be negative under all 3 sentinels; got %d", score)
	}
	if score != 0 {
		t.Errorf("expected 0 (lower clamp; every pre-deduction sub-metric is 0); got %d", score)
	}
}


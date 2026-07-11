package mcp

// health.go — `vault_health` composition path per CONTEXT D-21 §OQ-1+OQ-3
// Amendment (Wave 0 reconciliation). The `vault_health` MCP tool is absent
// from tools.json v1.5.0; this file implements the Go-side surrogate by
// composing the canonical ADR-obs-05 score from existing MCP tools
// (`get_coverage_report` + `get_orphans`).
//
// ADR-obs-05 formula (byte-explicit from the ADR §Formula block):
//
//   vault_health_score = 0.40 × coverage_pct
//                      + 0.30 × content_sufficiency_pct
//                      + 0.20 × orphan_compliance_pct
//                      + 0.10 × link_integrity_pct
//
// Component sources (CONTEXT D-21 §OQ-1+OQ-3 Amendment):
//   - coverage_pct:           get_coverage_report.data.coverage_pct
//   - content_sufficiency_pct: get_coverage_report.data.content_sufficiency_pct
//   - link_integrity_pct:     get_coverage_report.data.link_integrity_pct
//   - orphan_compliance_pct:  derived from get_orphans row count via
//                             inverse-scaled formula 100 × (1 - min(1, n/100))
//                             (graceful: 0 orphans = 100; 100+ orphans = 0;
//                             100 picked as the operator's "intolerable"
//                             threshold per Plan 18-03 summary — documented
//                             deviation from ADR-obs-05 which defers to the
//                             per-type matrix from ADR-obs-01)
//
// Band semantics per ADR-obs-05 + CONTEXT D-28:
//   ≥70 → green (v2.1 ship-target; exit 0)
//   50-69 → yellow (v2.2 advisory; exit 1)
//   <50 → red (exit 2)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// ErrUpstreamHealthSignalFailed is the sentinel returned when any upstream
// MCP tool roundtrip used by ComputeVaultHealthScore fails (either transport
// error or non-OK envelope). Callers MUST wrap this with errors.Is so the
// `ws vault status` signal-2 collector can surface "red" with the tool name.
var ErrUpstreamHealthSignalFailed = errors.New("upstream MCP health signal failed")

// healthCaller is the subset of *Client used by ComputeVaultHealthScore.
// Defining the interface here (rather than taking a concrete *Client) lets
// tests inject canned envelopes via stubHealthCaller without spawning a
// real subprocess — the mark3labs/mcp-go client is non-trivial to fake.
type healthCaller interface {
	Call(ctx context.Context, tool string, args any) (*Envelope, error)
}

// repoRootProvider is OPTIONALLY implemented by a healthCaller to expose
// the vault-ai filesystem root for sentinel-presence checks (Phase 21
// Plan 21-c HARD-14 sub-signal 1/3: dr-drill-stale.warn). A type assertion
// in ComputeVaultHealthScore extracts the path when available; absent
// implementation = sentinel never fires (safe default; no false positives).
//
// The production *Client implements this via the VaultAIRepoRoot stored
// at construction (mcp.NewClient); tests inject the path via a stub method.
type repoRootProvider interface {
	RepoRoot() string
}

// sentinelExists returns true when `<repoRoot>/_tooling/state/<name>` is
// present on disk. A missing file is the green (no-deduction) default.
// Phase 21 Plan 21-c HARD-14 sub-signal 1/3 (dr-drill-stale.warn);
// Phase 21 Plan 21-d HARD-14 sub-signals 2/3 (cost-reconcile-drift.warn)
// and 3/3 (triage-drift.warn) wired via the same helper.
func sentinelExists(repoRoot, name string) bool {
	if repoRoot == "" || name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(repoRoot, "_tooling", "state", name))
	return err == nil
}

// Phase 21 Plan 21-d HARD-14 fold-as-anti-signal NAMED constants per
// ADR-obs-05 §v2.2 Amendment Delta 2 (penalty table). Each sentinel
// presence deducts a fixed number of percentage points from a specific
// pillar BEFORE the 40/30/20/10 weighted composition runs. The
// max(0, raw - deduction) floor guarantees no pillar goes negative
// (tripwire TestVaultHealthScoreNeverNegativeUnderAllSentinels).
//
// Bound via `const` (compile-time literal; pyright-analogue Go-vet pass)
// so the deduction magnitudes are grep-addressable AND testable. The
// associated sentinel file lives at `_tooling/state/<filename>` per the
// Phase 16/20/21c sentinel-file convention.
const (
	// dr-drill-stale.warn -> -5 orphan_compliance_pct (Phase 21c HARD-08
	// emitter; Phase 21c Task 4 reader wired 1/3 of HARD-14).
	drillStaleDeduction = 5
	// triage-drift.warn -> -5 content_sufficiency_pct (Phase 16 TRIAGE-07
	// emitter, pre-existing; codified into ADR-obs-05 §v2.2 Amendment
	// Delta 2 + wired here in Phase 21d Wave 4 as 3/3 of HARD-14).
	triageDriftDeduction = 5
	// cost-reconcile-drift.warn -> -3 coverage_pct (Phase 21d HARD-13
	// reconciler emitter; this is sub-signal 2/3 of HARD-14).
	costReconcileDeduction = 3
)

// coverageReportData mirrors the metric fields ComputeVaultHealthScore
// reads from the get_coverage_report envelope. Missing fields decode to
// 0.0 — the operator gets a low score on incomplete data which is the
// safer default per `feedback_no_auto_state_mutation` (no silent recovery).
//
// Source: ADR-obs-05 §Component definitions (each field is a percentage
// in [0, 100]). The MCP tool emits these as float64 in the envelope.Data
// JSON object.
type coverageReportData struct {
	CoveragePct           float64 `json:"coverage_pct"`
	ContentSufficiencyPct float64 `json:"content_sufficiency_pct"`
	LinkIntegrityPct      float64 `json:"link_integrity_pct"`
}

// ComputeVaultHealthScore composes the ADR-obs-05 vault_health_score
// (0-100 integer) from the canonical MCP tools per CONTEXT D-21 §OQ-1+OQ-3
// Amendment. Returns ErrUpstreamHealthSignalFailed wrapped with the failing
// tool name if any upstream roundtrip errors or surfaces a non-OK envelope.
//
// Pure function over MCP tool responses — preserves CONTEXT D-17 no-bump
// invariant (no new tool added to tools.json; composition lives entirely
// in the Go consumer).
func ComputeVaultHealthScore(ctx context.Context, cl healthCaller) (int, error) {
	if cl == nil {
		return 0, fmt.Errorf("ComputeVaultHealthScore: nil caller")
	}

	// Signal 1 of 4 — coverage / sufficiency / link-integrity (3 of 4 components).
	covEnv, err := cl.Call(ctx, "get_coverage_report", &GetCoverageReportArgs{})
	if err != nil {
		return 0, fmt.Errorf("%w: get_coverage_report transport: %v", ErrUpstreamHealthSignalFailed, err)
	}
	if covEnv == nil || covEnv.Error != nil || !covEnv.OK {
		code := "nil"
		msg := "nil envelope"
		if covEnv != nil {
			code, msg = envelopeFailureDetail(covEnv)
		}
		return 0, fmt.Errorf("%w: get_coverage_report envelope error [%s]: %s", ErrUpstreamHealthSignalFailed, code, msg)
	}

	var cov coverageReportData
	if len(covEnv.Data) > 0 {
		// Tolerate missing fields (decode to 0.0 — composition penalises);
		// non-JSON payload is a hard error.
		if err := json.Unmarshal(covEnv.Data, &cov); err != nil {
			return 0, fmt.Errorf("%w: get_coverage_report data decode: %v", ErrUpstreamHealthSignalFailed, err)
		}
	}

	// Signal 2 of 2 — orphan count.
	orphEnv, err := cl.Call(ctx, "get_orphans", &GetOrphansArgs{})
	if err != nil {
		return 0, fmt.Errorf("%w: get_orphans transport: %v", ErrUpstreamHealthSignalFailed, err)
	}
	if orphEnv == nil || orphEnv.Error != nil || !orphEnv.OK {
		code := "nil"
		msg := "nil envelope"
		if orphEnv != nil {
			code, msg = envelopeFailureDetail(orphEnv)
		}
		return 0, fmt.Errorf("%w: get_orphans envelope error [%s]: %s", ErrUpstreamHealthSignalFailed, code, msg)
	}

	// The get_orphans envelope returns a list of orphan rows (per
	// tools.json v1.5.0 "list of orphan rows"). We count rows and apply
	// the inverse-scale formula. Decode into a generic []any to avoid
	// coupling to the row shape (only the count matters here).
	orphanCount := 0
	if len(orphEnv.Data) > 0 {
		var rows []any
		if err := json.Unmarshal(orphEnv.Data, &rows); err == nil {
			orphanCount = len(rows)
		}
		// Tolerate non-array shape: orphan_count stays 0 → orphan_score 100.
		// This is "best signal we can extract"; documented in ADR-obs-05
		// orphan_compliance_pct definition (governing-row count from W11).
	}

	// Compose per ADR-obs-05 formula. All inputs clamped to [0, 100].
	coveragePct := clamp01Pct(cov.CoveragePct) / 100.0
	sufficiencyPct := clamp01Pct(cov.ContentSufficiencyPct) / 100.0
	linkIntegrityPct := clamp01Pct(cov.LinkIntegrityPct) / 100.0
	orphanScore := orphanComplianceScore(orphanCount)

	// Phase 21 Plan 21-d HARD-14 -- 3 fold-as-anti-signal sentinels per
	// ADR-obs-05 §v2.2 Amendment Delta 2 (penalty table).
	// dr-drill-stale.warn      -> -5 orphan_compliance_pct      (Phase 21c HARD-08; sub-signal 1/3)
	// cost-reconcile-drift.warn -> -3 coverage_pct              (Phase 21d HARD-13; sub-signal 2/3)
	// triage-drift.warn        -> -5 content_sufficiency_pct    (Phase 16 TRIAGE-07; sub-signal 3/3)
	//
	// Each deduction floors at 0 (math.Max guarantees no pillar goes
	// negative; tripwire test TestVaultHealthScoreNeverNegativeUnderAllSentinels).
	// Sentinel-presence reads are GUARDED by type-assertion on the
	// optional repoRootProvider interface: a healthCaller that does
	// not expose RepoRoot() falls through with the sentinel treated
	// as ABSENT (no deduction; safe default).
	orphanPct := orphanScore * 100.0
	coveragePctScaled := coveragePct * 100.0
	sufficiencyPctScaled := sufficiencyPct * 100.0
	if provider, ok := cl.(repoRootProvider); ok {
		root := provider.RepoRoot()
		if sentinelExists(root, "dr-drill-stale.warn") {
			orphanPct = math.Max(0, orphanPct-float64(drillStaleDeduction))
		}
		if sentinelExists(root, "cost-reconcile-drift.warn") {
			coveragePctScaled = math.Max(0, coveragePctScaled-float64(costReconcileDeduction))
		}
		if sentinelExists(root, "triage-drift.warn") {
			sufficiencyPctScaled = math.Max(0, sufficiencyPctScaled-float64(triageDriftDeduction))
		}
	}
	orphanScore = orphanPct / 100.0
	coveragePct = coveragePctScaled / 100.0
	sufficiencyPct = sufficiencyPctScaled / 100.0

	composite := 40.0*coveragePct +
		30.0*sufficiencyPct +
		20.0*orphanScore +
		10.0*linkIntegrityPct

	score := int(math.Round(composite))
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score, nil
}

// envelopeFailureDetail extracts an operator-readable code+message from a
// non-nil failure envelope. A non-OK envelope without an error block is
// malformed per the wire contract — reported explicitly so the operator sees
// the drift instead of a fabricated composite score.
func envelopeFailureDetail(env *Envelope) (code, msg string) {
	if env.Error != nil {
		return env.Error.Code, env.Error.Message
	}
	return "OK_FALSE", "non-OK envelope without error block"
}

// clamp01Pct clamps a percentage value (expected 0-100) into [0, 100].
// Defensive against upstream sending out-of-range values.
func clamp01Pct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// orphanComplianceScore converts an orphan row count into the 0.0-1.0
// orphan_compliance_pct surrogate per CONTEXT D-21 §OQ-1+OQ-3 Amendment.
// Formula: 1.0 - min(1.0, n/100.0)
//   - 0 orphans → 1.0 (perfect compliance)
//   - 50 orphans → 0.5
//   - 100+ orphans → 0.0 (intolerable)
//
// The 100-orphan threshold is the operator's tolerance ceiling; ADR-obs-01
// defines a per-type matrix that a future Plan 21d can wire into this
// function once the cost-tracker daemon ships sufficiency-by-type data.
// Documented as a deviation from ADR-obs-05 in Plan 18-03 SUMMARY.
func orphanComplianceScore(orphanCount int) float64 {
	if orphanCount <= 0 {
		return 1.0
	}
	if orphanCount >= 100 {
		return 0.0
	}
	return 1.0 - float64(orphanCount)/100.0
}

// HealthBand maps a vault_health_score to one of three semantic bands
// per CONTEXT D-28 + ADR-obs-05. Used by both ws vault status (signal-2
// aggregation) and ws vault vault-health-score (exit code).
//
// Returns one of "green" (≥70), "yellow" (50-69), "red" (<50).
func HealthBand(score int) string {
	switch {
	case score >= 70:
		return "green"
	case score >= 50:
		return "yellow"
	default:
		return "red"
	}
}

// HealthBandExitCode converts a band into a Unix exit code per CONTEXT
// D-28. 0=green, 1=yellow, 2=red. Other inputs return 2 (red) as the
// safest default for unknown band strings.
func HealthBandExitCode(band string) int {
	switch band {
	case "green":
		return 0
	case "yellow":
		return 1
	case "red":
		return 2
	default:
		return 2
	}
}

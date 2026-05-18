package cmd

// vault_status_test.go — unit tests for CLI-01 `ws vault status`
// 6-signal composite per CONTEXT D-21. Asserts band → exit-code
// monotonicity, JSON-mode envelope shape, verify_audit_chain shell-out
// args (OQ-3 Amendment), and Phase 21c/21d graceful fallback (D-26).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

func TestVaultStatusAllGreen(t *testing.T) {
	orig := vaultStatusRunFn
	t.Cleanup(func() { vaultStatusRunFn = orig })
	vaultStatusRunFn = func(_ context.Context, _ *cobra.Command) (*statusReport, error) {
		signals := []statusSignal{
			{Label: "MCP liveness", Band: bandGreen, Detail: "MCP responsive (25 tools)"},
			{Label: "vault_health composite", Band: bandGreen, Detail: "vault-health-score: 85 (green)"},
			{Label: "audit-chain integrity", Band: bandGreen, Detail: "all 8 streams verified"},
			{Label: "cost-tracker headroom", Band: bandGreen, Detail: "most recent rollup cost-2026-05.jsonl"},
			{Label: "dedup gate readiness", Band: bandGreen, Detail: "Phase 17 deployed"},
			{Label: "last DR-drill age", Band: bandGreen, Detail: "last drill 14 days ago"},
		}
		return assembleReport(signals), nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "status"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected nil error on all-green status; got %v", err)
	}
	if !strings.Contains(out.String(), "green") {
		t.Errorf("expected 'green' in human output; got %q", out.String())
	}
}

func TestVaultStatusYellowOnAdvisory(t *testing.T) {
	orig := vaultStatusRunFn
	t.Cleanup(func() { vaultStatusRunFn = orig })
	vaultStatusRunFn = func(_ context.Context, _ *cobra.Command) (*statusReport, error) {
		signals := []statusSignal{
			{Label: "MCP liveness", Band: bandGreen, Detail: "MCP responsive"},
			{Label: "vault_health composite", Band: bandYellow, Detail: "vault-health-score: 60 (yellow)"},
			{Label: "audit-chain integrity", Band: bandGreen, Detail: "all 8 streams verified"},
			{Label: "cost-tracker headroom", Band: bandGreen, Detail: "ok"},
			{Label: "dedup gate readiness", Band: bandGreen, Detail: "Phase 17 deployed"},
			{Label: "last DR-drill age", Band: bandGreen, Detail: "ok"},
		}
		return assembleReport(signals), nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "status"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-nil error on yellow band")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 1 {
		t.Errorf("yellow band must map to exit 1; got %d", cerr.code)
	}
	if !strings.Contains(out.String(), "yellow") {
		t.Errorf("expected 'yellow' in human output; got %q", out.String())
	}
	if !strings.Contains(out.String(), "vault_health composite") {
		t.Errorf("expected yellow signal label visible in output; got %q", out.String())
	}
}

func TestVaultStatusRedOnCritical(t *testing.T) {
	orig := vaultStatusRunFn
	t.Cleanup(func() { vaultStatusRunFn = orig })
	vaultStatusRunFn = func(_ context.Context, _ *cobra.Command) (*statusReport, error) {
		// MCP unreachable — degraded report via mcpDownReport equivalent.
		signals := []statusSignal{
			{Label: "MCP liveness", Band: bandRed, Detail: "spawn failed: dial unix: connect: refused"},
			{Label: "vault_health composite", Band: bandYellow, Detail: "skipped (MCP unreachable)"},
			{Label: "audit-chain integrity", Band: bandYellow, Detail: "skipped (MCP unreachable)"},
			{Label: "cost-tracker headroom", Band: bandYellow, Detail: "skipped (MCP unreachable)"},
			{Label: "dedup gate readiness", Band: bandYellow, Detail: "skipped (MCP unreachable)"},
			{Label: "last DR-drill age", Band: bandYellow, Detail: "skipped (MCP unreachable)"},
		}
		return assembleReport(signals), nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "status"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on red band")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 2 {
		t.Errorf("red band must map to exit 2; got %d", cerr.code)
	}
	if !strings.Contains(out.String(), "MCP liveness") {
		t.Errorf("expected MCP liveness signal visible; got %q", out.String())
	}
}

func TestVaultStatusJSONMode(t *testing.T) {
	orig := vaultStatusRunFn
	t.Cleanup(func() { vaultStatusRunFn = orig })
	vaultStatusRunFn = func(_ context.Context, _ *cobra.Command) (*statusReport, error) {
		signals := []statusSignal{
			{Label: "MCP liveness", Band: bandGreen, Detail: "MCP responsive"},
			{Label: "vault_health composite", Band: bandGreen, Detail: "score 85"},
			{Label: "audit-chain integrity", Band: bandGreen, Detail: "verified"},
			{Label: "cost-tracker headroom", Band: bandGreen, Detail: "ok"},
			{Label: "dedup gate readiness", Band: bandGreen, Detail: "deployed"},
			{Label: "last DR-drill age", Band: bandGreen, Detail: "14d"},
		}
		return assembleReport(signals), nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "status", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rep statusReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("--json output must be parseable JSON; got err=%v output=%q", err, out.String())
	}
	if rep.OverallBand != bandGreen {
		t.Errorf("OverallBand = %q; want green", rep.OverallBand)
	}
	if rep.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0", rep.ExitCode)
	}
	if len(rep.Signals) != 6 {
		t.Errorf("expected 6 signals; got %d", len(rep.Signals))
	}
}

func TestVaultStatusVerifyAuditChainShellOut(t *testing.T) {
	// Per CONTEXT D-21 OQ-1+OQ-3 Amendment: verify_audit_chain is invoked
	// via shell-out (`uv run ... -- python -m vault_ai.tooling.verify_audit_chain
	// verify --month YYYY-MM --stream <name> --strict`), NOT MCP roundtrip.
	// Override the package-level seam + assert the invocation surface.
	orig := verifyAuditChainFn
	t.Cleanup(func() { verifyAuditChainFn = orig })

	var gotRepoRoot string
	verifyAuditChainFn = func(_ context.Context, repoRoot string) (string, int, string) {
		gotRepoRoot = repoRoot
		return "", 0, ""
	}

	repo := t.TempDir()
	signal := collectAuditChainSignal(context.Background(), repo)
	if signal.Band != bandGreen && signal.Band != bandYellow {
		// bandYellow acceptable if uv is missing on host (LookPath gate).
		t.Errorf("expected green or yellow (uv-missing fallback); got %q (%s)", signal.Band, signal.Detail)
	}
	// If uv is on PATH, the seam SHOULD have been called with the repo root.
	if signal.Band == bandGreen && gotRepoRoot != repo {
		t.Errorf("expected seam called with repoRoot=%q; got %q", repo, gotRepoRoot)
	}
}

func TestVaultStatusVerifyAuditChainRedSurfaces(t *testing.T) {
	// When the seam returns a non-zero exit code for a stream, the signal
	// MUST surface red with the stream name + first stderr line.
	orig := verifyAuditChainFn
	t.Cleanup(func() { verifyAuditChainFn = orig })
	verifyAuditChainFn = func(_ context.Context, _ string) (string, int, string) {
		return "watcher", 2, "audit verify FAILED at /var/audit/watcher-2026-05.jsonl:42:hash-mismatch"
	}

	// Only meaningful when uv is on PATH; otherwise the LookPath gate
	// short-circuits to yellow. Either way, the signal should reflect
	// the seam's verdict — we tolerate the skip.
	signal := collectAuditChainSignal(context.Background(), t.TempDir())
	switch signal.Band {
	case bandRed:
		if !strings.Contains(signal.Detail, "watcher") {
			t.Errorf("red detail must cite failed stream name; got %q", signal.Detail)
		}
	case bandYellow:
		// uv-missing fallback; acceptable.
	default:
		t.Errorf("expected red or yellow; got %q (%s)", signal.Band, signal.Detail)
	}
}

func TestVaultStatusPhase21cAbsent(t *testing.T) {
	// Per CONTEXT D-26: missing _tooling/logs/dr-drill-*.jsonl surfaces
	// yellow with "not yet ratified", but does NOT escalate to red.
	tmp := t.TempDir() // empty — no dr-drill jsonl
	signal := collectDRDrillAgeSignal(tmp)
	if signal.Band != bandYellow {
		t.Errorf("expected yellow when Phase 21c jsonl absent; got %q", signal.Band)
	}
	if !strings.Contains(signal.Detail, "Phase 21c") {
		t.Errorf("expected 'Phase 21c' in detail; got %q", signal.Detail)
	}
}

func TestVaultStatusPhase21dAbsent(t *testing.T) {
	// Per CONTEXT D-21 + D-26: missing cost-*.jsonl surfaces yellow.
	tmp := t.TempDir() // empty
	signal := collectCostHeadroomSignal(tmp)
	if signal.Band != bandYellow {
		t.Errorf("expected yellow when Phase 21d jsonl absent; got %q", signal.Band)
	}
	if !strings.Contains(signal.Detail, "Phase 21d") {
		t.Errorf("expected 'Phase 21d' in detail; got %q", signal.Detail)
	}
}

func TestVaultStatusDedupReadinessGreen(t *testing.T) {
	// Probe simulates create_note tool advertising check_dedup_before_create.
	tools := []listedToolFixture{
		{Name: "create_note", Props: map[string]any{"check_dedup_before_create": map[string]any{"type": "boolean"}, "body": map[string]any{"type": "string"}}},
		{Name: "search_notes", Props: map[string]any{"query": map[string]any{"type": "string"}}},
	}
	signal := collectDedupReadinessSignal(toolFixturesToListed(tools))
	if signal.Band != bandGreen {
		t.Errorf("expected green with check_dedup_before_create present; got %q (%s)", signal.Band, signal.Detail)
	}
}

func TestVaultStatusDedupReadinessRed(t *testing.T) {
	tools := []listedToolFixture{
		{Name: "create_note", Props: map[string]any{"body": map[string]any{"type": "string"}}},
	}
	signal := collectDedupReadinessSignal(toolFixturesToListed(tools))
	if signal.Band != bandRed {
		t.Errorf("expected red when create_note lacks check_dedup_before_create; got %q", signal.Band)
	}
}

func TestVaultStatusRegistered(t *testing.T) {
	if !findVaultLeaf(t, "status") {
		t.Fatal("`ws vault status` not registered as a subcommand of `ws vault`")
	}
}

// --- helpers ---

type listedToolFixture struct {
	Name  string
	Props map[string]any
}

func toolFixturesToListed(fixtures []listedToolFixture) []mcp.ListedTool {
	out := make([]mcp.ListedTool, len(fixtures))
	for i, f := range fixtures {
		out[i] = mcp.ListedTool{Name: f.Name, InputProperties: f.Props}
	}
	return out
}

// Smoke: verify the resolveVaultAIRepoRoot fallback chain works.
func TestResolveVaultAIRepoRoot(t *testing.T) {
	t.Setenv("VAULT_AI_REPO_ROOT", "/tmp/explicit-override")
	if got := resolveVaultAIRepoRoot(); got != "/tmp/explicit-override" {
		t.Errorf("expected env override; got %q", got)
	}
	t.Setenv("VAULT_AI_REPO_ROOT", "")
	got := resolveVaultAIRepoRoot()
	if !strings.HasSuffix(got, filepath.Join("projects", "vault-ai")) && got != "." {
		t.Errorf("expected fallback to ~/projects/vault-ai or '.'; got %q", got)
	}
}

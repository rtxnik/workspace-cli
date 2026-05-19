package cmd

// vault_status.go — `ws vault status` 6-signal composite (CLI-01).
//
// Per CONTEXT D-21: aggregates 6 health signals into a single exit-code-encoded
// summary. The aggregate band drives exit code monotonic with the
// vault_health_score bands per ADR-obs-05:
//   green  = all 6 healthy            → exit 0
//   yellow = ≥1 advisory (not critical) → exit 1
//   red    = ≥1 critical              → exit 2
//
// The 6 signals (per CONTEXT D-21 + §OQ-1+OQ-3 Amendment):
//   1. MCP liveness: ListTools roundtrip (sub-1s when healthy)
//   2. Vault health composite: ComputeVaultHealthScore (OQ-1 path B)
//   3. Audit-chain integrity: shell-out verify_audit_chain.py verify
//   4. Cost-tracker headroom: read _tooling/logs/cost-*.jsonl rollup
//   5. Dedup gate readiness: probe tools/list for create_note flag
//   6. Last-DR-drill-age: read _tooling/logs/dr-drill-*.jsonl
//
// Graceful fallback per CONTEXT D-26: signals 4 and 6 may surface "not yet
// ratified" yellow when the upstream Phase 21c/21d has not shipped — does
// NOT fail the overall status unless they are explicitly red.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

// statusBand is the per-signal severity. "green" passes; "yellow" is an
// advisory (visible in output but doesn't move overall band below yellow);
// "red" is critical (forces overall band to red).
type statusBand string

const (
	bandGreen  statusBand = "green"
	bandYellow statusBand = "yellow"
	bandRed    statusBand = "red"
)

// statusSignal is one row in the status table.
type statusSignal struct {
	Label  string     `json:"label"`
	Band   statusBand `json:"band"`
	Detail string     `json:"detail"`
}

// statusReport is the JSON-mode output shape (one final aggregate +
// per-signal breakdown).
type statusReport struct {
	OverallBand statusBand     `json:"overall_band"`
	ExitCode    int            `json:"exit_code"`
	Signals     []statusSignal `json:"signals"`
}

// vaultStatusRunFn is the production-to-test seam for the entire signal
// gathering. Production wires to runVaultStatus which spawns the MCP
// subprocess; tests override with a closure returning a canned report.
var vaultStatusRunFn = runVaultStatus

// runVaultStatus is the production gatherer: spawn MCP client, run all 6
// signal collectors, return the assembled report.
func runVaultStatus(ctx context.Context, root *cobra.Command) (*statusReport, error) {
	cl, err := mcp.NewClient(ctx, mcp.Options{
		VaultAIRepoRoot: os.Getenv("VAULT_AI_REPO_ROOT"),
		Version:         root.Version,
	})
	if err != nil {
		// MCP unreachable — return a degraded report with signal-1 red.
		// Other signals can't be collected without the client; surface as
		// "skipped (MCP down)" so the operator sees what's missing.
		return mcpDownReport(err), nil
	}
	stop := mcp.InstallSignalForward(cl)
	defer stop()
	defer func() { _ = cl.Close(ctx) }()

	repoRoot := resolveVaultAIRepoRoot()

	// Signal 1: MCP liveness — also caches ListTools result for signal 5.
	tools, livenessSignal := collectMCPLiveness(ctx, cl)

	// Signal 2: vault_health composite (OQ-1 path B).
	healthSignal := collectVaultHealthSignal(ctx, cl)

	// Signal 3: audit-chain integrity (shell-out per OQ-3 Amendment).
	chainSignal := collectAuditChainSignal(ctx, repoRoot)

	// Signal 4: cost-tracker headroom (jsonl rollup fallback per D-21).
	costSignal := collectCostHeadroomSignal(repoRoot)

	// Signal 5: dedup gate readiness (tools/list probe).
	dedupSignal := collectDedupReadinessSignal(tools)

	// Signal 6: last DR-drill age (jsonl read per D-21).
	drSignal := collectDRDrillAgeSignal(repoRoot)

	signals := []statusSignal{
		livenessSignal,
		healthSignal,
		chainSignal,
		costSignal,
		dedupSignal,
		drSignal,
	}
	return assembleReport(signals), nil
}

// mcpDownReport builds the degraded report shown when NewClient fails.
// Signal 1 = red; other signals = yellow "skipped (MCP unreachable)" so
// the operator sees the full surface but knows nothing was collected.
func mcpDownReport(spawnErr error) *statusReport {
	skipped := func(label string) statusSignal {
		return statusSignal{
			Label:  label,
			Band:   bandYellow,
			Detail: "skipped (MCP unreachable)",
		}
	}
	signals := []statusSignal{
		{Label: "MCP liveness", Band: bandRed, Detail: fmt.Sprintf("spawn failed: %s", truncate(spawnErr.Error(), 120))},
		skipped("vault_health composite"),
		skipped("audit-chain integrity"),
		skipped("cost-tracker headroom"),
		skipped("dedup gate readiness"),
		skipped("last DR-drill age"),
	}
	return assembleReport(signals)
}

// collectMCPLiveness pings the MCP subprocess via ListTools (cheapest
// roundtrip — no embedder load). Returns the tool catalogue alongside
// the signal so signal 5 (dedup readiness) can reuse it without a
// second roundtrip.
func collectMCPLiveness(ctx context.Context, cl *mcp.Client) ([]mcp.ListedTool, statusSignal) {
	tools, err := cl.ListTools(ctx)
	if err != nil {
		return nil, statusSignal{
			Label:  "MCP liveness",
			Band:   bandRed,
			Detail: fmt.Sprintf("ListTools failed: %s", truncate(err.Error(), 120)),
		}
	}
	return tools, statusSignal{
		Label:  "MCP liveness",
		Band:   bandGreen,
		Detail: fmt.Sprintf("MCP responsive (%d tools advertised)", len(tools)),
	}
}

// collectVaultHealthSignal invokes ComputeVaultHealthScore and maps the
// resulting score into a band per CONTEXT D-28 + ADR-obs-05.
func collectVaultHealthSignal(ctx context.Context, cl *mcp.Client) statusSignal {
	score, err := mcp.ComputeVaultHealthScore(ctx, cl)
	if err != nil {
		return statusSignal{
			Label:  "vault_health composite",
			Band:   bandRed,
			Detail: fmt.Sprintf("composition failed: %s", truncate(err.Error(), 120)),
		}
	}
	band := mcp.HealthBand(score)
	return statusSignal{
		Label:  "vault_health composite",
		Band:   statusBand(band),
		Detail: fmt.Sprintf("vault-health-score: %d (%s)", score, band),
	}
}

// verifyAuditChainFn is a package-level test seam for the
// verify_audit_chain.py shell-out. Production wires to the real exec
// invocation. Tests override to assert the command shape without
// actually spawning uv.
//
// Returns the per-stream-or-aggregate exit code (0 = chain intact) and
// any captured stderr first line for diagnostic output.
var verifyAuditChainFn = invokeVerifyAuditChain

// invokeVerifyAuditChain runs `uv run --project <repo>/_tooling/mcp --
// python -m vault_ai.tooling.verify_audit_chain verify --month YYYY-MM
// --stream <s> --strict` for each of the 8 streams declared in
// vault-ai/_tooling/mcp/vault_ai/tooling/verify_audit_chain.py STREAMS
// tuple at HEAD 2026-05-18. CONTEXT D-21 §OQ-1+OQ-3 Amendment selects
// this shell-out transport over an MCP roundtrip because
// verify_audit_chain is a Python CLI script (argparse, no FastMCP).
//
// Returns (failedStream, exitCode, stderrFirstLine). failedStream=="" on
// all-green; otherwise it's the first stream that failed.
func invokeVerifyAuditChain(ctx context.Context, repoRoot string) (failedStream string, code int, stderrFirstLine string) {
	streams := []string{"mcp", "search", "visibility", "cost", "embedder", "dedup", "triage", "watcher"}
	month := time.Now().UTC().Format("2006-01")
	for _, s := range streams {
		args := []string{
			"run",
			"--project", filepath.Join(repoRoot, "_tooling", "mcp"),
			"--",
			"python", "-m", "vault_ai.tooling.verify_audit_chain",
			"verify",
			"--month", month,
			"--stream", s,
			"--strict",
		}
		cmd := exec.CommandContext(ctx, "uv", args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			exitCode := 1
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			}
			line := strings.TrimSpace(strings.SplitN(stderr.String(), "\n", 2)[0])
			return s, exitCode, line
		}
	}
	return "", 0, ""
}

// collectAuditChainSignal runs verify_audit_chain.py across all 8 streams
// for the current month. Any failed stream surfaces as red.
func collectAuditChainSignal(ctx context.Context, repoRoot string) statusSignal {
	if _, err := exec.LookPath("uv"); err != nil {
		return statusSignal{
			Label:  "audit-chain integrity",
			Band:   bandYellow,
			Detail: "uv not on PATH — cannot invoke verify_audit_chain",
		}
	}
	failed, code, stderrLine := verifyAuditChainFn(ctx, repoRoot)
	if code == 0 {
		return statusSignal{
			Label:  "audit-chain integrity",
			Band:   bandGreen,
			Detail: "all 8 streams verified for current month",
		}
	}
	detail := fmt.Sprintf("stream %q chain broken (exit %d)", failed, code)
	if stderrLine != "" {
		detail = fmt.Sprintf("%s: %s", detail, truncate(stderrLine, 80))
	}
	return statusSignal{
		Label:  "audit-chain integrity",
		Band:   bandRed,
		Detail: detail,
	}
}

// collectCostHeadroomSignal reads the most recent _tooling/logs/cost-*.jsonl
// rollup and reports headroom. Per CONTEXT D-21: Phase 21d cost-tracker
// daemon not yet shipped — read-from-jsonl fallback. File absent → yellow
// "not yet ratified".
func collectCostHeadroomSignal(repoRoot string) statusSignal {
	logsDir := filepath.Join(repoRoot, "_tooling", "logs")
	matches, _ := filepath.Glob(filepath.Join(logsDir, "cost-*.jsonl"))
	if len(matches) == 0 {
		return statusSignal{
			Label:  "cost-tracker headroom",
			Band:   bandYellow,
			Detail: "no cost-*.jsonl found — Phase 21d daemon not yet shipped (fallback only)",
		}
	}
	// Newest jsonl wins.
	sort.Slice(matches, func(i, j int) bool {
		ai, _ := os.Stat(matches[i])
		aj, _ := os.Stat(matches[j])
		if ai == nil || aj == nil {
			return false
		}
		return ai.ModTime().After(aj.ModTime())
	})
	path := matches[0]
	info, err := os.Stat(path)
	if err != nil {
		return statusSignal{
			Label:  "cost-tracker headroom",
			Band:   bandYellow,
			Detail: fmt.Sprintf("stat %s: %v", filepath.Base(path), err),
		}
	}
	// We can't parse domain-specific budget headroom here without the
	// Phase 21d schema; report file age + presence as the v2.2 fallback.
	age := time.Since(info.ModTime()).Round(time.Hour)
	return statusSignal{
		Label:  "cost-tracker headroom",
		Band:   bandGreen,
		Detail: fmt.Sprintf("most recent rollup %s (age %s)", filepath.Base(path), age),
	}
}

// collectDedupReadinessSignal probes the tools/list result for the
// create_note tool's check_dedup_before_create input property — Phase 17
// dedup gate is deployed iff that property is advertised in the schema.
func collectDedupReadinessSignal(tools []mcp.ListedTool) statusSignal {
	if len(tools) == 0 {
		return statusSignal{
			Label:  "dedup gate readiness",
			Band:   bandYellow,
			Detail: "skipped (MCP liveness probe returned no tools)",
		}
	}
	for _, t := range tools {
		if t.Name != "create_note" {
			continue
		}
		if _, ok := t.InputProperties["check_dedup_before_create"]; ok {
			return statusSignal{
				Label:  "dedup gate readiness",
				Band:   bandGreen,
				Detail: "create_note advertises check_dedup_before_create (Phase 17 deployed)",
			}
		}
		return statusSignal{
			Label:  "dedup gate readiness",
			Band:   bandRed,
			Detail: "create_note missing check_dedup_before_create — Phase 17 not deployed",
		}
	}
	return statusSignal{
		Label:  "dedup gate readiness",
		Band:   bandRed,
		Detail: "create_note tool absent from tools/list — XREPO-01 drift",
	}
}

// collectDRDrillAgeSignal reads the most recent
// _tooling/logs/dr-drill-*.jsonl mtime. Per CONTEXT D-21: Phase 21c not
// yet shipped — file absent yields yellow "not yet ratified" (does NOT
// fail status). When present, ≤95d = green, 95-100d = yellow, >100d = red.
func collectDRDrillAgeSignal(repoRoot string) statusSignal {
	logsDir := filepath.Join(repoRoot, "_tooling", "logs")
	matches, _ := filepath.Glob(filepath.Join(logsDir, "dr-drill-*.jsonl"))
	if len(matches) == 0 {
		return statusSignal{
			Label:  "last DR-drill age",
			Band:   bandYellow,
			Detail: "no dr-drill-*.jsonl found — Phase 21c DR drill not yet shipped",
		}
	}
	var newest time.Time
	for _, p := range matches {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return statusSignal{
			Label:  "last DR-drill age",
			Band:   bandYellow,
			Detail: "dr-drill-*.jsonl present but unreadable",
		}
	}
	ageDays := int(time.Since(newest).Hours() / 24)
	switch {
	case ageDays <= 95:
		return statusSignal{
			Label:  "last DR-drill age",
			Band:   bandGreen,
			Detail: fmt.Sprintf("last drill %d days ago", ageDays),
		}
	case ageDays <= 100:
		return statusSignal{
			Label:  "last DR-drill age",
			Band:   bandYellow,
			Detail: fmt.Sprintf("last drill %d days ago (approaching 100d ceiling)", ageDays),
		}
	default:
		return statusSignal{
			Label:  "last DR-drill age",
			Band:   bandRed,
			Detail: fmt.Sprintf("last drill %d days ago (>100d critical)", ageDays),
		}
	}
}

// assembleReport reduces the 6 signal bands into the overall band per
// CONTEXT D-21: red wins; else yellow if any yellow; else green. Exit
// code 0/1/2 maps directly per CONTEXT D-21 final paragraph (monotonic
// with vault_health_score bands).
func assembleReport(signals []statusSignal) *statusReport {
	worst := bandGreen
	for _, s := range signals {
		switch s.Band {
		case bandRed:
			worst = bandRed
		case bandYellow:
			if worst != bandRed {
				worst = bandYellow
			}
		}
	}
	return &statusReport{
		OverallBand: worst,
		ExitCode:    bandExitCode(worst),
		Signals:     signals,
	}
}

func bandExitCode(b statusBand) int {
	switch b {
	case bandGreen:
		return 0
	case bandYellow:
		return 1
	default:
		return 2
	}
}

// truncate caps a string at maxLen chars (with ellipsis suffix when cut).
func truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// resolveVaultAIRepoRoot mirrors internal/mcp/client.go's NewClient
// fallback chain so the status command's shell-out + jsonl reads use
// the same root the MCP subprocess will use.
func resolveVaultAIRepoRoot() string {
	if v := os.Getenv("VAULT_AI_REPO_ROOT"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "projects", "vault-ai")
	}
	return "."
}

func newVaultStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "status",
		Short:         "Aggregate vault health across 6 signals",
		Long:          "ws vault status aggregates 6 health signals (MCP liveness, vault_health composite, audit-chain integrity, cost-tracker headroom, dedup gate readiness, last DR-drill age) into one exit-code-encoded summary per CONTEXT D-21. Exit code 0=green, 1=yellow, 2=red — monotonic with vault_health_score bands per ADR-obs-05.",
		Annotations:   vaultAnnotation,
		Args:          cobra.NoArgs,
		SilenceErrors: true, // empty-msg cliErrorWithExit drives exit codes; no extra Error: line wanted
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			rep, err := vaultStatusRunFn(ctx, cmd.Root())
			if err != nil {
				return fmt.Errorf("status: %w", err)
			}
			if rep == nil {
				return fmt.Errorf("status: nil report")
			}

			jsonFlag, _ := cmd.Flags().GetBool("json")
			if err := renderStatusReport(cmd.OutOrStdout(), rep, jsonFlag); err != nil {
				return fmt.Errorf("status: render: %w", err)
			}

			if rep.ExitCode == 0 {
				return nil
			}
			return &cliErrorWithExit{code: rep.ExitCode, msg: ""}
		},
	}
}

// renderStatusReport writes the report to out in either JSON mode
// (single JSON object — not NDJSON because the report is one logical
// document) or the human-readable table.
func renderStatusReport(out io.Writer, rep *statusReport, jsonMode bool) error {
	if jsonMode {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(raw))
		return err
	}
	var b strings.Builder
	b.WriteString(output.SectionStyle.Render("Vault Status"))
	b.WriteString("\n\n")
	for _, s := range rep.Signals {
		fmt.Fprintf(&b, "  %s %s — %s\n", bandIcon(s.Band), output.StyleDim.Render(s.Label), s.Detail)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Overall: %s (exit %d)\n",
		bandLabel(rep.OverallBand), rep.ExitCode)
	_, err := fmt.Fprint(out, b.String())
	return err
}

func bandIcon(b statusBand) string {
	switch b {
	case bandGreen:
		return output.StyleSuccess.Render("●")
	case bandYellow:
		return output.StyleWarning.Render("●")
	default:
		return output.StyleError.Render("●")
	}
}

func bandLabel(b statusBand) string {
	switch b {
	case bandGreen:
		return lipgloss.NewStyle().Foreground(output.Green).Bold(true).Render("green")
	case bandYellow:
		return lipgloss.NewStyle().Foreground(output.Yellow).Bold(true).Render("yellow")
	default:
		return lipgloss.NewStyle().Foreground(output.Red).Bold(true).Render("red")
	}
}

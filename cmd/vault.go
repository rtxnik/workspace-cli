package cmd

// vault.go is the Cobra root for the `ws vault` subcommand family per
// CONTEXT D-02 (filename convention `vault_<command>.go` snake-case) +
// D-03 (depth-2 Cobra surface — no nested `ws vault profile <subcmd>`).
//
// Plan 18-03 (Wave 2) registers 5 read-only leaves here:
//   status, search, validate, get-coverage-report, vault-health-score
//
// Plan 18-04 will extend this init() with 4 mutating leaves:
//   triage-run, ingest, reindex, backup-verify
//
// Plan 18-05 will add the diagnostic leaf:
//   doctor
//
// Total 10 leaves at v2.2 ship per ADR-int-03 10-command cap.

import (
	"github.com/spf13/cobra"
)

// vaultAnnotation is the per-leaf group tag consumed by the groupedUsageTemplate
// in cmd/root.go to render the "Vault Commands:" section.
var vaultAnnotation = map[string]string{"group": "vault"}

var vaultCmd = &cobra.Command{
	Use:         "vault",
	Short:       "Vault-AI MCP operations",
	Long:        "Vault-AI MCP operations — read/mutate the personal knowledge vault via the stdio MCP transport (single chokepoint at internal/mcp.Client per CONTEXT D-05).",
	Annotations: vaultAnnotation,
}

func init() {
	// Plan 18-03 Task 1 (3 simpler MCP-wrapper read leaves):
	vaultCmd.AddCommand(newVaultSearchCmd())
	vaultCmd.AddCommand(newVaultValidateCmd())
	vaultCmd.AddCommand(newVaultGetCoverageReportCmd())
	// Plan 18-03 Task 2 (CLI-09 vault-health-score):
	vaultCmd.AddCommand(newVaultVaultHealthScoreCmd())
	// Plan 18-03 Task 3 (CLI-01 ship-gate — 6-signal composite):
	vaultCmd.AddCommand(newVaultStatusCmd())
	// Plan 18-04 Task 1a (CLI-03 triage-run MCP wrapper):
	vaultCmd.AddCommand(newVaultTriageRunCmd())
	// Plan 18-04 Task 1b (CLI-05 reindex shell-out — CONTEXT D-27 §OQ-3 Amendment):
	vaultCmd.AddCommand(newVaultReindexCmd())
	// Plan 18-04 Task 2a (CLI-06 backup-verify — graceful fallback for Phase 21c HARD-07):
	vaultCmd.AddCommand(newVaultBackupVerifyCmd())
	// Plan 18-04 Task 2b (CLI-07 ingest — dedup-gated create_note + --dedup-force/--yes pair):
	vaultCmd.AddCommand(newVaultIngestCmd())
	// Plan 18-05 diagnostic leaf (Wave 4): doctor

	rootCmd.AddCommand(vaultCmd)
}

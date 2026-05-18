package cmd

// vault_vault_health_score.go — `ws vault vault-health-score` leaf (CLI-09).
//
// Per CONTEXT D-28: prints the integer 0-100 vault_health_score to stdout
// (machine-parseable for cron consumers) + maps the score into bands
// 0/1/2 for shell control flow.
//
// Per CONTEXT D-21 §OQ-1+OQ-3 Amendment: the score is composed Go-side
// via internal/mcp.ComputeVaultHealthScore from existing MCP tools
// (get_coverage_report + get_orphans). The MCP `vault_health` tool is
// NOT in tools.json v1.3.0; composition preserves D-17 no-bump invariant.
//
// Exit codes per CONTEXT D-28 + ADR-obs-05 bands:
//   0  if score ≥ 70 (green; v2.1 ship-target)
//   1  if 50 ≤ score ≤ 69 (yellow; v2.2 advisory)
//   2  if score < 50 (red)

import (
	"context"
	"fmt"
	"os"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

// vaultHealthScoreComputeFn is the production-to-test seam for the
// composition function. Production wires to mcp.ComputeVaultHealthScore;
// tests inject a stub returning a canned score so the band-mapping logic
// can be exercised without spawning MCP.
//
// Note the seam accepts a *mcp.Client (not the healthCaller interface)
// because the production wrapper has to spawn the subprocess. The test
// override ignores the client argument and returns the canned value.
var vaultHealthScoreComputeFn = runVaultHealthScoreCompute

// runVaultHealthScoreCompute is the production runner: spawn client,
// invoke composition, return the score.
func runVaultHealthScoreCompute(ctx context.Context, root *cobra.Command) (int, error) {
	cl, err := mcp.NewClient(ctx, mcp.Options{
		VaultAIRepoRoot: os.Getenv("VAULT_AI_REPO_ROOT"),
		Version:         root.Version,
	})
	if err != nil {
		return 0, fmt.Errorf("spawn MCP client: %w", err)
	}
	stop := mcp.InstallSignalForward(cl)
	defer stop()
	defer cl.Close(ctx)

	return mcp.ComputeVaultHealthScore(ctx, cl)
}

func newVaultVaultHealthScoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "vault-health-score",
		Short:         "Print the 0-100 vault_health_score; exit code by band (0/1/2)",
		Long:          "Compose the ADR-obs-05 vault_health_score from MCP get_coverage_report + get_orphans per CONTEXT D-21 OQ-1+OQ-3 Amendment. Prints the integer 0-100 to stdout (machine-parseable). Exit code: 0 if ≥70 (green), 1 if 50-69 (yellow), 2 if <50 (red).",
		Annotations:   vaultAnnotation,
		Args:          cobra.NoArgs,
		SilenceErrors: true, // empty-msg cliErrorWithExit drives exit codes
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			score, err := vaultHealthScoreComputeFn(ctx, cmd.Root())
			if err != nil {
				return fmt.Errorf("vault-health-score: %w", err)
			}

			// Machine-parseable: just the integer on stdout, no prefix.
			fmt.Fprintln(cmd.OutOrStdout(), score)

			// Map to band-driven exit code; empty msg suppresses Cobra's
			// "Error:" line so cron consumers see only the score on stdout
			// while shells can branch on $?.
			band := mcp.HealthBand(score)
			code := mcp.HealthBandExitCode(band)
			if code == 0 {
				return nil
			}
			return &cliErrorWithExit{code: code, msg: ""}
		},
	}
}

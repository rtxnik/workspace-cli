package cmd

// vault_triage_run.go — `ws vault triage-run` leaf (CLI-03).
//
// Wraps the MCP `triage_run` tool (24th tool per Phase 16) per CONTEXT D-23.
// MUTATING leaf with a --dry-run safety toggle (propose-only; no writes).
// Operator-override surface (`triage_override`) is deferred to v2.3 per D-23.
//
// Flag mapping is 1:1 with tools.json v1.5.0 triage_run input schema
// (verified at Plan 18-04 RED via `jq '.tools[]|select(.name=="triage_run").input'`):
//   --session-id <str>  → session_id
//   --limit <int>       → limit (1..500; default 50 on server side)
//   --dry-run           → dry_run (true = propose-only; audit row carries user_confirmation='dry-run')
//   --no-batch          → no_batch (true = force per-op operator confirmation)
//
// NOTE: the plan's draft <interfaces> block proposed an `--inbox <path>` flag,
// but the contract has no `inbox_path` field. Tracked as Rule 1 deviation in
// 18-04-SUMMARY.md (flag-to-contract realignment).

import (
	"context"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

// vaultTriageRunCallFn is the production-to-test seam for the MCP roundtrip.
var vaultTriageRunCallFn = runVaultTriageRun

func runVaultTriageRun(ctx context.Context, root *cobra.Command, targs mcp.TriageRunArgs) (*mcp.Envelope, error) {
	return callVaultTool(ctx, root.Version, "triage_run", &targs)
}

func newVaultTriageRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "triage-run",
		Short:       "Run inbox triage pass (propose + route notes)",
		Long:        "Process inbox notes through the triage agent (24th MCP tool, Phase 16). Use --dry-run to preview routing without writing. Wraps MCP triage_run (CLI-03).",
		Annotations: vaultAnnotation,
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			sessionID, _ := cmd.Flags().GetString("session-id")
			limit, _ := cmd.Flags().GetInt("limit")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			noBatch, _ := cmd.Flags().GetBool("no-batch")

			targs := mcp.TriageRunArgs{
				SessionId: sessionID,
				Limit:     limit,
				DryRun:    dryRun,
				NoBatch:   noBatch,
			}

			env, err := vaultTriageRunCallFn(ctx, cmd.Root(), targs)
			return vaultRenderResult(cmd, "triage-run", env, err)
		},
	}
	cmd.Flags().String("session-id", "", "Operator-supplied session correlation id (auto-generated when omitted)")
	cmd.Flags().Int("limit", 0, "Maximum inbox notes to process this session (server default 50; range 1-500)")
	cmd.Flags().Bool("dry-run", false, "Propose-only — no writes; audit rows carry user_confirmation='dry-run'")
	cmd.Flags().Bool("no-batch", false, "Force per-op operator confirmation regardless of Pass-1 confidence")
	return cmd
}

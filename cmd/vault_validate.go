package cmd

// vault_validate.go — `ws vault validate <note-id>` leaf (CLI-04).
//
// Wraps the MCP `validate_note` tool per CONTEXT D-25. Read-only — runs the
// per-type content contract (ADR-schema-05) + sufficiency linter against a
// single note ID and returns structured findings.
//
// Exit codes:
//   - 0: note is valid (envelope.OK==true)
//   - 1: VALIDATION_FAILED (findings present)
//   - 5: NOT_IMPLEMENTED (tool missing — XREPO-01 drift signal)
//   - other: per MCP error code → MapErrorCodeToExitCode

import (
	"context"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

// vaultValidateRunFn is the production-to-test seam for the MCP roundtrip.
var vaultValidateRunFn = runVaultValidate

func runVaultValidate(ctx context.Context, root *cobra.Command, vargs mcp.ValidateNoteArgs) (*mcp.Envelope, error) {
	return callVaultTool(ctx, root.Version, "validate_note", &vargs)
}

func newVaultValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "validate <note-id>",
		Short:       "Validate a vault note against its per-type content contract",
		Long:        "Runs JSON Schema + content-sufficiency validation of a note via MCP validate_note (CLI-04). Exit 0 on green, 1 on findings, other codes per MCP error envelope.",
		Annotations: vaultAnnotation,
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			env, err := vaultValidateRunFn(ctx, cmd.Root(), mcp.ValidateNoteArgs{
				Id: args[0],
			})
			return vaultRenderResult(cmd, "validate", env, err)
		},
	}
}

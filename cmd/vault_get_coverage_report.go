package cmd

// vault_get_coverage_report.go — `ws vault get-coverage-report` leaf (CLI-08).
//
// Read-only wrapper around the MCP `get_coverage_report` tool per CONTEXT
// D-25. Routes through the internal/mcp.Client single chokepoint (D-05).
//
// Output modes:
//   - --json (root persistent flag): emits the envelope.Data JSON to stdout
//   - default (human): pretty-prints the envelope.Data JSON indented to stdout
//
// The tool itself has no input args (input: {} in tools.json v1.5.0).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

// vaultGetCoverageReportRunFn is the production-to-test seam for the
// end-to-end execution. Production wires to runVaultGetCoverageReport which
// spawns a real subprocess; tests override with a closure that returns a
// canned envelope without touching MCP.
var vaultGetCoverageReportRunFn = runVaultGetCoverageReport

// runVaultGetCoverageReport is the production runner: spawn client, call
// the MCP tool, return the envelope (or an error).
func runVaultGetCoverageReport(ctx context.Context, root *cobra.Command) (*mcp.Envelope, error) {
	return callVaultTool(ctx, root.Version, "get_coverage_report", &mcp.GetCoverageReportArgs{})
}

func newVaultGetCoverageReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "get-coverage-report",
		Short:       "Coverage diff between sibling-repo entities and vault notes",
		Long:        "Read-only coverage diff between sibling-repo entities (workspace-cli, workflow-kit, dotfiles) and vault notes; wraps MCP get_coverage_report tool. Output flagged via --json (raw envelope.Data) or default (indented JSON).",
		Annotations: vaultAnnotation,
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			env, err := vaultGetCoverageReportRunFn(ctx, cmd.Root())
			return vaultRenderResult(cmd, "get-coverage-report", env, err)
		},
	}
}

// renderCoverageReport writes envelope.Data to out in either raw passthrough
// mode (--json) or indented JSON (default). Extracted for unit-test
// readability — the rendering branch is pure I/O over a byte slice.
func renderCoverageReport(out io.Writer, data json.RawMessage, jsonMode bool) error {
	if jsonMode {
		_, err := fmt.Fprintln(out, string(data))
		return err
	}
	var pretty interface{}
	if err := json.Unmarshal(data, &pretty); err != nil {
		_, e := fmt.Fprintln(out, string(data))
		return e
	}
	return output.WriteJSON(out, pretty)
}

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
// The tool itself has no input args (input: {} in tools.json v1.3.0).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rtxnik/workspace-cli/internal/mcp"
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
	cl, err := mcp.NewClient(ctx, mcp.Options{
		VaultAIRepoRoot: os.Getenv("VAULT_AI_REPO_ROOT"),
		Version:         root.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn MCP client: %w", err)
	}
	stop := mcp.InstallSignalForward(cl)
	defer stop()
	defer cl.Close(ctx)

	env, err := cl.Call(ctx, "get_coverage_report", &mcp.GetCoverageReportArgs{})
	if err != nil {
		return nil, fmt.Errorf("MCP roundtrip: %w", err)
	}
	return env, nil
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
			if err != nil {
				return fmt.Errorf("get-coverage-report: %w", err)
			}
			if env == nil {
				return fmt.Errorf("get-coverage-report: nil envelope")
			}
			if env.Error != nil {
				return &cliErrorWithExit{
					code: mcp.MapErrorCodeToExitCode(env.Error.Code),
					msg:  fmt.Sprintf("get-coverage-report: %s: %s", env.Error.Code, env.Error.Message),
				}
			}

			jsonFlag, _ := cmd.Flags().GetBool("json")
			return renderCoverageReport(cmd.OutOrStdout(), env.Data, jsonFlag)
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
	indented, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		_, e := fmt.Fprintln(out, string(data))
		return e
	}
	_, err = fmt.Fprintln(out, string(indented))
	return err
}

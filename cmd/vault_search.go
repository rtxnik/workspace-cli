package cmd

// vault_search.go — `ws vault search [query]` leaf (CLI-02).
//
// Wraps the MCP `search_notes` tool per CONTEXT D-22 (NOT `search_hybrid`
// — REQUIREMENTS CLI-02 wording auto-fix per Rule-1 reconciliation;
// `search_notes` IS the hybrid-search tool in tools.json v1.3.0). Routes
// through internal/mcp.Client single chokepoint (D-05).
//
// Output:
//   - --json (root persistent flag): raw envelope.Data passthrough (NDJSON-friendly)
//   - default (human): indented JSON

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

// vaultSearchRunFn is the production-to-test seam for the end-to-end
// roundtrip. Production wires to runVaultSearch which spawns the
// subprocess; tests override.
var vaultSearchRunFn = runVaultSearch

func runVaultSearch(ctx context.Context, root *cobra.Command, sargs mcp.SearchNotesArgs) (*mcp.Envelope, error) {
	cl, err := mcp.NewClient(ctx, mcp.Options{
		VaultAIRepoRoot: os.Getenv("VAULT_AI_REPO_ROOT"),
		Version:         root.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn MCP client: %w", err)
	}
	stop := mcp.InstallSignalForward(cl)
	defer stop()
	defer func() { _ = cl.Close(ctx) }()

	env, err := cl.Call(ctx, "search_notes", &sargs)
	if err != nil {
		return nil, fmt.Errorf("MCP roundtrip: %w", err)
	}
	return env, nil
}

func newVaultSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "search [query]",
		Short:       "Hybrid search (RRF + reranker) over vault notes",
		Long:        "Free-text search across the vault using the hybrid retriever (BM25 + dense + reranker). Wraps MCP search_notes (CLI-02). Output JSON via --json or indented JSON otherwise.",
		Annotations: vaultAnnotation,
		Args:        cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			limit, _ := cmd.Flags().GetInt("limit")
			if limit <= 0 {
				limit = 10
			}

			query := strings.TrimSpace(strings.Join(args, " "))
			if query == "" {
				return fmt.Errorf("search: empty query")
			}

			env, err := vaultSearchRunFn(ctx, cmd.Root(), mcp.SearchNotesArgs{
				Query: query,
				Limit: limit,
			})
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}
			if env == nil {
				return fmt.Errorf("search: nil envelope")
			}
			if env.Error != nil {
				return &cliErrorWithExit{
					code: mcp.MapErrorCodeToExitCode(env.Error.Code),
					msg:  fmt.Sprintf("search: %s: %s", env.Error.Code, env.Error.Message),
				}
			}

			jsonFlag, _ := cmd.Flags().GetBool("json")
			return renderCoverageReport(cmd.OutOrStdout(), env.Data, jsonFlag)
		},
	}
	cmd.Flags().IntP("limit", "n", 10, "Maximum number of results to return (1-100)")
	return cmd
}

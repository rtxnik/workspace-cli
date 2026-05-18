package cmd

// vault_reindex.go — `ws vault reindex` leaf (CLI-05).
//
// Shell-out to vault-ai's `_tooling/mcp/vault_ai/cli/embed_index.py index`
// per CONTEXT D-27 + §OQ-3 Amendment. Reindex is a long-running offline
// operation that does not fit MCP stdio JSON-RPC semantics (would block the
// loop for minutes); the architectural responsibility map permits shell-out
// in this specific case. Subprocess stdout/stderr stream through to the
// operator and the exit code is passed through unchanged.
//
// Why a package constant for the subcommand name: CONTEXT D-27 §OQ-3 Amendment
// requires byte-identical match against the live `embed_index.py` subparser.
// `reindexSubcommandName` is the single grep target so a future contributor
// renaming the Python subparser surfaces as one test failure
// (TestReindexSubcommandNameConstant) instead of a string scattered across
// the package.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

// reindexSubcommandName is the live `embed_index.py` argparse subcommand name
// verified at Wave 0 (Plan 18-00 SUMMARY) + at unit-test time
// (TestReindexSubcommandNameConstant). See CONTEXT D-27 §OQ-3 Amendment.
const reindexSubcommandName = "index"

// runReindexFn is the production-to-test seam. Production wires to
// runReindex which actually invokes os/exec; tests override with a closure
// that captures the invocation args.
var runReindexFn = runReindex

// runReindex invokes the Python embed_index.py CLI via `uv run` and streams
// its stdout/stderr to the parent CLI's outputs. On non-zero exit, returns a
// cliErrorWithExit wrapping the subprocess exit code so the operator sees
// the same exit status as if they had invoked embed_index.py directly.
func runReindex(ctx context.Context, root *cobra.Command, name string, cmdArgs []string) error {
	c := exec.CommandContext(ctx, name, cmdArgs...)
	c.Stdout = root.OutOrStdout()
	c.Stderr = root.ErrOrStderr()
	c.Stdin = nil
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &cliErrorWithExit{
				code: exitErr.ExitCode(),
				msg:  fmt.Sprintf("reindex: embed_index.py exited with status %d", exitErr.ExitCode()),
			}
		}
		// Transport-level failure (uv not on PATH, fork failure, etc.) —
		// MISSING_DEPENDENCY exit code per ADR-int-03 exit table.
		return &cliErrorWithExit{
			code: 4,
			msg:  fmt.Sprintf("reindex: invoke %s: %v", name, err),
		}
	}
	return nil
}

func newVaultReindexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "reindex [paths...]",
		Short:       "Re-embed notes (offline; shells out to embed_index.py)",
		Long:        "Re-embed notes via the vault-ai embedder CLI. Long-running offline operation — see CONTEXT D-27 for architectural rationale (does NOT route through MCP). Pass file/dir paths to scope; omit to scope by other flags (--changed-only).",
		Annotations: vaultAnnotation,
		Args:        cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Resolve vault-ai repo root per CONTEXT D-08 fallback chain.
			repoRoot := os.Getenv("VAULT_AI_REPO_ROOT")
			if repoRoot == "" {
				if home, err := os.UserHomeDir(); err == nil {
					repoRoot = filepath.Join(home, "projects", "vault-ai")
				}
			}
			if repoRoot == "" {
				return &cliErrorWithExit{code: 4, msg: "reindex: VAULT_AI_REPO_ROOT unset and $HOME unavailable"}
			}

			changedOnly, _ := cmd.Flags().GetBool("changed-only")

			invocation := []string{
				"run",
				"--project", filepath.Join(repoRoot, "_tooling", "mcp"),
				"--",
				"python", "-m", "vault_ai.cli.embed_index",
				reindexSubcommandName,
			}
			invocation = append(invocation, args...)
			if changedOnly {
				invocation = append(invocation, "--changed-only")
			}

			return runReindexFn(ctx, cmd.Root(), "uv", invocation)
		},
	}
	cmd.Flags().Bool("changed-only", false, "Scope to `git diff HEAD --name-only` .md files (pass-through to embed_index.py)")
	return cmd
}

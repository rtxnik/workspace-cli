package cmd

// vault_client.go — shared helpers for the `ws vault` cobra leaves.
//
// Every vault leaf spawns the same stdio MCP client (NewClient ->
// InstallSignalForward -> defer Close) and, for the envelope-returning
// leaves, runs the same RunE tail (transport-error wrap -> nil-envelope
// guard -> envelope-error -> render). These helpers hold the single copy.

import (
	"context"
	"fmt"
	"os"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

// vaultErrExit maps an MCP envelope error to the leaf-level cliErrorWithExit,
// preserving the "<leaf>: <code>: <message>" convention every vault leaf uses
// and the 0-7 exit-code mapping (CONTEXT D-18 / MapErrorCodeToExitCode).
func vaultErrExit(leaf string, e *mcp.EnvelopeError) error {
	return &cliErrorWithExit{
		code: mcp.MapErrorCodeToExitCode(e.Code),
		msg:  fmt.Sprintf("%s: %s: %s", leaf, e.Code, e.Message),
	}
}

// vaultRenderResult is the shared RunE tail for the envelope-returning vault
// leaves (search/validate/triage-run/get-coverage-report). It wraps a
// transport error, guards a nil envelope, maps an envelope error to an exit
// code, and otherwise renders env.Data via renderCoverageReport honoring --json.
func vaultRenderResult(cmd *cobra.Command, leaf string, env *mcp.Envelope, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", leaf, err)
	}
	if env == nil {
		return fmt.Errorf("%s: nil envelope", leaf)
	}
	if env.Error != nil {
		return vaultErrExit(leaf, env.Error)
	}
	jsonFlag, _ := cmd.Flags().GetBool("json")
	return renderCoverageReport(cmd.OutOrStdout(), env.Data, jsonFlag)
}

// withVaultClient owns the MCP client lifecycle shared by every ws vault leaf:
// NewClient -> InstallSignalForward -> defer stop() -> defer Close(). It
// centralizes the signal-forward install so no leaf can forget it (predict-
// bulk-load did — this is the fix). Generic over the collector's return type T.
func withVaultClient[T any](ctx context.Context, version string, fn func(context.Context, *mcp.Client) (T, error)) (T, error) {
	var zero T
	cl, err := mcp.NewClient(ctx, mcp.Options{
		VaultAIRepoRoot: os.Getenv("VAULT_AI_REPO_ROOT"),
		Version:         version,
	})
	if err != nil {
		return zero, fmt.Errorf("spawn MCP client: %w", err)
	}
	stop := mcp.InstallSignalForward(cl)
	defer stop()
	defer func() { _ = cl.Close(ctx) }()
	return fn(ctx, cl)
}

// callVaultTool is the common case: acquire a client, invoke one MCP tool,
// tear down. Preserves the "MCP roundtrip: %w" transport-error wrap.
func callVaultTool(ctx context.Context, version, tool string, args any) (*mcp.Envelope, error) {
	return withVaultClient(ctx, version, func(ctx context.Context, cl *mcp.Client) (*mcp.Envelope, error) {
		env, err := cl.Call(ctx, tool, args)
		if err != nil {
			return nil, fmt.Errorf("MCP roundtrip: %w", err)
		}
		return env, nil
	})
}

package mcp

// reporoot.go — single source of truth for resolving the vault-ai checkout
// root shared by NewClient (subprocess cmd.Dir + the uv --project flag) and
// the `ws vault doctor` diagnostic leaves. Consolidates a fallback chain that
// was previously re-inlined at three call sites and had drifted: the doctor
// leaves ignored $VAULT_AI_REPO_ROOT entirely, and NewClient left repoRoot=""
// when os.UserHomeDir failed instead of failing closed (CONTEXT D-08).

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolveRepoRoot returns the absolute filesystem path to the vault-ai
// checkout using the documented fallback chain (CONTEXT D-08):
//
//  1. override, when non-empty (Options.VaultAIRepoRoot / an explicit caller arg)
//  2. $VAULT_AI_REPO_ROOT, when non-empty
//  3. <home>/projects/vault-ai
//
// Fail-closed: when the default leg is reached and os.UserHomeDir errors,
// ResolveRepoRoot returns ("", err) rather than an empty root — callers MUST
// surface the failure instead of operating against "" (which the OS would
// resolve relative to the process CWD).
func ResolveRepoRoot(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if env := os.Getenv("VAULT_AI_REPO_ROOT"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve vault-ai repo root: %w", err)
	}
	return filepath.Join(home, "projects", "vault-ai"), nil
}

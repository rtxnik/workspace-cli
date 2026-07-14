package cmd

// vault_reporoot_parity_test.go — equivalence guard for the vault-ai repo-root
// resolver dedup. After resolveBackupVerifyLogDir and the `reindex` inline
// resolution were migrated onto mcp.ResolveRepoRoot, these tests pin that the
// migrated call sites still produce byte-identical paths for the two
// observable cases (env override set, env unset → $HOME default). The status
// resolver's own parity is asserted by TestResolveVaultAIRepoRoot.

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

// TestResolveBackupVerifyLogDirParity: the backup-verify log dir is
// <root>/_tooling/logs for both the env-override and the $HOME-default case,
// where <root> is exactly what the shared resolver returns.
func TestResolveBackupVerifyLogDirParity(t *testing.T) {
	// env-override case: verbatim root, unchanged subpath.
	t.Setenv("VAULT_AI_REPO_ROOT", "/tmp/fake-vault-ai")
	got, err := resolveBackupVerifyLogDir()
	if err != nil {
		t.Fatalf("env-set: unexpected error: %v", err)
	}
	if want := filepath.Join("/tmp/fake-vault-ai", "_tooling", "logs"); got != want {
		t.Errorf("env-set: got %q, want %q", got, want)
	}

	// default case: env unset → shared resolver's $HOME default.
	t.Setenv("VAULT_AI_REPO_ROOT", "")
	root, rerr := mcp.ResolveRepoRoot("")
	if rerr != nil {
		t.Skipf("home dir unavailable in this environment: %v", rerr)
	}
	got, err = resolveBackupVerifyLogDir()
	if err != nil {
		t.Fatalf("default: unexpected error: %v", err)
	}
	if want := filepath.Join(root, "_tooling", "logs"); got != want {
		t.Errorf("default: got %q, want %q", got, want)
	}
}

// TestReindexRepoRootParityDefault drives `ws vault reindex` with env unset and
// captures the shell-out args; invocation[2] (`--project <root>/_tooling/mcp`)
// must carry exactly the shared resolver's $HOME default. The env-set path is
// already pinned by TestVaultReindexShellOut.
func TestReindexRepoRootParityDefault(t *testing.T) {
	t.Setenv("VAULT_AI_REPO_ROOT", "")
	root, rerr := mcp.ResolveRepoRoot("")
	if rerr != nil {
		t.Skipf("home dir unavailable in this environment: %v", rerr)
	}

	orig := runReindexFn
	t.Cleanup(func() { runReindexFn = orig })
	var gotArgs []string
	runReindexFn = func(_ context.Context, _ *cobra.Command, _ string, cmdArgs []string) error {
		gotArgs = cmdArgs
		return nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "reindex"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if len(gotArgs) < 3 {
		t.Fatalf("captured too few args: %v", gotArgs)
	}
	if want := filepath.Join(root, "_tooling", "mcp"); gotArgs[2] != want {
		t.Errorf("default --project: got %q, want %q", gotArgs[2], want)
	}
}

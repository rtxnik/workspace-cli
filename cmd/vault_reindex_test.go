package cmd

// vault_reindex_test.go — unit tests for CLI-05 `ws vault reindex`.
//
// Coverage (per Plan 18-04 Task 1 behavior block):
//   - TestVaultReindexShellOut — mock runReindexFn captures invoked args; assert
//     they match the verified `uv run --project <root>/_tooling/mcp -- python -m
//     vault_ai.cli.embed_index index [paths...]` shape per CONTEXT D-27 §OQ-3
//     Amendment.
//   - TestVaultReindexExitCodePropagates — mock returns subprocess exit 3 →
//     leaf returns cliErrorWithExit{code: 3} (pass-through subprocess exit;
//     NOT MapErrorCodeToExitCode — reindex is not MCP)
//   - TestReindexSubcommandNameConstant — asserts `reindexSubcommandName` is
//     non-empty AND appears in live `embed_index.py` (OQ-3 drift catcher).
//   - TestVaultReindexRegistered — walker finds "reindex" under vault
//
// NOTE on flag mapping (Rule 1 deviation tracked in SUMMARY):
//   The plan's draft <interfaces> proposed `--collection <name>`, but the live
//   `embed_index.py index` subparser exposes positional `paths` (file/dir paths
//   to re-embed) + boolean modifiers (--changed-only, --schema-bump, --type,
//   --all-types, --cache-disabled). There is no --collection argument.
//   Surface flags are therefore positional `[paths...]` + `--changed-only`.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestVaultReindexShellOut(t *testing.T) {
	orig := runReindexFn
	t.Cleanup(func() { runReindexFn = orig })
	var gotName string
	var gotArgs []string
	runReindexFn = func(_ context.Context, _ *cobra.Command, name string, cmdArgs []string) error {
		gotName = name
		gotArgs = cmdArgs
		return nil
	}
	t.Setenv("VAULT_AI_REPO_ROOT", "/tmp/fake-vault-ai")

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "reindex", "30_Resources/foo.md", "--changed-only"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if gotName != "uv" {
		t.Errorf("expected command name 'uv'; got %q", gotName)
	}
	want := []string{
		"run",
		"--project", "/tmp/fake-vault-ai/_tooling/mcp",
		"--",
		"python", "-m", "vault_ai.cli.embed_index",
		reindexSubcommandName,
		"30_Resources/foo.md",
		"--changed-only",
	}
	if len(gotArgs) != len(want) {
		t.Fatalf("arg count mismatch: got %v; want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg[%d]: got %q want %q", i, gotArgs[i], want[i])
		}
	}
}

func TestVaultReindexExitCodePropagates(t *testing.T) {
	orig := runReindexFn
	t.Cleanup(func() { runReindexFn = orig })
	runReindexFn = func(_ context.Context, _ *cobra.Command, _ string, _ []string) error {
		return &cliErrorWithExit{code: 3, msg: "embed_index exited with status 3"}
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

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on subprocess exit 3")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 3 {
		t.Errorf("expected exit code 3 pass-through; got %d", cerr.code)
	}
}

// TestReindexSubcommandNameConstant asserts the package constant
// reindexSubcommandName is byte-identical to a live `embed_index.py`
// subcommand definition. This is the OQ-3 drift catcher: a future
// contributor renaming the Python subparser fails this test on next CI run.
func TestReindexSubcommandNameConstant(t *testing.T) {
	if reindexSubcommandName == "" {
		t.Fatal("reindexSubcommandName must be non-empty (CONTEXT D-27 §OQ-3 Amendment)")
	}

	root := os.Getenv("VAULT_AI_REPO_ROOT")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("UserHomeDir failed: %v", err)
		}
		root = filepath.Join(home, "projects", "vault-ai")
	}
	embedPath := filepath.Join(root, "_tooling", "mcp", "vault_ai", "cli", "embed_index.py")
	body, err := os.ReadFile(embedPath)
	if err != nil {
		t.Skipf("embed_index.py not readable at %s: %v (Skip rather than fail — gate runs in environments without vault-ai checkout)", embedPath, err)
	}
	// Match either `sub.add_parser("<name>"` or `def cmd_<name>` style.
	addParserMarker := `add_parser("` + reindexSubcommandName + `"`
	cmdFuncMarker := `def cmd_` + reindexSubcommandName + `(`
	if !strings.Contains(string(body), addParserMarker) && !strings.Contains(string(body), cmdFuncMarker) {
		t.Errorf("reindexSubcommandName=%q absent from live embed_index.py (looked for %q or %q) — OQ-3 drift: rename the constant or fix the Python source",
			reindexSubcommandName, addParserMarker, cmdFuncMarker)
	}
}

func TestVaultReindexRegistered(t *testing.T) {
	if !findVaultLeaf(t, "reindex") {
		t.Fatal("`ws vault reindex` not registered as a subcommand of `ws vault`")
	}
}

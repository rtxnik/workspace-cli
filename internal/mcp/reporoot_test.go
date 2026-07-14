package mcp

// reporoot_test.go — unit spec-lock for ResolveRepoRoot's fallback chain
// (override -> $VAULT_AI_REPO_ROOT -> <home>/projects/vault-ai), plus the
// fail-closed contract on home-resolution error.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveRepoRootOverrideWins — a non-empty override takes precedence even
// when $VAULT_AI_REPO_ROOT is set to a different value.
func TestResolveRepoRootOverrideWins(t *testing.T) {
	t.Setenv("VAULT_AI_REPO_ROOT", "/env/should/lose")
	got, err := ResolveRepoRoot("/explicit/override")
	if err != nil {
		t.Fatalf("override path must not error; got %v", err)
	}
	if got != "/explicit/override" {
		t.Errorf("override must win; got %q want %q", got, "/explicit/override")
	}
}

// TestResolveRepoRootEnvWins — with an empty override, $VAULT_AI_REPO_ROOT is
// used verbatim.
func TestResolveRepoRootEnvWins(t *testing.T) {
	t.Setenv("VAULT_AI_REPO_ROOT", "/tmp/env-root")
	got, err := ResolveRepoRoot("")
	if err != nil {
		t.Fatalf("env path must not error; got %v", err)
	}
	if got != "/tmp/env-root" {
		t.Errorf("env must win when override empty; got %q want %q", got, "/tmp/env-root")
	}
}

// TestResolveRepoRootDefault — empty override + unset env falls back to
// <home>/projects/vault-ai. Also asserts the happy-path fail-closed contract:
// a non-empty root with a nil error. (The home-error leg returns ("", err) by
// construction; os.UserHomeDir is not mockable here, so we document that leg
// and assert the reachable happy path instead.)
func TestResolveRepoRootDefault(t *testing.T) {
	t.Setenv("VAULT_AI_REPO_ROOT", "") // empty is treated as unset by the resolver
	home, herr := os.UserHomeDir()
	if herr != nil {
		t.Skipf("cannot resolve home dir in this test environment: %v", herr)
	}
	want := filepath.Join(home, "projects", "vault-ai")

	got, err := ResolveRepoRoot("")
	if err != nil {
		t.Fatalf("default path must not error on a host with a resolvable home; got %v", err)
	}
	if got == "" {
		t.Fatal("default path must never return an empty root (fail-closed contract)")
	}
	if got != want {
		t.Errorf("default fallback mismatch; got %q want %q", got, want)
	}
}

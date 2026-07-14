package cmd

// vault_doctor_reporoot_test.go — regression proof for the doctor
// split-diagnosis bug: the stale-lock and xrepo-drift checks must resolve the
// vault-ai root through the shared resolver so $VAULT_AI_REPO_ROOT is honored,
// instead of hardcoding <home>/projects/vault-ai.

import (
	"context"
	"strings"
	"testing"
)

// TestVaultDoctorStaleLockHonorsRepoRootEnv — with VAULT_AI_REPO_ROOT pointing
// at a non-existent synthetic root, checkStaleLocksImpl walks that root's
// _tooling/state (absent -> green) and the diagnostic Detail names the
// env-derived path. Before the fix the check ignored the env and named
// <home>/projects/vault-ai instead, so this assertion failed.
func TestVaultDoctorStaleLockHonorsRepoRootEnv(t *testing.T) {
	t.Setenv("VAULT_AI_REPO_ROOT", "/tmp/ws-doctor-root")

	check := checkStaleLocksImpl(context.Background())
	if check == nil {
		t.Fatal("checkStaleLocksImpl returned nil")
	}

	const want = "/tmp/ws-doctor-root/_tooling/state"
	if !strings.Contains(check.Detail, want) {
		t.Errorf(
			"stale-lock check must resolve the state dir under $VAULT_AI_REPO_ROOT; "+
				"Detail=%q does not contain %q",
			check.Detail, want,
		)
	}
}

package cmd

// vault_triage_run_test.go — unit tests for CLI-03 `ws vault triage-run`.
//
// Coverage (per Plan 18-04 Task 1 behavior block):
//   - TestVaultTriageRunHappyPath — mock client.Call returns success; --dry-run
//     propagates as DryRun: true
//   - TestVaultTriageRunSessionAndLimit — --session-id + --limit flags propagate
//   - TestVaultTriageRunEnvelopeError — VALIDATION_FAILED envelope → cliErrorWithExit{code: 1}
//   - TestVaultTriageRunRegistered — walker finds "triage-run" under vault
//
// NOTE on flag mapping (Rule 1 deviation tracked in SUMMARY):
//   The plan's draft <interfaces> block proposed an --inbox <path> flag, but
//   tools.json v1.3.0 triage_run input schema exposes only {session_id, limit,
//   dry_run, no_batch} — there is no inbox_path argument. Surface flags are
//   therefore --session-id, --limit, --dry-run, --no-batch (1:1 with contract).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

func TestVaultTriageRunHappyPath(t *testing.T) {
	orig := vaultTriageRunCallFn
	t.Cleanup(func() { vaultTriageRunCallFn = orig })
	var gotDryRun bool
	vaultTriageRunCallFn = func(_ context.Context, _ *cobra.Command, args mcp.TriageRunArgs) (*mcp.Envelope, error) {
		gotDryRun = args.DryRun
		return &mcp.Envelope{
			OK:   true,
			Data: json.RawMessage(`{"session_id":"s-1","processed":3,"proposals":[{"id":"a"},{"id":"b"}]}`),
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "triage-run", "--dry-run"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if !gotDryRun {
		t.Errorf("--dry-run did not propagate as DryRun: true")
	}
	if !strings.Contains(out.String(), `"session_id"`) {
		t.Errorf("expected stdout to contain envelope.Data; got %q", out.String())
	}
}

func TestVaultTriageRunSessionAndLimit(t *testing.T) {
	orig := vaultTriageRunCallFn
	t.Cleanup(func() { vaultTriageRunCallFn = orig })
	var gotSession string
	var gotLimit int
	var gotNoBatch bool
	vaultTriageRunCallFn = func(_ context.Context, _ *cobra.Command, args mcp.TriageRunArgs) (*mcp.Envelope, error) {
		gotSession = args.SessionId
		gotLimit = args.Limit
		gotNoBatch = args.NoBatch
		return &mcp.Envelope{OK: true, Data: json.RawMessage(`{}`)}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "triage-run", "--session-id", "ops-2026-05-18", "--limit", "20", "--no-batch"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if gotSession != "ops-2026-05-18" {
		t.Errorf("expected session_id 'ops-2026-05-18'; got %q", gotSession)
	}
	if gotLimit != 20 {
		t.Errorf("expected limit 20; got %d", gotLimit)
	}
	if !gotNoBatch {
		t.Errorf("expected no_batch true; got false")
	}
}

func TestVaultTriageRunEnvelopeError(t *testing.T) {
	orig := vaultTriageRunCallFn
	t.Cleanup(func() { vaultTriageRunCallFn = orig })
	vaultTriageRunCallFn = func(_ context.Context, _ *cobra.Command, _ mcp.TriageRunArgs) (*mcp.Envelope, error) {
		return &mcp.Envelope{
			OK: false,
			Error: &mcp.EnvelopeError{
				Code:    "VALIDATION_FAILED",
				Message: "limit out of range",
			},
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "triage-run"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on VALIDATION_FAILED envelope")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 1 {
		t.Errorf("expected exit 1 (VALIDATION_FAILED); got %d", cerr.code)
	}
	if !strings.Contains(cerr.msg, "VALIDATION_FAILED") {
		t.Errorf("expected msg to cite code; got %q", cerr.msg)
	}
}

func TestVaultTriageRunRegistered(t *testing.T) {
	if !findVaultLeaf(t, "triage-run") {
		t.Fatal("`ws vault triage-run` not registered as a subcommand of `ws vault`")
	}
}

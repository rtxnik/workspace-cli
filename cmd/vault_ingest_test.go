package cmd

// vault_ingest_test.go — unit tests for CLI-07 `ws vault ingest`.
//
// Coverage (per Plan 18-04 Task 2 behavior block):
//   - TestVaultIngestHappyPath — mock client.Call returns success envelope;
//     exit 0; stdout shows envelope.Data
//   - TestVaultIngestDedupBlockedNoForce — mock returns DEDUP_BLOCKED envelope;
//     assert exit 6
//   - TestVaultIngestDedupForceRequiresYes — --dedup-force without --yes
//     triggers Confirm gate; when Confirm returns false → exit 1 + Aborted,
//     create_note NOT called (operator-controlled-mutation discipline per
//     Phase 22 D-09/D-10 + memory feedback_no_auto_state_mutation)
//   - TestVaultIngestDedupForceWithYes — --dedup-force --yes skips Confirm;
//     create_note called with ConfirmDedupOverride=true + Reason populated
//   - TestVaultIngestDedupForceRequiresReason — --dedup-force without --reason
//     errors before any MCP call
//   - TestVaultIngestRegistered — walker finds "ingest" under vault
//
// NOTE on field mapping (Rule 1 deviation tracked in SUMMARY):
//   The MCP create_note tool exposes `confirm_dedup_override` (bool) + `reason`
//   (string); the plan draft <interfaces> proposed DedupForce + DedupOverrideReason
//   field names that do not exist on the generated CreateNoteArgs struct.
//   The user-facing CLI flag is --dedup-force (the Phase 17 / D-24 operator
//   vocabulary) but it maps to args.ConfirmDedupOverride at call time. The
//   --reason flag maps directly to args.Reason. Also: Path is NOT a contract
//   field — required contract fields are {Type, Frontmatter, Body, Zone};
//   the ingest leaf parses these out of the operator's markdown file (YAML
//   frontmatter + body split).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// writeTempIngestFile creates a YAML-frontmatter + body markdown fixture in
// t.TempDir() and returns the absolute path. Mirrors the minimum-viable
// shape that create_note demands.
func writeTempIngestFile(t *testing.T, body string) string {
	t.Helper()
	if body == "" {
		body = `---
type: zettel
zone: 30_Resources
title: Test Zettel
---

# Test body

A small ingested note.
`
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "test-note.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func TestVaultIngestHappyPath(t *testing.T) {
	origCall := vaultIngestCallFn
	t.Cleanup(func() { vaultIngestCallFn = origCall })
	var gotArgs mcp.CreateNoteArgs
	vaultIngestCallFn = func(_ context.Context, _ *cobra.Command, args mcp.CreateNoteArgs) (*mcp.Envelope, error) {
		gotArgs = args
		return &mcp.Envelope{
			OK:   true,
			Data: json.RawMessage(`{"id":"zettel-20260518-test","path":"30_Resources/zettel-20260518-test.md"}`),
		}, nil
	}
	path := writeTempIngestFile(t, "")

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "ingest", path})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		// Reset ingest flags so subsequent tests in the package see clean
		// state (rootCmd is a package-level singleton — Cobra flag values
		// persist across rootCmd.Execute() calls).
		resetVaultIngestFlags(t)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if !gotArgs.CheckDedupBeforeCreate {
		t.Errorf("CheckDedupBeforeCreate must default true per CONTEXT D-24")
	}
	if gotArgs.Type == "" {
		t.Errorf("Type must be parsed from frontmatter; got empty")
	}
	if gotArgs.Body == "" {
		t.Errorf("Body must be parsed; got empty")
	}
	if gotArgs.ConfirmDedupOverride {
		t.Errorf("ConfirmDedupOverride must be false on default ingest")
	}
}

func TestVaultIngestDedupBlockedNoForce(t *testing.T) {
	origCall := vaultIngestCallFn
	t.Cleanup(func() { vaultIngestCallFn = origCall })
	vaultIngestCallFn = func(_ context.Context, _ *cobra.Command, _ mcp.CreateNoteArgs) (*mcp.Envelope, error) {
		return &mcp.Envelope{
			OK: false,
			Error: &mcp.EnvelopeError{
				Code:    "DEDUP_BLOCKED",
				Message: "blocked: 0.92 similar to zettel-20260101-prior",
				Details: json.RawMessage(`{"top_match_id":"zettel-20260101-prior","top_match_score":0.92,"block_threshold":0.85}`),
			},
		}, nil
	}
	path := writeTempIngestFile(t, "")

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "ingest", path})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		// Reset ingest flags so subsequent tests in the package see clean
		// state (rootCmd is a package-level singleton — Cobra flag values
		// persist across rootCmd.Execute() calls).
		resetVaultIngestFlags(t)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on DEDUP_BLOCKED")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 6 {
		t.Errorf("expected exit 6 (DEDUP_BLOCKED per ADR-int-03 exit table); got %d", cerr.code)
	}
	if !strings.Contains(cerr.msg, "DEDUP_BLOCKED") {
		t.Errorf("expected msg to cite DEDUP_BLOCKED; got %q", cerr.msg)
	}
	// Details must surface to stderr so operator sees existing-id +
	// similarity score per CONTEXT D-24.
	if !strings.Contains(errOut.String(), "zettel-20260101-prior") {
		t.Errorf("expected envelope.error.details on stderr; got %q", errOut.String())
	}
}

func TestVaultIngestDedupForceRequiresYes(t *testing.T) {
	origCall := vaultIngestCallFn
	origConfirm := vaultIngestConfirmFn
	t.Cleanup(func() {
		vaultIngestCallFn = origCall
		vaultIngestConfirmFn = origConfirm
	})
	callCount := 0
	vaultIngestCallFn = func(_ context.Context, _ *cobra.Command, _ mcp.CreateNoteArgs) (*mcp.Envelope, error) {
		callCount++
		return &mcp.Envelope{OK: true, Data: json.RawMessage(`{}`)}, nil
	}
	vaultIngestConfirmFn = func(_, _ string) bool { return false } // operator declines
	path := writeTempIngestFile(t, "")

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "ingest", path, "--dedup-force", "--reason", "operator test override"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		// Reset ingest flags so subsequent tests in the package see clean
		// state (rootCmd is a package-level singleton — Cobra flag values
		// persist across rootCmd.Execute() calls).
		resetVaultIngestFlags(t)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit when operator declines Confirm prompt")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 1 {
		t.Errorf("expected exit 1 on Aborted; got %d", cerr.code)
	}
	if callCount != 0 {
		t.Errorf("create_note must NOT be called when Confirm denies (got %d calls)", callCount)
	}
	if !strings.Contains(strings.ToLower(cerr.msg+errOut.String()), "abort") {
		t.Errorf("expected 'aborted' in diagnostic; got msg=%q stderr=%q", cerr.msg, errOut.String())
	}
}

func TestVaultIngestDedupForceWithYes(t *testing.T) {
	origCall := vaultIngestCallFn
	origConfirm := vaultIngestConfirmFn
	t.Cleanup(func() {
		vaultIngestCallFn = origCall
		vaultIngestConfirmFn = origConfirm
	})
	confirmCount := 0
	vaultIngestConfirmFn = func(_, _ string) bool {
		confirmCount++
		return true // should NOT be invoked when --yes is supplied
	}
	var gotArgs mcp.CreateNoteArgs
	vaultIngestCallFn = func(_ context.Context, _ *cobra.Command, args mcp.CreateNoteArgs) (*mcp.Envelope, error) {
		gotArgs = args
		return &mcp.Envelope{
			OK:   true,
			Data: json.RawMessage(`{"id":"zettel-test-override"}`),
		}, nil
	}
	path := writeTempIngestFile(t, "")

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "ingest", path, "--dedup-force", "--yes", "--reason", "operator override audit row test"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		// Reset ingest flags so subsequent tests in the package see clean
		// state (rootCmd is a package-level singleton — Cobra flag values
		// persist across rootCmd.Execute() calls).
		resetVaultIngestFlags(t)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if confirmCount != 0 {
		t.Errorf("--yes must skip Confirm; got %d invocations", confirmCount)
	}
	if !gotArgs.ConfirmDedupOverride {
		t.Errorf("ConfirmDedupOverride must be true when --dedup-force set")
	}
	if gotArgs.Reason != "operator override audit row test" {
		t.Errorf("Reason must propagate from --reason; got %q", gotArgs.Reason)
	}
}

func TestVaultIngestDedupForceRequiresReason(t *testing.T) {
	origCall := vaultIngestCallFn
	t.Cleanup(func() { vaultIngestCallFn = origCall })
	callCount := 0
	vaultIngestCallFn = func(_ context.Context, _ *cobra.Command, _ mcp.CreateNoteArgs) (*mcp.Envelope, error) {
		callCount++
		return &mcp.Envelope{OK: true}, nil
	}
	path := writeTempIngestFile(t, "")

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "ingest", path, "--dedup-force", "--yes"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		// Reset ingest flags so subsequent tests in the package see clean
		// state (rootCmd is a package-level singleton — Cobra flag values
		// persist across rootCmd.Execute() calls).
		resetVaultIngestFlags(t)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --dedup-force has no --reason")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 1 {
		t.Errorf("expected exit 1 (validation); got %d", cerr.code)
	}
	if callCount != 0 {
		t.Errorf("create_note must NOT be called when --reason missing (got %d calls)", callCount)
	}
	if !strings.Contains(cerr.msg, "--reason") {
		t.Errorf("expected msg to cite --reason; got %q", cerr.msg)
	}
}

func TestVaultIngestRegistered(t *testing.T) {
	if !findVaultLeaf(t, "ingest") {
		t.Fatal("`ws vault ingest` not registered as a subcommand of `ws vault`")
	}
}

// resetVaultIngestFlags walks rootCmd → vault → ingest and resets each Cobra
// Flag to its Default value. Cobra's persistent flag-storage layer keeps
// values across rootCmd.Execute() calls (because rootCmd is a package-level
// singleton); without this helper, a `--reason "..."` set by test T1 leaks
// into test T2 even with rootCmd.SetArgs(nil) cleanup.
func resetVaultIngestFlags(t *testing.T) {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() != "vault" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() != "ingest" {
				continue
			}
			sub.Flags().VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
		}
	}
}

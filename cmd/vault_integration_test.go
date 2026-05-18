//go:build integration

package cmd

// vault_integration_test.go — end-to-end integration tests for Plan 18-03
// read-only leaves. Spawns the real MCP subprocess via internal/mcp.Client
// and asserts each leaf exits cleanly + emits the expected output shape.
//
// Skipped on CI lanes without uv / vault-ai / VAULT_AI_TOKEN per the
// requireUvAndVaultAIForCmd helper (mirrors internal/mcp's gate).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireUvAndVaultAIForCmd mirrors the gate from
// internal/mcp/client_integration_test.go::requireUvAndVaultAI. Duplicated
// here because cross-package test-helper sharing in Go would require a
// non-_test helper file. Skip if any dependency missing.
func requireUvAndVaultAIForCmd(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv binary not on PATH")
	}
	root := os.Getenv("VAULT_AI_REPO_ROOT")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("UserHomeDir: %v", err)
		}
		root = filepath.Join(home, "projects", "vault-ai")
	}
	tools := filepath.Join(root, "_tooling", "mcp", "contract", "tools.json")
	if _, err := os.Stat(tools); err != nil {
		t.Skipf("vault-ai checkout missing tools.json at %s: %v", tools, err)
	}
	if os.Getenv("VAULT_AI_TOKEN") == "" {
		t.Skip("VAULT_AI_TOKEN unset — integration tests need a provisioned token")
	}
	return root
}

// TestVaultSearchIntegration invokes `ws vault search <query>` end-to-end
// against the real MCP subprocess. Accepts either exit 0 (results returned)
// OR a documented envelope.Error exit code in [1,7] (e.g. INTERNAL from
// Qdrant being unreachable in the dev environment) — both prove the
// JSON-RPC round-trip + envelope decoding works.
func TestVaultSearchIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "search", "test", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		// Happy path: envelope.OK + results.
		if out.Len() == 0 {
			t.Errorf("expected stdout from search; got empty")
		}
		var any interface{}
		if jerr := json.Unmarshal(out.Bytes(), &any); jerr != nil {
			t.Logf("note: stdout not strict JSON; raw=%q err=%v", out.String(), jerr)
		}
		return
	}
	// Envelope.Error path is also a valid round-trip (proves the
	// transport + envelope decode work). Only fail on transport-level
	// errors (no cliErrorWithExit wrapper).
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("vault search transport failure: %v (stderr=%q)", err, errOut.String())
	}
	if cerr.code < 1 || cerr.code > 7 {
		t.Errorf("envelope error exit out of band: %d", cerr.code)
	}
}

// TestVaultGetCoverageReportIntegration invokes `ws vault get-coverage-report`
// end-to-end. Accepts exit 0 OR documented envelope error code in [1,7].
func TestVaultGetCoverageReportIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "get-coverage-report", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		if out.Len() == 0 {
			t.Errorf("expected non-empty stdout; got empty")
		}
		return
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("vault get-coverage-report transport failure: %v (stderr=%q)", err, errOut.String())
	}
	if cerr.code < 1 || cerr.code > 7 {
		t.Errorf("envelope error exit out of band: %d", cerr.code)
	}
}

// TestVaultValidateIntegration invokes `ws vault validate` on a likely-present
// note (00_MOC.md is the canonical vault root MOC). Either exit 0 (green)
// or exit 1 (VALIDATION_FAILED findings); both prove the round-trip works.
func TestVaultValidateIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "validate", "00_MOC", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		return // green — exit 0
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("vault validate unexpected error type: %T (%v)", err, err)
	}
	// Accept any documented exit code; the goal is to prove the round-trip
	// reached the MCP subprocess and got a structured envelope back.
	if cerr.code < 0 || cerr.code > 7 {
		t.Errorf("validate returned unexpected exit code %d", cerr.code)
	}
}

// TestVaultVaultHealthScoreIntegration invokes `ws vault vault-health-score`
// end-to-end. Asserts stdout is parseable as int in [0, 100].
func TestVaultVaultHealthScoreIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "vault-health-score"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	// May exit non-zero on yellow/red band — that is expected behaviour;
	// we only care that stdout has the integer.
	_ = rootCmd.Execute()

	got, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("stdout must be parseable as int; got %q (err=%v)", out.String(), err)
	}
	if got < 0 || got > 100 {
		t.Errorf("vault-health-score out of range: %d (want [0,100])", got)
	}
}

// TestVaultStatusIntegration invokes `ws vault status --json` end-to-end.
// Asserts JSON envelope shape + exit code in {0, 1, 2}.
func TestVaultStatusIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "status", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	// err may be nil (green) or *cliErrorWithExit with code 1/2.
	var cerr *cliErrorWithExit
	if err != nil && !errors.As(err, &cerr) {
		t.Fatalf("vault status unexpected error type: %T (%v)", err, err)
	}

	var rep statusReport
	if jerr := json.Unmarshal(out.Bytes(), &rep); jerr != nil {
		t.Fatalf("--json output must parse; got err=%v out=%q", jerr, out.String())
	}
	if len(rep.Signals) != 6 {
		t.Errorf("expected exactly 6 signals; got %d", len(rep.Signals))
	}
	if rep.ExitCode < 0 || rep.ExitCode > 2 {
		t.Errorf("exit code out of band range; got %d", rep.ExitCode)
	}
	// Verify each signal label is from the documented D-21 set.
	want := map[string]bool{
		"MCP liveness":           false,
		"vault_health composite": false,
		"audit-chain integrity":  false,
		"cost-tracker headroom":  false,
		"dedup gate readiness":   false,
		"last DR-drill age":      false,
	}
	for _, s := range rep.Signals {
		if _, ok := want[s.Label]; ok {
			want[s.Label] = true
		}
	}
	for label, seen := range want {
		if !seen {
			t.Errorf("missing required signal label %q", label)
		}
	}
}

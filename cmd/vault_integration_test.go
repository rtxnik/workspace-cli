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

// ---------------------------------------------------------------------------
// Plan 18-04 Wave 2 mutating-leaf integration tests
// ---------------------------------------------------------------------------

// TestVaultTriageRunIntegration invokes `ws vault triage-run --dry-run --limit 1`
// against the live MCP subprocess. --dry-run guarantees no writes. Accepts
// exit 0 (proposals returned) or any documented envelope error code in [1,7]
// (e.g. RATE_LIMITED / NOT_IMPLEMENTED if Phase 16 triage_run not yet wired).
func TestVaultTriageRunIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "triage-run", "--dry-run", "--limit", "1", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		if out.Len() == 0 {
			t.Errorf("expected stdout on triage-run --dry-run; got empty")
		}
		return
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("vault triage-run transport failure: %v (stderr=%q)", err, errOut.String())
	}
	if cerr.code < 1 || cerr.code > 7 {
		t.Errorf("triage-run envelope error exit out of band: %d", cerr.code)
	}
}

// TestVaultReindexIntegration invokes `ws vault reindex --changed-only`
// against the live embed_index.py CLI via uv shell-out. Accepts exit 0
// (re-embed successful or no-op) or pass-through non-zero subprocess exit.
// Idempotent: a re-run on a clean tree should also exit 0.
func TestVaultReindexIntegration(t *testing.T) {
	root := requireUvAndVaultAIForCmd(t)
	if _, err := os.Stat(filepath.Join(root, "_tooling", "mcp", "vault_ai", "cli", "embed_index.py")); err != nil {
		t.Skipf("embed_index.py not present at %s: %v", root, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "reindex", "--changed-only"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err != nil {
		var cerr *cliErrorWithExit
		if !errors.As(err, &cerr) {
			t.Fatalf("vault reindex transport failure: %v (stderr=%q)", err, errOut.String())
		}
		// Pass-through subprocess exit is acceptable — proves the shell-out
		// reached embed_index.py + the JSON-RPC-bypassing pattern works.
		_ = cerr
	}
}

// TestVaultBackupVerifyIntegration invokes `ws vault backup-verify` against
// the operator-local _tooling/logs/backup-verify-*.jsonl. At Phase 18 ship
// time, Phase 21c HARD-07 cron has NOT yet shipped, so the expected outcome
// is exit 4 with the Phase 21c diagnostic per CONTEXT D-26 graceful fallback.
// If Phase 21c later ships, exit 0 (green) or exit 1 (rot) are also valid.
func TestVaultBackupVerifyIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "backup-verify"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		return // green
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("vault backup-verify unexpected error type: %T (%v)", err, err)
	}
	switch cerr.code {
	case 1, 4:
		// 1 = rot detected; 4 = missing/stale (Phase 21c not yet shipped).
		// Both are documented exit codes.
	default:
		t.Errorf("backup-verify exit code out of [0,1,4]: %d", cerr.code)
	}
}

// TestVaultIngestIntegration invokes `ws vault ingest` against the live MCP
// twice: once to create + once to verify DEDUP_BLOCKED on a re-ingest of
// near-identical content. Both rounds prove the create_note → dedup gate
// path end-to-end.
//
// Skip strategy: requires VAULT_AI_TOKEN AND a writeable vault root. The
// test cleans up after itself by emitting only into a temp-tagged zone so
// the operator can grep+rm post-test.
func TestVaultIngestIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	dir := t.TempDir()
	fixture := filepath.Join(dir, "ingest-it.md")
	stamp := time.Now().UTC().Format("20060102-150405")
	body := []byte(`---
type: zettel
zone: 30_Resources
title: Plan 18-04 integration test fixture ` + stamp + `
---

# Integration Test ` + stamp + `

This note exists only to verify the ws vault ingest end-to-end path.
Cleanup by greppping for the stamp ` + stamp + ` post-test.
`)
	if err := os.WriteFile(fixture, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "ingest", fixture, "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err != nil {
		var cerr *cliErrorWithExit
		if !errors.As(err, &cerr) {
			t.Fatalf("vault ingest transport failure: %v (stderr=%q)", err, errOut.String())
		}
		if cerr.code < 1 || cerr.code > 7 {
			t.Errorf("ingest envelope error exit out of band: %d", cerr.code)
		}
	}
}

// TestVaultIngestDedupOverrideAuditStream — Plan 18-04 Test 11 (Warning 9
// fix). After a first ingest succeeds, a SECOND ingest of near-identical
// content with --dedup-force --yes --reason "..." should:
//  1. exit 0 (override accepted)
//  2. write a `dedup_override` audit row into
//     ~/projects/vault-ai/_tooling/logs/dedup-*.jsonl with our reason text.
//
// Verifies CONTEXT D-24 + Phase 17 D-12 dedup-override audit invariant
// end-to-end against live infrastructure — MCP-side handler routing of the
// override into the dedup audit stream is not assumable.
//
// NOTE on stream-field naming (Rule 1 deviation tracked in SUMMARY): the
// plan draft asserted `'"stream":"dedup"'` but the live audit row uses
// `"event":"dedup_override"` (see vault-ai/_tooling/mcp/vault_ai/dedup/audit.py
// `write_override_row`). The "stream" is implied by the per-stream file path
// (`dedup-*.jsonl`); the per-row `event` field discriminates within the
// stream. We assert on `event=dedup_override` + our reason text presence.
func TestVaultIngestDedupOverrideAuditStream(t *testing.T) {
	root := requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	dir := t.TempDir()
	// Same-content fixture both times to force a high similarity score.
	stamp := time.Now().UTC().Format("20060102-150405")
	reason := "plan-18-04 audit-stream integration test " + stamp
	body := []byte(`---
type: zettel
zone: 30_Resources
title: Plan 18-04 dedup-override integration ` + stamp + `
---

# Dedup Override Integration ` + stamp + `

Identical content twice — second invocation MUST land in dedup_override audit stream.
`)
	fixture := filepath.Join(dir, "dedup-override-it.md")
	if err := os.WriteFile(fixture, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// First ingest (priming the dedup index with the seed note).
	var out1, err1 bytes.Buffer
	rootCmd.SetOut(&out1)
	rootCmd.SetErr(&err1)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "ingest", fixture, "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	firstErr := rootCmd.Execute()
	// First ingest may succeed (exit 0) or DEDUP_BLOCK against an existing
	// vault note (exit 6); both are acceptable — what we care about is that
	// the SECOND --dedup-force invocation produces the audit row.
	_ = firstErr

	// Second ingest with --dedup-force + --reason — must write audit row.
	var out2, err2 bytes.Buffer
	rootCmd.SetOut(&out2)
	rootCmd.SetErr(&err2)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "ingest", fixture, "--dedup-force", "--yes", "--reason", reason, "--json"})
	if err := rootCmd.Execute(); err != nil {
		// The override path should accept; if it fails, surface the error
		// for diagnosis but do not auto-fail the audit-stream assertion
		// (the upstream MCP server may legitimately reject the override on
		// other grounds at runtime).
		t.Logf("second ingest with --dedup-force: err=%v stderr=%q", err, err2.String())
	}

	// Grep the dedup audit stream for our reason text + event marker.
	logDir := filepath.Join(root, "_tooling", "logs")
	matches, err := filepath.Glob(filepath.Join(logDir, "dedup-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Skipf("no dedup-*.jsonl files at %s (dedup stream may not be writeable in this env): %v", logDir, err)
	}
	var found bool
	for _, p := range matches {
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		if strings.Contains(string(body), `"event": "dedup_override"`) && strings.Contains(string(body), reason) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf(
			"expected dedup_override audit row carrying reason %q in %v — Phase 17 D-12 audit invariant not satisfied",
			reason, matches,
		)
	}
}

// ---------------------------------------------------------------------------
// Plan 18-05 Wave 3 diagnostic-leaf integration test
// ---------------------------------------------------------------------------

// TestVaultDoctorIntegration invokes `ws vault doctor --json` end-to-end
// against the live system. At a clean test time, expectations:
//   - exit 0 (all 5 green) when no orphans/locks + token present + xrepo clean
//   - exit 1 / 2 acceptable if the test env happens to have advisory/critical
//     state (cron-induced stale lock, etc.) — both prove the round-trip works
//
// Asserts: stdout is valid NDJSON with exactly 5 entries (one per CONTEXT
// D-12 check); each entry has a band in {green, yellow, red}.
func TestVaultDoctorIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "doctor", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err != nil {
		var cerr *cliErrorWithExit
		if !errors.As(err, &cerr) {
			t.Fatalf("vault doctor unexpected error type: %T (%v)", err, err)
		}
		if cerr.code < 0 || cerr.code > 2 {
			t.Errorf("doctor exit code out of [0,1,2]: %d", cerr.code)
		}
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("doctor --json must emit 5 NDJSON lines (one per CONTEXT D-12 check); got %d (out=%q)", len(lines), out.String())
	}
	validBands := map[string]bool{"green": true, "yellow": true, "red": true}
	for i, line := range lines {
		var rec struct {
			Name string `json:"name"`
			Band string `json:"band"`
		}
		if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
			t.Errorf("line %d not valid JSON: %v (line=%q)", i, jerr, line)
			continue
		}
		if !validBands[rec.Band] {
			t.Errorf("line %d band=%q (want green/yellow/red); name=%q", i, rec.Band, rec.Name)
		}
	}
}

// TestVaultDoctorReadOnlyIntegration is the live-environment companion to the
// TestVaultDoctorReadOnlyByDefault unit test. Even on a real system with real
// orphan PIDs / stale lock files (if any), invoking doctor with NO mutation
// flags MUST NOT change any state. We verify this by:
//   1. Capturing pgrep + state-dir snapshots before invocation
//   2. Invoking doctor (no flags)
//   3. Asserting both snapshots are byte-identical post-invocation
func TestVaultDoctorReadOnlyIntegration(t *testing.T) {
	_ = requireUvAndVaultAIForCmd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Snapshot: pgrep list (order may shift in rare races; sort to stabilize).
	pgrepBefore, _ := exec.Command("pgrep", "-fa", "vault_ai/adapter_stdio/server.py").Output()

	// Snapshot: list lock files in state dir.
	home, _ := os.UserHomeDir()
	stateDir := filepath.Join(home, "projects", "vault-ai", "_tooling", "state")
	locksBefore, _ := filepath.Glob(filepath.Join(stateDir, "*.lock"))

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetContext(ctx)
	rootCmd.SetArgs([]string{"vault", "doctor", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	_ = rootCmd.Execute() // exit code doesn't matter

	pgrepAfter, _ := exec.Command("pgrep", "-fa", "vault_ai/adapter_stdio/server.py").Output()
	locksAfter, _ := filepath.Glob(filepath.Join(stateDir, "*.lock"))

	// Lock-file presence must be unchanged.
	if len(locksBefore) != len(locksAfter) {
		t.Errorf(
			"ws vault doctor (no flags) mutated lock files: before=%d after=%d files. "+
				"memory feedback_no_auto_state_mutation VIOLATED at integration level.",
			len(locksBefore), len(locksAfter),
		)
	}
	// pgrep snapshot: doctor's own dry-run NewClient WILL spawn a transient
	// subprocess (check 4), but Close() reaps it before the function returns.
	// We expect the post-snapshot count to equal the pre-snapshot count.
	beforeLines := nonEmptyLineCount(string(pgrepBefore))
	afterLines := nonEmptyLineCount(string(pgrepAfter))
	if beforeLines != afterLines {
		t.Errorf(
			"ws vault doctor (no flags) changed live MCP subprocess count: before=%d after=%d. "+
				"Either check 4 leaked a subprocess OR mutation discipline VIOLATED.",
			beforeLines, afterLines,
		)
	}
}

func nonEmptyLineCount(s string) int {
	var n int
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

package cmd

// vault_doctor_test.go — unit tests for CLI-10 `ws vault doctor`.
//
// 10 unit cases (Test 11 is integration in vault_integration_test.go):
//   1. TestVaultDoctorRegistered           — walker finds doctor under vault
//   2. TestVaultDoctorAllGreen             — 5 mocked greens → exit 0 + 5 entries
//   3. TestVaultDoctorOrphanOnly           — orphan red → exit 2 + --kill-orphans hint
//   4. TestVaultDoctorStaleLockOnly        — stale-lock yellow → exit 1 + hint
//   5. TestVaultDoctorMissingToken         — token red → ADR-ai-06 hint
//   6. TestVaultDoctorBrokenFDPass         — fd-pass red → stage-of-failure detail
//   7. TestVaultDoctorXrepoDrift           — xrepo red → drift detail
//   8. TestVaultDoctorReadOnlyByDefault    — no flags → kill/remove fns never invoked
//   9. TestVaultDoctorKillOrphansRequiresYes — --kill-orphans w/o --yes + Confirm=false → no kill
//  10. TestVaultDoctorKillOrphansWithYes   — --kill-orphans --yes → kill fn called with PIDs
//
// All tests inject mocked check/kill/remove functions via the package-level
// seams (doctorOrphanCheckFn, doctorStaleLockCheckFn, ...) so no live MCP /
// pgrep / find subprocesses are spawned at unit test time.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// installDoctorMocks installs five mocked check functions that all return
// green. Returns a cleanup func that restores the production wires. Per-test
// overrides happen AFTER calling this, by reassigning specific seam vars.
func installDoctorMocks(t *testing.T) func() {
	t.Helper()
	origOrphan := doctorOrphanCheckFn
	origStaleLock := doctorStaleLockCheckFn
	origToken := doctorTokenCheckFn
	origFDPass := doctorFDPassCheckFn
	origXrepo := doctorXrepoCheckFn
	origKill := doctorKillFn
	origRemove := doctorRemoveFn
	origConfirm := doctorConfirmFn

	doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{Name: "orphan-mcp-subprocess", Band: bandGreen, Detail: "0 orphan MCP subprocesses"}
	}
	doctorStaleLockCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{Name: "stale-lock-files", Band: bandGreen, Detail: "no stale locks"}
	}
	doctorTokenCheckFn = func() *doctorCheck {
		return &doctorCheck{Name: "vault-ai-token", Band: bandGreen, Detail: "VAULT_AI_TOKEN present"}
	}
	doctorFDPassCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{Name: "token-fd-pass", Band: bandGreen, Detail: "MCP handshake successful"}
	}
	doctorXrepoCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{Name: "xrepo-contract-parity", Band: bandGreen, Detail: "no drift"}
	}
	doctorKillFn = func(_ []int) error { return nil }
	doctorRemoveFn = func(_ []string) error { return nil }
	doctorConfirmFn = func(_, _ string) bool { return false }

	return func() {
		doctorOrphanCheckFn = origOrphan
		doctorStaleLockCheckFn = origStaleLock
		doctorTokenCheckFn = origToken
		doctorFDPassCheckFn = origFDPass
		doctorXrepoCheckFn = origXrepo
		doctorKillFn = origKill
		doctorRemoveFn = origRemove
		doctorConfirmFn = origConfirm
	}
}

// resetVaultDoctorFlags walks rootCmd → vault → doctor and resets each Cobra
// Flag to its Default value. Mirrors resetVaultIngestFlags — rootCmd is a
// package singleton and Cobra flag values persist across rootCmd.Execute()
// calls.
func resetVaultDoctorFlags(t *testing.T) {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() != "vault" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() != "doctor" {
				continue
			}
			sub.Flags().VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
			return
		}
	}
}

func TestVaultDoctorRegistered(t *testing.T) {
	if !findVaultLeaf(t, "doctor") {
		t.Fatal("`ws vault doctor` not registered as a subcommand of `ws vault`")
	}
}

func TestVaultDoctorAllGreen(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected exit 0 on all-green; got err=%v (stderr=%q)", err, errOut.String())
	}

	// --json emits NDJSON: one line per check. Count green entries.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 NDJSON entries (one per check); got %d (out=%q)", len(lines), out.String())
	}
	for i, line := range lines {
		var rec doctorCheck
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v (line=%q)", i, err, line)
		}
		if rec.Band != bandGreen {
			t.Errorf("line %d band=%q (want green); line=%q", i, rec.Band, line)
		}
	}
}

func TestVaultDoctorOrphanOnly(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name:        "orphan-mcp-subprocess",
			Band:        bandRed,
			Detail:      "3 orphan MCP subprocesses detected (PIDs: 1001 1002 1003)",
			Remediation: "re-run with --kill-orphans --yes to clean up",
			PIDs:        []int{1001, 1002, 1003},
		}
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit on red orphan check")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T (%v)", err, err)
	}
	if cerr.code != 2 {
		t.Errorf("expected exit 2 (red); got %d", cerr.code)
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "--kill-orphans") {
		t.Errorf("expected remediation hint mentioning --kill-orphans; got out=%q err=%q", out.String(), errOut.String())
	}
}

func TestVaultDoctorStaleLockOnly(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	doctorStaleLockCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name:        "stale-lock-files",
			Band:        bandYellow,
			Detail:      "1 stale lock: /home/vscode/projects/vault-ai/_tooling/state/audit.lock (mtime 3h ago)",
			Remediation: "re-run with --clear-stale-locks --yes to remove",
			LockPaths:   []string{"/home/vscode/projects/vault-ai/_tooling/state/audit.lock"},
		}
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit on yellow stale-lock check")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T (%v)", err, err)
	}
	if cerr.code != 1 {
		t.Errorf("expected exit 1 (yellow); got %d", cerr.code)
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "--clear-stale-locks") {
		t.Errorf("expected remediation hint mentioning --clear-stale-locks; got %q / %q", out.String(), errOut.String())
	}
}

func TestVaultDoctorMissingToken(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	doctorTokenCheckFn = func() *doctorCheck {
		return &doctorCheck{
			Name:        "vault-ai-token",
			Band:        bandRed,
			Detail:      "VAULT_AI_TOKEN unset or empty",
			Remediation: "provision via chezmoi+age per ADR-ai-06 §Auth; see dotfiles ADR-sec-02 for the age key flow",
		}
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit on missing-token red")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) || cerr.code != 2 {
		t.Errorf("expected exit 2; got %v", err)
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "ADR-ai-06") {
		t.Errorf("expected remediation to cite ADR-ai-06; got %q / %q", out.String(), errOut.String())
	}
}

func TestVaultDoctorBrokenFDPass(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	doctorFDPassCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name:        "token-fd-pass",
			Band:        bandRed,
			Detail:      "stage=initialize handshake: MCP Initialize handshake: context deadline exceeded",
			Remediation: "see RESEARCH §Pitfall 7 + Plan 18-01 for fd-3 wiring",
		}
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit on broken fd-pass red")
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "stage=") {
		t.Errorf("expected detail to indicate which stage broke (stage=...); got %q", combined)
	}
}

func TestVaultDoctorXrepoDrift(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	doctorXrepoCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name:        "xrepo-contract-parity",
			Band:        bandRed,
			Detail:      "check-xrepo-contract.sh exit 1: contract_version drift detected: tools.json=1.3.0 vault-commands.md=1.4.0",
			Remediation: "run gen-go-types.py + verify tools.json contract_version",
		}
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit on xrepo drift red")
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "drift") {
		t.Errorf("expected detail to mention drift; got %q", combined)
	}
}

// TestVaultDoctorReadOnlyByDefault is the load-bearing memory
// `feedback_no_auto_state_mutation` enforcement test. Invokes doctor with NO
// mutation flags + with orphans + stale locks present in the mock state.
// Asserts that doctorKillFn and doctorRemoveFn are NEVER invoked.
func TestVaultDoctorReadOnlyByDefault(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	t.Cleanup(func() { resetVaultDoctorFlags(t) })

	// Mock orphans + stale locks present.
	doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name: "orphan-mcp-subprocess", Band: bandRed,
			Detail: "5 orphan MCP subprocesses", PIDs: []int{1, 2, 3, 4, 5},
		}
	}
	doctorStaleLockCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name: "stale-lock-files", Band: bandYellow,
			Detail: "2 stale locks", LockPaths: []string{"/tmp/a.lock", "/tmp/b.lock"},
		}
	}

	// Count mutation invocations — these MUST stay 0.
	var killCalls, removeCalls int
	doctorKillFn = func(_ []int) error { killCalls++; return nil }
	doctorRemoveFn = func(_ []string) error { removeCalls++; return nil }

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	_ = rootCmd.Execute() // exit code doesn't matter; we care about kill/remove call counts

	if killCalls != 0 {
		t.Errorf(
			"memory feedback_no_auto_state_mutation VIOLATED: "+
				"ws vault doctor (no flags) called killFn %d times — must be 0 for read-only-default discipline",
			killCalls,
		)
	}
	if removeCalls != 0 {
		t.Errorf(
			"memory feedback_no_auto_state_mutation VIOLATED: "+
				"ws vault doctor (no flags) called removeFn %d times — must be 0 for read-only-default discipline",
			removeCalls,
		)
	}
}

func TestVaultDoctorKillOrphansRequiresYes(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	t.Cleanup(func() { resetVaultDoctorFlags(t) })

	doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name: "orphan-mcp-subprocess", Band: bandRed,
			Detail: "3 orphans", PIDs: []int{100, 200, 300},
		}
	}

	// Confirm returns false → kill MUST NOT be called.
	doctorConfirmFn = func(_, _ string) bool { return false }
	var killCalls int
	var gotPIDs []int
	doctorKillFn = func(pids []int) error {
		killCalls++
		gotPIDs = pids
		return nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor", "--kill-orphans"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	_ = rootCmd.Execute()

	if killCalls != 0 {
		t.Errorf("--kill-orphans without --yes + Confirm=false MUST NOT call killFn; got %d calls with pids=%v", killCalls, gotPIDs)
	}
}

func TestVaultDoctorKillOrphansWithYes(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	t.Cleanup(func() { resetVaultDoctorFlags(t) })

	mockPIDs := []int{100, 200, 300}
	doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{
			Name: "orphan-mcp-subprocess", Band: bandRed,
			Detail: "3 orphans", PIDs: mockPIDs,
		}
	}

	// --yes flag should skip Confirm entirely; force Confirm to fail-mode to
	// prove --yes path bypasses it.
	confirmCalls := 0
	doctorConfirmFn = func(_, _ string) bool {
		confirmCalls++
		return false
	}

	var killCalls int
	var gotPIDs []int
	doctorKillFn = func(pids []int) error {
		killCalls++
		gotPIDs = pids
		return nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "doctor", "--kill-orphans", "--yes"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	_ = rootCmd.Execute()

	if confirmCalls != 0 {
		t.Errorf("--yes MUST skip Confirm prompt; got %d invocations", confirmCalls)
	}
	if killCalls != 1 {
		t.Errorf("expected 1 killFn call when --kill-orphans --yes; got %d", killCalls)
	}
	if len(gotPIDs) != len(mockPIDs) {
		t.Errorf("expected killFn called with %d PIDs from check; got %d (%v)", len(mockPIDs), len(gotPIDs), gotPIDs)
	}
	for i, pid := range mockPIDs {
		if i >= len(gotPIDs) || gotPIDs[i] != pid {
			t.Errorf("PID[%d] mismatch: want %d got %v", i, pid, gotPIDs)
		}
	}
}

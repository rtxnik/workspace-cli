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
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/output"
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
	origReady := promptReadyFn

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
	doctorConfirmFn = func(_, _ string) (bool, error) { return false, nil }
	// A terminal to ask on, so that a mutation flag without --yes reaches
	// its prompt: under go test stdin is the null device, and the doctor
	// would refuse before its checks.
	promptReadyFn = func() error { return nil }

	return func() {
		doctorOrphanCheckFn = origOrphan
		doctorStaleLockCheckFn = origStaleLock
		doctorTokenCheckFn = origToken
		doctorFDPassCheckFn = origFDPass
		doctorXrepoCheckFn = origXrepo
		doctorKillFn = origKill
		doctorRemoveFn = origRemove
		doctorConfirmFn = origConfirm
		promptReadyFn = origReady
	}
}

// runDoctor runs ws vault doctor with args, every flag of its leaf reset
// first, and returns what reached stdout — its report, written to the
// command's own stdout, and anything else — what reached stderr, and the
// command's error.
func runDoctor(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	leaf, _, ferr := rootCmd.Find([]string{"vault", "doctor"})
	if ferr != nil {
		t.Fatal(ferr)
	}
	resetFlags(t, leaf)
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append([]string{"vault", "doctor"}, args...))
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		resetFlags(t, leaf)
	})
	osOut, osErr := captureStd(t, func() { err = rootCmd.Execute() })
	return out.String() + osOut, errOut.String() + osErr, err
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
	doctorConfirmFn = func(_, _ string) (bool, error) { return false, nil }
	var killCalls int
	var gotPIDs []int
	doctorKillFn = func(pids []int) error {
		killCalls++
		gotPIDs = pids
		return nil
	}

	_, stderr, err := runDoctor(t, "--kill-orphans")

	if killCalls != 0 {
		t.Errorf("--kill-orphans without --yes + Confirm=false MUST NOT call killFn; got %d calls with pids=%v", killCalls, gotPIDs)
	}
	// The operator's no keeps the band's exit code and says what it skipped.
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) || cerr.code != 2 {
		t.Errorf("a decline returned %v; want the red band's code 2", err)
	}
	if stderr != "Aborted --kill-orphans\n" {
		t.Errorf("a decline printed %q on stderr; want Aborted --kill-orphans", stderr)
	}
}

// Without --yes and with no terminal, ws vault doctor --kill-orphans or
// --clear-stale-locks refuses before any check runs, whatever the checks
// would find: the Problem that names the stream and the step with --yes,
// and nothing printed. With --yes, or without a mutation flag, it does not
// ask, and its checks run.
func TestVaultDoctorRefusesWithNoTerminal(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	var checked int
	doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
		checked++
		return &doctorCheck{Name: "orphan-mcp-subprocess", Band: bandRed, Detail: "1 orphan", PIDs: []int{100}}
	}
	promptReadyFn = func() error { return errNoTerminal }

	for _, tc := range []struct{ args, words []string }{
		{[]string{"--kill-orphans"}, []string{"ws", "vault", "doctor", "--kill-orphans", "--yes"}},
		{[]string{"--clear-stale-locks"}, []string{"ws", "vault", "doctor", "--clear-stale-locks", "--yes"}},
		{[]string{"--kill-orphans", "--clear-stale-locks", "--json"},
			[]string{"ws", "vault", "doctor", "--clear-stale-locks", "--json", "--kill-orphans", "--yes"}},
	} {
		checked = 0
		stdout, stderr, err := runDoctor(t, tc.args...)
		if !errors.Is(err, output.ErrNoTTY) {
			t.Errorf("%v: errors.Is(%v, ErrNoTTY) = false; want true", tc.args, err)
		}
		want := output.Problem{
			Title: "Cannot ask for confirmation: no terminal",
			Cause: "stdin is not a terminal",
			Steps: []output.Remedy{{Label: "Skip the prompt", Words: tc.words}},
		}
		if p, ok := output.ProblemOf(err); !ok || !reflect.DeepEqual(p, want) {
			t.Errorf("%v: the Problem is %+v, %t; want %+v", tc.args, p, ok, want)
		}
		if stdout != "" || stderr != "" {
			t.Errorf("%v: the command printed stdout %q, stderr %q; want nothing", tc.args, stdout, stderr)
		}
		if checked != 0 {
			t.Errorf("%v: the checks ran before the refusal", tc.args)
		}
	}

	for _, args := range [][]string{{"--kill-orphans", "--yes"}, {}} {
		checked = 0
		_, _, err := runDoctor(t, args...)
		var cerr *cliErrorWithExit
		if checked != 1 || !errors.As(err, &cerr) || cerr.code != 2 {
			t.Errorf("%v: checks ran %d time(s), returned %v; want them run once and the red band's code 2", args, checked, err)
		}
	}
}

// On a terminal, a cancelled prompt of ws vault doctor ends the run with the
// cancellation, for the root to die of its signal: the report is already on
// stdout, the command adds nothing to it, the second prompt is not reached,
// and nothing is killed or removed.
func TestVaultDoctorPromptCancelledKeepsTheReport(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{Name: "orphan-mcp-subprocess", Band: bandRed, Detail: "1 orphan", PIDs: []int{100}}
	}
	doctorStaleLockCheckFn = func(_ context.Context) *doctorCheck {
		return &doctorCheck{Name: "stale-lock-files", Band: bandYellow, Detail: "1 stale lock", LockPaths: []string{"/tmp/a.lock"}}
	}
	var asked, killed, removed int
	doctorConfirmFn = func(_, _ string) (bool, error) {
		asked++
		return false, &output.Cancelled{Signal: syscall.SIGINT}
	}
	doctorKillFn = func(_ []int) error { killed++; return nil }
	doctorRemoveFn = func(_ []string) error { removed++; return nil }

	stdout, stderr, err := runDoctor(t, "--kill-orphans", "--clear-stale-locks")
	var cancelled *output.Cancelled
	if !errors.As(err, &cancelled) || cancelled.Signal != syscall.SIGINT {
		t.Errorf("the command returned %v; want the SIGINT cancellation", err)
	}
	if !strings.HasPrefix(stdout, "Vault doctor\n") || !strings.HasSuffix(stdout, "\nOverall: red (exit 2)\n") {
		t.Errorf("stdout is not the report alone:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q; want nothing added to the report", stderr)
	}
	if asked != 1 || killed != 0 || removed != 0 {
		t.Errorf("asked %d, killed %d, removed %d; want one prompt and no mutation", asked, killed, removed)
	}
}

// On a terminal, a prompt of ws vault doctor that fails ends the run with
// the report on stdout, the prompt's Problem for the root to print, and the
// band's code, at least 1: 2 under the red band the kill prompt runs on, 1
// under a band that is green. A prompt that found no terminal after all
// gives the Problem with the step with --yes.
func TestVaultDoctorPromptFailed(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	failed := errors.New("prompt failed: raw mode refused")
	red := func() {
		doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
			return &doctorCheck{Name: "orphan-mcp-subprocess", Band: bandRed, Detail: "1 orphan", PIDs: []int{100}}
		}
	}
	green := func() {
		doctorOrphanCheckFn = func(_ context.Context) *doctorCheck {
			return &doctorCheck{Name: "orphan-mcp-subprocess", Band: bandGreen, Detail: "0 orphans"}
		}
		doctorStaleLockCheckFn = func(_ context.Context) *doctorCheck {
			return &doctorCheck{Name: "stale-lock-files", Band: bandGreen, LockPaths: []string{"/tmp/a.lock"}}
		}
	}
	for _, tc := range []struct {
		name   string
		setup  func()
		flag   string
		err    error
		want   output.Problem
		code   int
		report string
	}{
		{"red band", red, "--kill-orphans", failed, output.Problem{Title: "prompt failed: raw mode refused"}, 2, "Overall: red (exit 2)"},
		{"green band", green, "--clear-stale-locks", failed, output.Problem{Title: "prompt failed: raw mode refused"}, 1, "Overall: green (exit 0)"},
		{"no terminal at the prompt", red, "--kill-orphans", errNoTerminal, output.Problem{
			Title: "Cannot ask for confirmation: no terminal",
			Cause: "stdin is not a terminal",
			Steps: []output.Remedy{{Label: "Skip the prompt", Words: []string{"ws", "vault", "doctor", "--kill-orphans", "--yes"}}},
		}, 2, "Overall: red (exit 2)"},
	} {
		tc.setup()
		doctorConfirmFn = func(_, _ string) (bool, error) { return false, tc.err }
		stdout, _, err := runDoctor(t, tc.flag)
		msg, code := run(err)
		if code != tc.code {
			t.Errorf("%s: exit %d; want %d", tc.name, code, tc.code)
		}
		if p, printed := rootProblem(msg, err); !printed || !reflect.DeepEqual(p, tc.want) {
			t.Errorf("%s: the root prints %+v, %t; want %+v", tc.name, p, printed, tc.want)
		}
		if !strings.HasPrefix(stdout, "Vault doctor\n") || !strings.HasSuffix(stdout, "\n"+tc.report+"\n") {
			t.Errorf("%s: stdout is not the report:\n%s", tc.name, stdout)
		}
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
	doctorConfirmFn = func(_, _ string) (bool, error) {
		confirmCalls++
		return false, nil
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

// TestVaultDoctorReport pins ws vault doctor's report (phase-5 §3.7): each
// check's band as its state, its detail as the note with its remediation as
// a second paragraph, and the worst band with its exit code as the caption.
func TestVaultDoctorReport(t *testing.T) {
	checks := []*doctorCheck{
		{Name: "orphan-mcp-subprocess", Band: bandGreen, Detail: "0 orphan MCP subprocesses"},
		{Name: "vault-ai-token", Band: bandRed, Detail: "VAULT_AI_TOKEN unset or empty", Remediation: "provision via chezmoi+age"},
		{Name: "xrepo-contract-parity", Band: bandYellow, Detail: "check-xrepo-contract.sh not found", Remediation: "verify the checkout"},
		{Name: "token-fd-pass", Band: bandRed, Remediation: "re-run with --verbose"},
		{Name: "future-check", Band: statusBand("purple"), Detail: "a band no check produces today"},
	}
	want := output.Checks{Title: "Vault doctor", Items: []output.Check{
		{Name: "orphan-mcp-subprocess", State: output.StateOK, Note: "0 orphan MCP subprocesses"},
		{Name: "vault-ai-token", State: output.StateFail, Note: "VAULT_AI_TOKEN unset or empty\nFix: provision via chezmoi+age"},
		{Name: "xrepo-contract-parity", State: output.StateAdvisory, Note: "check-xrepo-contract.sh not found\nFix: verify the checkout"},
		{Name: "token-fd-pass", State: output.StateFail, Note: "Fix: re-run with --verbose"},
		{Name: "future-check", State: output.StateUnknown, Note: "a band no check produces today"},
	}, Caption: "Overall: red (exit 2)"}
	got := vaultDoctorReport(checks, bandRed, 2)
	if got.Title != want.Title || got.Caption != want.Caption {
		t.Errorf("title %q caption %q; want %q, %q", got.Title, got.Caption, want.Title, want.Caption)
	}
	if len(got.Items) != len(want.Items) {
		t.Fatalf("%d lines, want %d: %+v", len(got.Items), len(want.Items), got.Items)
	}
	for i, w := range want.Items {
		if got.Items[i] != w {
			t.Errorf("line %d = %+v; want %+v", i+1, got.Items[i], w)
		}
	}
}

// TestVaultDoctorVerdictOnStdout: the human report and its verdict go to
// stdout, nothing to stderr, and the command returns the band's exit code
// with no message, so the root prints nothing of its own.
func TestVaultDoctorVerdictOnStdout(t *testing.T) {
	restore := installDoctorMocks(t)
	t.Cleanup(restore)
	resetVaultDoctorFlags(t)
	doctorTokenCheckFn = func() *doctorCheck {
		return &doctorCheck{Name: "vault-ai-token", Band: bandRed, Detail: "VAULT_AI_TOKEN unset or empty", Remediation: "provision it"}
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
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) || cerr.code != 2 || cerr.msg != "" {
		t.Errorf("returned %#v; want a cliErrorWithExit with code 2 and no message", err)
	}
	if !strings.HasPrefix(out.String(), "Vault doctor\n") || !strings.HasSuffix(out.String(), "\nOverall: red (exit 2)\n") {
		t.Errorf("stdout is not the report closed by its verdict:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q; want nothing", errOut.String())
	}
}

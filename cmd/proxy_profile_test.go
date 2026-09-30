package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/fsutil"
	"github.com/rtxnik/workspace-cli/internal/output"
)

func TestProxyProfileCommand(t *testing.T) {
	var foundProxy bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "proxy" {
			continue
		}
		foundProxy = true
		var foundProfile bool
		for _, sub := range c.Commands() {
			if sub.Name() != "profile" {
				continue
			}
			foundProfile = true
			want := map[string]bool{
				"add": false, "list": false, "use": false, "rm": false,
				"show": false, "current": false, "regenerate": false,
			}
			for _, leaf := range sub.Commands() {
				if _, ok := want[leaf.Name()]; ok {
					want[leaf.Name()] = true
				}
			}
			for name, present := range want {
				if !present {
					t.Errorf("expected `ws proxy profile %s` leaf not registered", name)
				}
			}
			if flag := sub.PersistentFlags().Lookup("no-migrate"); flag == nil {
				t.Error("expected --no-migrate persistent flag on `ws proxy profile`")
			}
		}
		if !foundProfile {
			t.Error("`ws proxy profile` not registered as a subcommand of `ws proxy`")
		}
	}
	if !foundProxy {
		t.Fatal("`ws proxy` not registered on rootCmd")
	}
}

func TestProxyProfileHelpExits0(t *testing.T) {
	cmd := rootCmd
	origOut, origErr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(origOut)
		cmd.SetErr(origErr)
	})
	cmd.SetArgs([]string{"proxy", "profile", "--help"})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("`ws proxy profile --help` returned %v; stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	combined := out.String() + errOut.String()
	for _, leaf := range []string{"add", "list", "use", "rm", "show", "current", "regenerate"} {
		if !strings.Contains(combined, leaf) {
			t.Errorf("help output missing leaf %q; got: %s", leaf, combined)
		}
	}
}

// TestProfileUseRendersPreSwapError guards the 2026-05-13 hotfix: pre-swap
// errors from xray.SwitchTo (invalid profile name, legacy bind mount, missing
// target file) must reach the operator instead of being swallowed by
// os.Exit(1). The original profileUseCmd used `Run` and exited silently on
// error; this test pins the RunE contract. Phase 1 moved the print itself: the
// root silences cobra and prints the error, so what this test pins is that the
// error propagates and which branch the root takes. A pre-swap error is a
// plain error, which the root prints as its ✗ line: xray's
// TestSwitchToLeavesTheErrorToTheRoot pins that, and TestRootProblemSelects
// what the root prints.
//
// We use a slash-containing profile name to trip ValidateProfileName's regex
// (^[a-z0-9_-]{1,32}$) — same pre-swap error path as the bind-check failure,
// but reachable without overriding internal/xray package-private seams.
// --no-migrate avoids EnsureMigrated I/O during the test.
//
// 260513-dbc: the new orchestration runs verifyProxyReadyFn BEFORE
// switchToFn. To still reach the xray.SwitchTo pre-swap path — the contract
// this test pins — stub the pre-flight to nil so the orchestrator falls
// through to switchToFn.
func TestProfileUseRendersPreSwapError(t *testing.T) {
	origVerify := verifyProxyReadyFn
	verifyProxyReadyFn = func(_ config.Config) error { return nil }
	t.Cleanup(func() { verifyProxyReadyFn = origVerify })

	cmd := rootCmd
	origOut, origErr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(origOut)
		cmd.SetErr(origErr)
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "profile", "use", "bad/name", "--no-migrate"})

	resetSilenceUsage(t, "proxy", "profile", "use")
	failed, err := cmd.ExecuteC()
	if err == nil {
		t.Fatal("expected Execute() to return non-nil for invalid profile name (pre-swap error must propagate)")
	}
	// The root prints the error, not cobra: rootCmd sets SilenceErrors, so the
	// text reaches the operator through run() and rootProblem, which
	// TestRootProblemSelects pins.
	msg, code := run(err)
	if !strings.Contains(msg, "invalid profile name") || code != 1 {
		t.Fatalf("root protocol returned msg=%q code=%d; want the pre-swap error at exit 1", msg, code)
	}
	// A runtime error, not a usage error: the body set SilenceUsage before it
	// returned, so the root prints no usage lines under it.
	if _, _, isUsage := usageTarget(failed, err); isUsage {
		t.Errorf("the pre-swap error is classified as a usage error: %v", err)
	}
}

// resetSilenceUsage sets the SilenceUsage of the command at path back to
// false for the rest of the test, and restores it after. Every body sets it
// as its first statement, and a command object lives for the whole test
// process, so an earlier test that ran the same command leaves it true; a
// test that asserts an error is a runtime error would then pass whatever the
// code under test did.
func resetSilenceUsage(t *testing.T, path ...string) {
	t.Helper()
	leaf, _, err := rootCmd.Find(path)
	if err != nil {
		t.Fatalf("finding %v: %v", path, err)
	}
	was := leaf.SilenceUsage
	leaf.SilenceUsage = false
	t.Cleanup(func() { leaf.SilenceUsage = was })
}

// withTempCfg installs a config.Load stub returning a Config rooted at
// the given temp dir, and restores on cleanup.
func withTempCfg(t *testing.T, root string) config.Config {
	t.Helper()
	cfg := config.Config{
		XrayConfig:      filepath.Join(root, "config.json"),
		XrayProfilesDir: filepath.Join(root, "profiles"),
		ProxyContainer:  "ws-proxy-test",
	}
	orig := loadConfigFn
	loadConfigFn = func() config.Config { return cfg }
	t.Cleanup(func() { loadConfigFn = orig })
	return cfg
}

// seamHandles aggregates the four cmd-layer orchestration seams so tests
// can swap individually while the cleanup restores all four atomically.
type seamHandles struct {
	verify        *func(config.Config) error
	switchTo      *func(config.Config, string) error
	switchSymlink *func(config.Config, string) error
	restart       *func(config.Config) error
}

func withSeams(t *testing.T) seamHandles {
	t.Helper()
	origVerify := verifyProxyReadyFn
	origSwitch := switchToFn
	origSwitchSymlink := switchToSymlinkOnlyFn
	origRestart := proxyRestartFn
	t.Cleanup(func() {
		verifyProxyReadyFn = origVerify
		switchToFn = origSwitch
		switchToSymlinkOnlyFn = origSwitchSymlink
		proxyRestartFn = origRestart
	})
	return seamHandles{
		verify:        &verifyProxyReadyFn,
		switchTo:      &switchToFn,
		switchSymlink: &switchToSymlinkOnlyFn,
		restart:       &proxyRestartFn,
	}
}

// execCapture prepares rootCmd for one Execute() call with captured
// stdout/stderr. Cleans up on test end.
//
// Cobra flag values persist across Execute() calls when the same *cobra.Command
// instance is reused (which is unavoidable here — rootCmd is a package
// global). Tests that exercise different flag combinations would otherwise
// see stale values from prior tests. Explicit reset before each call avoids
// the bleed.
func execCapture(t *testing.T, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	resetProfileUseFlags(t)
	cmd := rootCmd
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	return &out, &errOut, cmd.Execute()
}

// resetProfileUseFlags zeroes any stateful Cobra flags on `ws proxy profile use`
// so previous test invocations don't leak their parsed values into the next.
func resetProfileUseFlags(t *testing.T) {
	t.Helper()
	if f := profileUseCmd.Flags().Lookup("no-reload"); f != nil {
		_ = f.Value.Set("false")
		f.Changed = false
	}
	if f := profileCmd.PersistentFlags().Lookup("no-migrate"); f != nil {
		_ = f.Value.Set("false")
		f.Changed = false
	}
}

func TestProfileUseInvokesProxyRestart_HappyPath(t *testing.T) {
	_ = withTempCfg(t, t.TempDir())
	s := withSeams(t)
	var switchCalls int
	*s.verify = func(_ config.Config) error { return nil }
	*s.switchTo = func(_ config.Config, _ string) error {
		switchCalls++
		return nil
	}
	*s.switchSymlink = func(_ config.Config, _ string) error {
		t.Fatal("switchToSymlinkOnlyFn must NOT be called on default path")
		return nil
	}

	out, _, err := execCapture(t, "proxy", "profile", "use", "somename", "--no-migrate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if switchCalls != 1 {
		t.Errorf("expected switchToFn called once, got %d", switchCalls)
	}
	if !strings.Contains(out.String(), "Switched to") {
		t.Errorf("missing success header; stdout=%q", out.String())
	}
	if !strings.Contains(out.String(), "proxy reloaded in") {
		t.Errorf("missing elapsed-time tag; stdout=%q", out.String())
	}
}

func TestProfileUseSkipsProxyRestartWithFlag(t *testing.T) {
	_ = withTempCfg(t, t.TempDir())
	s := withSeams(t)
	*s.verify = func(_ config.Config) error {
		t.Fatal("verifyProxyReadyFn must NOT be called with --no-reload")
		return nil
	}
	*s.switchTo = func(_ config.Config, _ string) error {
		t.Fatal("switchToFn must NOT be called with --no-reload")
		return nil
	}
	*s.switchSymlink = func(_ config.Config, _ string) error { return nil }

	out, _, err := execCapture(t, "proxy", "profile", "use", "somename", "--no-reload", "--no-migrate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "NOT reloaded") {
		t.Errorf("expected 'NOT reloaded' in stdout; got %q", out.String())
	}
	if !strings.Contains(out.String(), "ws proxy restart") {
		t.Errorf("expected manual-recovery hint 'ws proxy restart'; got %q", out.String())
	}
}

// TestProfileUseRendersPartialFailureWithoutRollback is the cmd-layer
// TRIPWIRE for D-10 / feedback_no_auto_state_mutation. It pins the
// orchestration-boundary contract: when xray.SwitchTo fails AFTER landing
// the symlink swap, the cmd layer does NOT restore the previous symlink.
// If a future contributor adds rollback logic to profileUseCmd (e.g.
// restoring the previous symlink on restart failure), this test fails.
// Before changing it they MUST revisit the discuss-phase decision at
// workspace-meta/.planning/todos/pending/2026-05-13-phase-22-seamless-profile-use-with-reload.md
// §"D-10 compliance" AND Phase 22 CONTEXT.md D-10.
//
// Companion tripwire: internal/xray.TestManualRecoveryOnFailedSwitch
// (same contract, lower layer).
func TestProfileUseRendersPartialFailureWithoutRollback(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "primary.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "broken.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed broken: %v", err)
	}
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfgPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	orig := loadConfigFn
	loadConfigFn = func() config.Config {
		return config.Config{
			XrayConfig:      cfgPath,
			XrayProfilesDir: profilesDir,
			ProxyContainer:  "ws-proxy-test",
		}
	}
	t.Cleanup(func() { loadConfigFn = orig })

	s := withSeams(t)
	*s.verify = func(_ config.Config) error { return nil }
	// Simulate xray.SwitchTo: do the real symlink swap, then return the
	// error of a post-swap restart failure, which carries the switch's
	// Problem. This matches the production failure path that xray.SwitchTo
	// produces when restartProxyFn fails after AtomicSymlink succeeded.
	switchProblem := output.Problem{Title: `Switch to "broken" failed`,
		Facts: []output.Fact{{K: "Previous", V: "primary"}, {K: "Active", V: "broken (not rolled back)"}}}
	*s.switchTo = func(cfg config.Config, name string) error {
		relativeTarget := filepath.Join("profiles", name+".json")
		if err := fsutil.AtomicSymlink(relativeTarget, cfg.XrayConfig); err != nil {
			return err
		}
		return &output.ProblemError{P: switchProblem, Err: fmt.Errorf("switch to %q failed (previous=%q): %w",
			name, "primary", errors.New("simulated post-swap restart failure"))}
	}

	out, errOut, err := execCapture(t, "proxy", "profile", "use", "broken", "--no-migrate")
	if err == nil {
		t.Fatal("expected non-nil error after simulated post-swap failure")
	}

	// CRITICAL TRIPWIRE: symlink must STILL point at the new (broken)
	// target — NO cmd-layer auto-rollback.
	got, readErr := os.Readlink(cfgPath)
	if readErr != nil {
		t.Fatalf("readlink after failure: %v", readErr)
	}
	wantSuffix := filepath.Join("profiles", "broken.json")
	if got != wantSuffix {
		t.Fatalf("CMD-LAYER AUTO-ROLLBACK DETECTED: symlink points at %q, want %q (D-10 / feedback_no_auto_state_mutation tripwire). If you intentionally added rollback to profileUseCmd, revisit todos/2026-05-13-phase-22-seamless-profile-use-with-reload.md §D-10 first.", got, wantSuffix)
	}

	combined := out.String() + errOut.String() + err.Error()
	if !strings.Contains(combined, "previous=") {
		t.Errorf("expected wrapped error to contain 'previous='; got: %s / err=%v", combined, err)
	}
	if !strings.Contains(combined, "simulated post-swap restart failure") {
		t.Errorf("expected underlying error to be visible; got: %s / err=%v", combined, err)
	}
	if strings.Contains(out.String(), "proxy reloaded in") {
		t.Errorf("happy-path success line must NOT fire on failure; stdout=%q", out.String())
	}
	// The cmd layer prints nothing of the failure and hides nothing of it:
	// the root prints the switch's Problem, once.
	if p, ok := output.ProblemOf(err); !ok || !reflect.DeepEqual(p, switchProblem) || errOut.Len() != 0 {
		t.Errorf("the cmd layer wrote %q and returned a Problem %+v (%t); want nothing written and the switch's Problem", errOut.String(), p, ok)
	}
}

func TestProfileUsePreFlightRejectsWhenProxyDown(t *testing.T) {
	_ = withTempCfg(t, t.TempDir())
	s := withSeams(t)
	*s.verify = func(_ config.Config) error {
		return fmt.Errorf("proxy container %q is not running (state=exited)", "ws-proxy")
	}
	*s.switchTo = func(_ config.Config, _ string) error {
		t.Fatal("switchToFn must NOT be called when pre-flight fails")
		return nil
	}
	*s.switchSymlink = func(_ config.Config, _ string) error {
		t.Fatal("switchToSymlinkOnlyFn must NOT be called on default path")
		return nil
	}

	out, errOut, err := execCapture(t, "proxy", "profile", "use", "somename", "--no-migrate")
	if err == nil {
		t.Fatal("expected non-nil error on pre-flight failure")
	}
	combined := out.String() + errOut.String() + err.Error()
	if !strings.Contains(combined, "proxy not ready for reload") {
		t.Errorf("expected 'proxy not ready for reload'; got %s / err=%v", combined, err)
	}
	if !strings.Contains(combined, "not running") {
		t.Errorf("expected wrapped pre-flight error 'not running'; got %s / err=%v", combined, err)
	}
}

func TestProfileUseSkipsPreFlightWhenNoReload(t *testing.T) {
	_ = withTempCfg(t, t.TempDir())
	s := withSeams(t)
	*s.verify = func(_ config.Config) error {
		t.Fatal("verifyProxyReadyFn must NOT be called when --no-reload is set")
		return nil
	}
	*s.switchSymlink = func(_ config.Config, _ string) error { return nil }

	_, _, err := execCapture(t, "proxy", "profile", "use", "somename", "--no-reload", "--no-migrate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProfileShowHysteria2Masking(t *testing.T) {
	dir := t.TempDir()
	profilesDir := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[
		{"tag":"proxy-1","protocol":"hysteria","settings":{"version":2,"address":"h.example","port":443},
		 "streamSettings":{"network":"hysteria","security":"tls",
		   "tlsSettings":{"serverName":"h.example"},
		   "hysteriaSettings":{"version":2,"auth":"AUTHREDACTED"},
		   "finalmask":{"udp":[{"type":"salamander","settings":{"password":"OBFSREDACTED"}}]}}},
		{"tag":"direct","protocol":"freedom","settings":{}}],"routing":{"rules":[]}}`
	if err := os.WriteFile(filepath.Join(profilesDir, "hy2.json"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hermetic isolation: profileCmd.PersistentPreRunE calls config.Load()
	// DIRECTLY (not the loadConfigFn seam) and would run EnsureMigrated against
	// the operator's real ~/.config/xray. Point config.Load at the temp dir via
	// env, and pass --no-migrate so no migration is attempted.
	t.Setenv("XRAY_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("XRAY_PROFILES_DIR", profilesDir)

	// masked (default)
	out, _, err := execCapture(t, "proxy", "profile", "show", "hy2", "--no-migrate")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	masked := out.String()
	if strings.Contains(masked, "AUTHREDACTED") || strings.Contains(masked, "OBFSREDACTED") {
		t.Errorf("show leaked secret in masked mode:\n%s", masked)
	}
	if !strings.Contains(masked, "Protocol:") || !strings.Contains(masked, "Auth:") {
		t.Errorf("show missing hy2 fields:\n%s", masked)
	}
	// revealed
	out, _, err = execCapture(t, "proxy", "profile", "show", "hy2", "--reveal", "--no-migrate")
	if err != nil {
		t.Fatalf("show --reveal: %v", err)
	}
	if revealed := out.String(); !strings.Contains(revealed, "AUTHREDACTED") {
		t.Errorf("show --reveal did not reveal auth:\n%s", revealed)
	}
}

func TestProfileShowHysteria2JSONNoUUID(t *testing.T) {
	dir := t.TempDir()
	profilesDir := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[
		{"tag":"proxy-1","protocol":"hysteria","settings":{"version":2,"address":"h.example","port":443},
		 "streamSettings":{"network":"hysteria","security":"tls",
		   "tlsSettings":{"serverName":"h.example"},
		   "hysteriaSettings":{"version":2,"auth":"AUTHREDACTED"},
		   "finalmask":{"udp":[{"type":"salamander","settings":{"password":"OBFSREDACTED"}}]}}},
		{"tag":"direct","protocol":"freedom","settings":{}}],"routing":{"rules":[]}}`
	if err := os.WriteFile(filepath.Join(profilesDir, "hy2.json"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("XRAY_PROFILES_DIR", profilesDir)

	out, _, err := execCapture(t, "proxy", "profile", "show", "hy2", "--json", "--no-migrate")
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}

	var dp map[string]any
	if err := json.Unmarshal(out.Bytes(), &dp); err != nil {
		t.Fatalf("unmarshal JSON: %v\nraw: %s", err, out.String())
	}
	if proto, _ := dp["protocol"].(string); proto != "hysteria2" {
		t.Errorf("expected protocol=hysteria2, got %q", proto)
	}
	if uuid, ok := dp["uuid"]; ok && uuid != "" {
		t.Errorf("expected no uuid key (or empty) in hysteria2 JSON output, got %q", uuid)
	}
}

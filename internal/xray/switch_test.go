package xray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// TestValidationGate asserts ValidateProfile produces an error wrapping the
// profile name when the underlying ProxyExec call fails (e.g. dev-proxy is
// absent). Live docker integration belongs to Plan 22-06.
func TestValidationGate(t *testing.T) {
	cfg := config.Config{
		ProxyContainer:  "this-container-does-not-exist-xx22",
		XrayProfilesDir: t.TempDir(),
	}
	err := ValidateProfile(cfg, "primary")
	if err == nil {
		t.Skip("docker exec to non-existent container did not error (CI without docker?)")
	}
	if !strings.Contains(err.Error(), "xray -test failed for profile \"primary\"") {
		t.Errorf("error does not wrap profile name: %v", err)
	}
}

// TestManualRecoveryOnFailedSwitch is the TRIPWIRE test per D-10 and memory
// `feedback_no_auto_state_mutation`. It forces step-3 (Restart) to fail and
// asserts the post-failure symlink STILL points at the new (failed) target —
// i.e. NO auto-rollback occurred. If a future contributor "improves" the
// failure path by adding `os.Symlink(previous, cfg.XrayConfig)` or any other
// state-mutating recovery, THIS TEST fails in CI. They MUST revisit the
// discuss-phase decision (Q10 revised; CONTEXT.md D-10) before changing it.
func TestManualRecoveryOnFailedSwitch(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed two profile files.
	if err := os.WriteFile(filepath.Join(profilesDir, "primary.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "broken.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed broken: %v", err)
	}
	// Initial symlink: config.json -> profiles/primary.json.
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfgPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	cfg := config.Config{
		XrayConfig:      cfgPath,
		XrayProfilesDir: profilesDir,
		ProxyContainer:  "dev-proxy-test-xx22",
	}

	// Override test seams: validate passes, bind-mount check passes, wait
	// passes — but restart fails. This isolates the post-swap failure path.
	origValidate := validateProfileFn
	origRestart := restartProxyFn
	origWait := waitForHealthFn
	origBindCheck := bindMountIsWholeDirFn
	defer func() {
		validateProfileFn = origValidate
		restartProxyFn = origRestart
		waitForHealthFn = origWait
		bindMountIsWholeDirFn = origBindCheck
	}()
	validateProfileFn = func(_ config.Config, _ string) error { return nil }
	restartProxyFn = func(_ config.Config) error { return fmt.Errorf("simulated docker restart failure") }
	waitForHealthFn = func(_ config.Config, _ time.Duration) error { return nil }
	bindMountIsWholeDirFn = func(_ config.Config) (bool, error) { return true, nil }

	err := SwitchTo(cfg, "broken")
	if err == nil {
		t.Fatal("expected SwitchTo to return error after simulated restart failure")
	}
	if !strings.Contains(err.Error(), "primary") {
		t.Errorf("expected error to mention previous profile 'primary'; got: %v", err)
	}

	// CRITICAL TRIPWIRE ASSERTION: symlink must STILL point at the new
	// (broken) target. If a future contributor adds auto-rollback to
	// switch.go, this assertion fails with the message below — which
	// directs them at the discuss-phase decision they would need to
	// revisit before making the change.
	got, readErr := os.Readlink(cfgPath)
	if readErr != nil {
		t.Fatalf("readlink after failure: %v", readErr)
	}
	wantSuffix := filepath.Join("profiles", "broken.json")
	if got != wantSuffix {
		t.Fatalf("AUTO-ROLLBACK DETECTED: symlink points at %q, want %q (D-10 + feedback_no_auto_state_mutation tripwire). If you intentionally added auto-rollback, you must revisit the discuss-phase decision (CONTEXT.md D-10) first.", got, wantSuffix)
	}
}

// newSwitchFixture seeds a profiles dir holding primary.json and secondary.json,
// points config.json at primary, and stubs every SwitchTo seam to succeed. A
// test overrides the seam it drives; t.Cleanup restores all four.
func newSwitchFixture(t *testing.T) (config.Config, string) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"primary", "secondary"} {
		if err := os.WriteFile(filepath.Join(profilesDir, name+".json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfgPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	origValidate, origRestart, origWait, origBindCheck := validateProfileFn, restartProxyFn, waitForHealthFn, bindMountIsWholeDirFn
	t.Cleanup(func() {
		validateProfileFn, restartProxyFn, waitForHealthFn, bindMountIsWholeDirFn = origValidate, origRestart, origWait, origBindCheck
	})
	validateProfileFn = func(_ config.Config, _ string) error { return nil }
	restartProxyFn = func(_ config.Config) error { return nil }
	waitForHealthFn = func(_ config.Config, _ time.Duration) error { return nil }
	bindMountIsWholeDirFn = func(_ config.Config) (bool, error) { return true, nil }

	return config.Config{
		XrayConfig:      cfgPath,
		XrayProfilesDir: profilesDir,
		ProxyContainer:  "dev-proxy-test-xx22",
	}, cfgPath
}

// TestSwitchToWaitsOneFullProbeCycle pins the liveness budget to the proxy
// image's HEALTHCHECK schedule. The restart is a stop and a start, and every
// start resets Docker's health status to "starting". On Engine 27.0 or later
// the first probe runs about 5s after the start and, if it fails (a curl
// timeout included), the next one runs 30s after it ends; older Engines run the
// first probe only after the 30s interval. A budget shorter than that reports a
// switch whose container passes that second probe as timed out.
func TestSwitchToWaitsOneFullProbeCycle(t *testing.T) {
	// The proxy image declares HEALTHCHECK --interval=30s --timeout=10s
	// --start-period=5s --retries=3 and sets no --start-interval.
	const (
		startInterval = 5 * time.Second  // Docker's default --start-interval (Engine 27.0 or later)
		interval      = 30 * time.Second // --interval, counted from the end of the previous probe
		probe         = 11 * time.Second // upper bound: --timeout plus up to ~1s to start the exec (curl --max-time 5 usually ends it within ~6s)
		poll          = 1 * time.Second  // how often WaitForHealth inspects the container
	)
	// 58s. On older Engines the first probe alone needs interval+probe+poll = 42s.
	floor := startInterval + probe + interval + probe + poll

	cfg, _ := newSwitchFixture(t)
	var budget time.Duration
	waitForHealthFn = func(_ config.Config, d time.Duration) error {
		budget = d
		return nil
	}

	if err := SwitchTo(cfg, "secondary"); err != nil {
		t.Fatalf("SwitchTo: %v", err)
	}
	if budget < floor {
		t.Fatalf("SwitchTo waits %s for liveness; a healthy container can need %s (a failed first probe, then a passing second one)", budget, floor)
	}
}

// TestNoRollbackWhenLivenessTimesOut extends the no-auto-rollback tripwire
// above to the liveness step: a wait that gives up must leave the symlink at
// the new profile and name the previous one in the error.
func TestNoRollbackWhenLivenessTimesOut(t *testing.T) {
	cfg, cfgPath := newSwitchFixture(t)
	waitForHealthFn = func(_ config.Config, d time.Duration) error {
		return fmt.Errorf("proxy health check timed out after %s", d)
	}

	err := SwitchTo(cfg, "secondary")
	if err == nil {
		t.Fatal("expected SwitchTo to return the liveness failure")
	}
	if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "primary") {
		t.Errorf("expected the timeout and the previous profile 'primary' in the error; got: %v", err)
	}
	got, readErr := os.Readlink(cfgPath)
	if readErr != nil {
		t.Fatalf("readlink after failure: %v", readErr)
	}
	if want := filepath.Join("profiles", "secondary.json"); got != want {
		t.Fatalf("AUTO-ROLLBACK DETECTED: symlink points at %q after a liveness failure, want %q", got, want)
	}
}

// TestSwitchToReturnsPreSwapErrorOnLegacyBind guards the 2026-05-13 hotfix:
// when BindMountIsWholeDir reports false (legacy single-file bind), SwitchTo
// must return a non-nil error whose message mentions "legacy single-file bind
// mount" so the cmd layer can surface it to the operator. Previously the
// error was correctly returned by SwitchTo, but profileUseCmd swallowed it via
// os.Exit(1) with no output. This test pins the SwitchTo-layer contract; the
// cmd layer's rendering of it is pinned by cmd/error_protocol_test.go
// (TestErrorOutputBaseline); cmd/proxy_profile_test.go pins that it propagates.
func TestSwitchToReturnsPreSwapErrorOnLegacyBind(t *testing.T) {
	origBindCheck := bindMountIsWholeDirFn
	defer func() { bindMountIsWholeDirFn = origBindCheck }()
	bindMountIsWholeDirFn = func(_ config.Config) (bool, error) { return false, nil }

	cfg := config.Config{
		ProxyContainer:  "dev-proxy-test-xx22-cibind",
		XrayConfig:      filepath.Join(t.TempDir(), "config.json"),
		XrayProfilesDir: t.TempDir(),
	}
	err := SwitchTo(cfg, "foo")
	if err == nil {
		t.Fatal("expected SwitchTo to return non-nil when bind is legacy single-file")
	}
	if !strings.Contains(err.Error(), "legacy single-file bind mount") {
		t.Fatalf("expected error to mention `legacy single-file bind mount`; got: %v", err)
	}
}

//go:build integration

package xray

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
)

// Throwaway VLESS URIs — `xray -test` validates JSON shape only; localhost
// is fine. Markers (#test-*-do-not-use) keep these visually distinct.
const (
	testURIA = "vless://12345678-1234-1234-1234-123456789012@127.0.0.1:443?type=tcp&security=none#test-a-do-not-use"
	testURIB = "vless://87654321-1234-1234-1234-210987654321@127.0.0.1:8443?type=tcp&security=none#test-b-do-not-use"
)

func requireDevProxy(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	out, err := exec.Command("docker", "ps", "-q", "-f", "name=dev-proxy").Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Skip("dev-proxy not running")
	}
}

func realXrayRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "xray")
}

func captureOriginalActive(t *testing.T) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(realXrayRoot(), "config.json"))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(target), ".json")
}

// TestIntegration_Cycle exercises add -> swap -> read -> rm on a fully
// test-isolated XRAY_CONFIG + XRAY_PROFILES_DIR via t.Setenv (operator's real
// ~/.config/xray/ NEVER touched). Strategy A: bypasses SwitchTo's container
// validation gate (container bind cannot see test tmpdir); exercises
// AddProfile + AtomicSymlink + ReadActiveProfileName + RemoveProfile directly.
// Full real-pipeline E2E lives in TestProfileLifecycleE2E + operator checkpoint.
func TestIntegration_Cycle(t *testing.T) {
	requireDevProxy(t)
	// Defensive capture+restore: Strategy A does not actually mutate real
	// state, but guard against future refactors that re-target real cfg.
	originalActive := captureOriginalActive(t)
	t.Cleanup(func() {
		if originalActive == "" {
			return
		}
		real := config.Config{
			XrayConfig:      filepath.Join(realXrayRoot(), "config.json"),
			XrayProfilesDir: filepath.Join(realXrayRoot(), "profiles"),
			ProxyContainer:  "dev-proxy",
		}
		if cur, err := ReadActiveProfileName(real); err == nil && cur == originalActive {
			return
		}
		if err := SwitchTo(real, originalActive); err != nil {
			t.Logf("cleanup SwitchTo(%q) failed: %v", originalActive, err)
		}
	})

	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	profilesDir := filepath.Join(root, "profiles")
	t.Setenv("XRAY_CONFIG", cfgPath)
	t.Setenv("XRAY_PROFILES_DIR", profilesDir)
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	cfg := config.Load()
	if cfg.XrayConfig != cfgPath || cfg.XrayProfilesDir != profilesDir {
		t.Fatalf("config.Load ignored t.Setenv: cfg=%+v", cfg)
	}
	if err := AddProfile(cfg, "test-primary", testURIA, false); err != nil {
		t.Fatalf("AddProfile test-primary: %v", err)
	}
	if err := AddProfile(cfg, "test-backup", testURIB, false); err != nil {
		t.Fatalf("AddProfile test-backup: %v", err)
	}
	for _, name := range []string{"test-primary", "test-backup"} {
		if _, err := os.Stat(filepath.Join(profilesDir, name+".json")); err != nil {
			t.Fatalf("profile %q missing: %v", name, err)
		}
	}
	// Seed initial symlink, then swap. Bypasses SwitchTo container gate.
	if err := AtomicSymlink(filepath.Join("profiles", "test-primary.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("AtomicSymlink seed: %v", err)
	}
	if active, err := ReadActiveProfileName(cfg); err != nil || active != "test-primary" {
		t.Fatalf("after seed: active=%q err=%v; want test-primary", active, err)
	}
	if err := AtomicSymlink(filepath.Join("profiles", "test-backup.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("AtomicSymlink swap: %v", err)
	}
	active, err := ReadActiveProfileName(cfg)
	if err != nil || active != "test-backup" {
		t.Fatalf("after swap: active=%q err=%v; want test-backup", active, err)
	}
	// rm inactive ok; rm active returns 'cannot remove active' (D-08 +
	// threat T-22-active-delete).
	if err := RemoveProfile(cfg, "test-primary"); err != nil {
		t.Errorf("RemoveProfile inactive: %v", err)
	}
	err = RemoveProfile(cfg, "test-backup")
	if err == nil || !strings.Contains(err.Error(), "cannot remove active") {
		t.Errorf("RemoveProfile active: want 'cannot remove active'; got %v", err)
	}
}

// TestExistingStateDiscovery confirms operator's pre-existing profiles +
// config.json symlink are discoverable WITHOUT prompts. READ-ONLY — no
// AddProfile, no SwitchTo, no RemoveProfile. CONTEXT.md: operator has
// primary + backup.
func TestExistingStateDiscovery(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	cfg := config.Load() // No t.Setenv — point at operator's real paths.
	profiles, err := ListProfiles(cfg)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(profiles) < 1 {
		t.Skipf("no profiles at %s", cfg.XrayProfilesDir)
	}
	active, err := ReadActiveProfileName(cfg)
	if err != nil {
		t.Fatalf("ReadActiveProfileName: %v", err)
	}
	if active == "" {
		t.Fatal("operator should have an active profile (CONTEXT.md)")
	}
	// One list entry MUST match active name AND carry Active=true.
	for _, p := range profiles {
		if p.Name == active {
			if !p.Active {
				t.Errorf("active=%q in list but Active flag false", active)
			}
			return
		}
	}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, p.Name)
	}
	t.Fatalf("active %q absent from ListProfiles %v", active, names)
}

// TestProfileLifecycleE2E asserts the validation-gate negative path: SwitchTo
// against a malformed-JSON profile returns non-nil error from `xray run -test`
// AND no symlink mutation occurs (D-10 + feedback_no_auto_state_mutation +
// TestManualRecoveryOnFailedSwitch contract). Hybrid isolation: tmp
// XRAY_CONFIG (errant swaps land there, not real symlink) + real
// XRAY_PROFILES_DIR (so container whole-dir bind sees the planted broken
// profile). t.Cleanup removes the planted file regardless of outcome.
func TestProfileLifecycleE2E(t *testing.T) {
	requireDevProxy(t)
	if ok, err := docker.BindMountIsWholeDir(config.Load()); err != nil || !ok {
		t.Skip("dev-proxy uses legacy bind; run `ws proxy down && ws proxy up` once")
	}
	realProfilesDir := filepath.Join(realXrayRoot(), "profiles")
	tmpCfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("XRAY_CONFIG", tmpCfg)
	t.Setenv("XRAY_PROFILES_DIR", realProfilesDir)
	cfg := config.Load()
	if cfg.XrayConfig != tmpCfg || cfg.XrayProfilesDir != realProfilesDir {
		t.Fatalf("config.Load ignored t.Setenv: cfg=%+v", cfg)
	}
	realSymlinkPath := filepath.Join(realXrayRoot(), "config.json")
	preTestRealLink, err := os.Readlink(realSymlinkPath)
	if err != nil {
		t.Skipf("no operator symlink at %s: %v", realSymlinkPath, err)
	}

	brokenName := "test-broken-e2e"
	brokenPath := filepath.Join(realProfilesDir, brokenName+".json")
	const malformedJSON = "{ this is not valid xray JSON --- intentional negative path"
	if err := os.WriteFile(brokenPath, []byte(malformedJSON), 0o644); err != nil {
		t.Fatalf("plant broken profile %s: %v", brokenPath, err)
	}
	t.Cleanup(func() {
		_ = os.Remove(brokenPath)
	})

	// SwitchTo MUST fail at the validation gate BEFORE any symlink swap.
	err = SwitchTo(cfg, brokenName)
	if err == nil {
		t.Fatal("SwitchTo against malformed JSON: expected validation-gate error; got nil")
	}
	if !strings.Contains(err.Error(), "xray -test failed") && !strings.Contains(err.Error(), "Validate target profile") && !strings.Contains(err.Error(), "switch to") {
		t.Errorf("SwitchTo error: want xray-validation-gate hint; got %v", err)
	}
	// Tmp symlink MUST NOT exist (validation failed pre-swap).
	if _, err := os.Lstat(tmpCfg); err == nil {
		t.Errorf("tmp XRAY_CONFIG %s created despite validation failure; D-10 violated", tmpCfg)
	}
	// Operator's real symlink MUST be byte-identical to pre-test capture.
	postTestRealLink, err := os.Readlink(realSymlinkPath)
	if err != nil {
		t.Fatalf("readlink %s post-test: %v", realSymlinkPath, err)
	}
	if postTestRealLink != preTestRealLink {
		t.Errorf("operator symlink mutated: pre=%q post=%q", preTestRealLink, postTestRealLink)
	}
}

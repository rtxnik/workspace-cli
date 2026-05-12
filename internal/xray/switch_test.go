package xray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// TestAtomicSwapSymlink asserts that observers of linkPath never see a
// missing or in-between state under concurrent AtomicSymlink writers and
// concurrent os.Readlink readers — the D-04 atomicity guarantee.
func TestAtomicSwapSymlink(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, "config.json")
	targetA := filepath.Join("profiles", "primary.json")
	targetB := filepath.Join("profiles", "backup.json")

	// Initial state: link -> targetA.
	if err := os.Symlink(targetA, linkPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 200 readers — every read MUST yield targetA or targetB; never empty,
	// never an error, never a third target value.
	wg.Add(200)
	for i := 0; i < 200; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				target, err := os.Readlink(linkPath)
				if err != nil {
					t.Errorf("Readlink failed: %v (no in-between state allowed)", err)
					return
				}
				if target != targetA && target != targetB {
					t.Errorf("Readlink returned unexpected target %q (must be A or B)", target)
					return
				}
			}
		}()
	}

	// 100 writers — alternating A and B.
	wg.Add(100)
	for i := 0; i < 100; i++ {
		i := i
		go func() {
			defer wg.Done()
			tgt := targetA
			if i%2 == 0 {
				tgt = targetB
			}
			if err := AtomicSymlink(tgt, linkPath); err != nil {
				t.Errorf("AtomicSymlink(%q): %v", tgt, err)
			}
		}()
	}

	// Give the readers time to run alongside the writers, then stop.
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

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

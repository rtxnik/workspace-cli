package xray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// mkMigrateTestCfg returns a Config whose XrayConfig + XrayProfilesDir live in
// a fresh t.TempDir() so tests never touch the operator's real ~/.config/xray.
func mkMigrateTestCfg(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	return config.Config{
		XrayConfig:      filepath.Join(root, "config.json"),
		XrayProfilesDir: filepath.Join(root, "profiles"),
	}
}

// TestAutoMigrate (PROXY-MIG-01 + PROXY-MIG-02): regular-file config.json is
// renamed to profiles/primary.json and a relative symlink is created;
// MigrateLegacy returns (true, nil). Validates both the symlink mode AND the
// relative target shape per RESEARCH §7 + memory feedback re portability.
func TestAutoMigrate(t *testing.T) {
	cfg := mkMigrateTestCfg(t)
	// Seed legacy regular-file config.json.
	const seed = `{"log":{"loglevel":"warning"}}`
	if err := os.WriteFile(cfg.XrayConfig, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	migrated, err := MigrateLegacy(cfg)
	if err != nil {
		t.Fatalf("MigrateLegacy: %v", err)
	}
	if !migrated {
		t.Fatal("want migrated=true; got false")
	}

	// profiles/primary.json must exist with the original content.
	primaryPath := filepath.Join(cfg.XrayProfilesDir, "primary.json")
	data, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read primary.json: %v", err)
	}
	if !strings.Contains(string(data), `"loglevel":"warning"`) {
		t.Errorf("primary.json missing original content; got: %s", string(data))
	}

	// config.json must now be a symlink (not regular file).
	info, err := os.Lstat(cfg.XrayConfig)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.json is not a symlink after migration")
	}

	// Symlink target must be RELATIVE (memory note: chezmoi apply / DevPod
	// rebuild portability) — exactly "profiles/primary.json".
	target, err := os.Readlink(cfg.XrayConfig)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	want := filepath.Join("profiles", "primary.json")
	if target != want {
		t.Errorf("symlink target = %q; want %q", target, want)
	}
}

// TestMigrateIdempotent (PROXY-MIG-03): re-running MigrateLegacy on the
// already-symlinked post-migration state is a true no-op (migrated=false,
// err=nil, no file mutation).
func TestMigrateIdempotent(t *testing.T) {
	cfg := mkMigrateTestCfg(t)
	if err := os.WriteFile(cfg.XrayConfig, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First pass migrates.
	migrated, err := MigrateLegacy(cfg)
	if err != nil || !migrated {
		t.Fatalf("first MigrateLegacy: migrated=%v err=%v", migrated, err)
	}

	// Capture post-migration state for mtime/inode comparison.
	primaryPath := filepath.Join(cfg.XrayProfilesDir, "primary.json")
	preInfo, err := os.Lstat(primaryPath)
	if err != nil {
		t.Fatalf("lstat primary post-migrate: %v", err)
	}

	// Second pass MUST be a no-op.
	migrated2, err := MigrateLegacy(cfg)
	if err != nil {
		t.Fatalf("second MigrateLegacy: %v", err)
	}
	if migrated2 {
		t.Fatal("second MigrateLegacy returned migrated=true; want false (idempotent)")
	}

	// primary.json modification time must not have changed (no rewrite).
	postInfo, err := os.Lstat(primaryPath)
	if err != nil {
		t.Fatalf("lstat primary post-noop: %v", err)
	}
	if !postInfo.ModTime().Equal(preInfo.ModTime()) {
		t.Errorf("primary.json mtime changed across no-op call: pre=%v post=%v",
			preInfo.ModTime(), postInfo.ModTime())
	}

	// And EnsureMigrated twice in a row also no-ops cleanly (the operator's
	// real ws-proxy-profile-* call shape).
	if err := EnsureMigrated(cfg, true); err != nil {
		t.Errorf("EnsureMigrated(allowMigrate=true) after migration: %v", err)
	}
	if err := EnsureMigrated(cfg, true); err != nil {
		t.Errorf("second EnsureMigrated(allowMigrate=true): %v", err)
	}
}

// TestMigrateConflict (PROXY-MIG-03 + RESEARCH §7 most-likely-failure-mode):
// regular-file config.json AND existing profiles/primary.json must NOT clobber
// either file; MigrateLegacy returns an error pointing at manual remediation.
func TestMigrateConflict(t *testing.T) {
	cfg := mkMigrateTestCfg(t)
	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const configSeed = `{"a":1}`
	const primarySeed = `{"b":2}`
	if err := os.WriteFile(cfg.XrayConfig, []byte(configSeed), 0o644); err != nil {
		t.Fatalf("seed config.json: %v", err)
	}
	primaryPath := filepath.Join(cfg.XrayProfilesDir, "primary.json")
	if err := os.WriteFile(primaryPath, []byte(primarySeed), 0o644); err != nil {
		t.Fatalf("seed primary.json: %v", err)
	}

	migrated, err := MigrateLegacy(cfg)
	if err == nil {
		t.Fatal("expected error on conflict; got nil")
	}
	if migrated {
		t.Error("expected migrated=false on conflict; got true")
	}
	if !strings.Contains(err.Error(), "cannot migrate") {
		t.Errorf("expected 'cannot migrate' in error; got: %v", err)
	}

	// NEITHER file mutated — bytes must match seeds verbatim.
	if data, _ := os.ReadFile(cfg.XrayConfig); string(data) != configSeed {
		t.Errorf("config.json mutated by failed migration: got %q want %q", string(data), configSeed)
	}
	if data, _ := os.ReadFile(primaryPath); string(data) != primarySeed {
		t.Errorf("primary.json mutated by failed migration: got %q want %q", string(data), primarySeed)
	}

	// config.json must remain a regular file (NOT replaced by symlink).
	info, err := os.Lstat(cfg.XrayConfig)
	if err != nil {
		t.Fatalf("lstat config.json: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("config.json was turned into a symlink despite conflict")
	}
}

// TestNoMigrateFlag (PROXY-MIG-02 + memory feedback_no_auto_state_mutation):
// EnsureMigrated(cfg, false) against a regular-file config.json returns an
// error referencing --no-migrate, and the file is NOT mutated.
func TestNoMigrateFlag(t *testing.T) {
	cfg := mkMigrateTestCfg(t)
	if err := os.WriteFile(cfg.XrayConfig, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := EnsureMigrated(cfg, false)
	if err == nil {
		t.Fatal("expected error with --no-migrate set against regular-file config.json")
	}
	if !strings.Contains(err.Error(), "--no-migrate") {
		t.Errorf("expected '--no-migrate' in error; got: %v", err)
	}

	// config.json must remain a regular file (no auto-migration occurred).
	info, err := os.Lstat(cfg.XrayConfig)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("config.json was migrated despite --no-migrate")
	}
	if !info.Mode().IsRegular() {
		t.Errorf("config.json mode unexpectedly changed: mode=%s", info.Mode())
	}

	// And no profiles/primary.json should have appeared.
	if _, err := os.Lstat(filepath.Join(cfg.XrayProfilesDir, "primary.json")); err == nil {
		t.Error("profiles/primary.json was created despite --no-migrate")
	}
}

// TestEnsureMigratedFreshInstall: no config.json at all. EnsureMigrated returns
// nil under BOTH allowMigrate=true and allowMigrate=false (fresh install is
// not an error in either mode — `profile add` will create config.json later).
// Also covers the symlink-already-present branch.
func TestEnsureMigratedFreshInstall(t *testing.T) {
	cfg := mkMigrateTestCfg(t)
	if err := EnsureMigrated(cfg, true); err != nil {
		t.Errorf("EnsureMigrated(allowMigrate=true) fresh install: %v", err)
	}
	if err := EnsureMigrated(cfg, false); err != nil {
		t.Errorf("EnsureMigrated(allowMigrate=false) fresh install: %v", err)
	}

	// Branch coverage: pre-existing symlink at config.json — both modes no-op.
	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.XrayProfilesDir, "primary.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	if err := EnsureMigrated(cfg, true); err != nil {
		t.Errorf("EnsureMigrated(allowMigrate=true) already-symlink: %v", err)
	}
	if err := EnsureMigrated(cfg, false); err != nil {
		t.Errorf("EnsureMigrated(allowMigrate=false) already-symlink: %v", err)
	}
}

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// assertLogLevel fails the test unless the file at path is valid JSON whose
// log.loglevel equals want.
func assertLogLevel(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config at %s is not valid JSON after rewrite: %v", path, err)
	}
	log, ok := cfg["log"].(map[string]any)
	if !ok {
		t.Fatalf("config at %s has no log section: %s", path, data)
	}
	if got := log["loglevel"]; got != want {
		t.Errorf("loglevel = %v, want %q", got, want)
	}
}

// assertOnlyEntries fails the test if dir contains anything besides want —
// catching leaked temp files from a non-atomic or interrupted write.
func assertOnlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
		if !wantSet[e.Name()] {
			t.Errorf("unexpected entry %q in %s (temp remnant?)", e.Name(), dir)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("dir %s has entries %v, want exactly %v", dir, got, want)
	}
}

// TestSetXrayLogLevelPreservesSymlink pins the D-07 contract: cfg.XrayConfig
// is the active-profile symlink (config.json -> profiles/<name>.json), and
// setXrayLogLevel must update the profile file THROUGH the symlink without
// ever replacing the symlink itself with a regular file. An atomic temp+rename
// write aimed at the symlink path instead of its resolved target destroys the
// active-profile pointer — this test fails in that case.
func TestSetXrayLogLevelPreservesSymlink(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	profilePath := filepath.Join(profilesDir, "primary.json")
	if err := os.WriteFile(profilePath, []byte(`{"log":{"loglevel":"warning"},"outbounds":[]}`), 0o600); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	linkPath := filepath.Join(root, "config.json")
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), linkPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	if err := setXrayLogLevel(linkPath, "debug"); err != nil {
		t.Fatalf("setXrayLogLevel: %v", err)
	}

	// The symlink must survive as a symlink with an unchanged target.
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.json is no longer a symlink (mode=%s); active-profile pointer destroyed", info.Mode())
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if want := filepath.Join("profiles", "primary.json"); target != want {
		t.Errorf("symlink target = %q, want %q", target, want)
	}

	// The resolved profile file carries the new level, stays valid JSON, 0600.
	assertLogLevel(t, profilePath, "debug")
	pinfo, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("stat profile: %v", err)
	}
	if pinfo.Mode().Perm() != 0o600 {
		t.Errorf("profile perm = %o, want 600", pinfo.Mode().Perm())
	}

	// No temp remnants in either directory.
	assertOnlyEntries(t, root, "config.json", "profiles")
	assertOnlyEntries(t, profilesDir, "primary.json")
}

// TestSetXrayLogLevelLegacyRegularFile covers the pre-migration layout where
// config.json is still a regular file: the rewrite must happen in place (the
// file stays a regular file), and a missing log section must be created.
func TestSetXrayLogLevelLegacyRegularFile(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := setXrayLogLevel(cfgPath, "debug"); err != nil {
		t.Fatalf("setXrayLogLevel: %v", err)
	}
	info, err := os.Lstat(cfgPath)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("config.json mode = %s, want regular file", info.Mode())
	}
	assertLogLevel(t, cfgPath, "debug")
	assertOnlyEntries(t, root, "config.json")
}

// TestSetXrayLogLevelMissingConfig: a missing file and a dangling symlink must
// both surface an error and never silently create a config.
func TestSetXrayLogLevelMissingConfig(t *testing.T) {
	root := t.TempDir()
	if err := setXrayLogLevel(filepath.Join(root, "config.json"), "debug"); err == nil {
		t.Fatal("expected error for missing config, got nil")
	}
	link := filepath.Join(root, "dangling.json")
	if err := os.Symlink(filepath.Join("profiles", "gone.json"), link); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	if err := setXrayLogLevel(link, "debug"); err == nil {
		t.Fatal("expected error for dangling symlink, got nil")
	}
	// The failed calls created nothing.
	assertOnlyEntries(t, root, "dangling.json")
}

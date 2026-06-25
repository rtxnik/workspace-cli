package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetUpgradeConfigFlags clears the stateful --no-migrate flag on the shared
// global command so values don't bleed across Execute() calls (same pattern as
// resetProfileUseFlags in proxy_profile_test.go).
func resetUpgradeConfigFlags(t *testing.T) {
	t.Helper()
	if f := proxyUpgradeConfigCmd.Flags().Lookup("no-migrate"); f != nil {
		_ = f.Value.Set("false")
		f.Changed = false
	}
}

// A legacy single-file config.json with a vless proxy outbound and a
// pre-tproxy inbound (no streamSettings.sockopt) — upgrade has real work.
const legacyConfigJSON = `{
  "log": {"loglevel": "warning"},
  "inbounds": [
    {"tag": "transparent", "port": 12345, "protocol": "dokodemo-door",
     "settings": {"network": "tcp,udp", "followRedirect": true}}
  ],
  "outbounds": [
    {"tag": "proxy-1", "protocol": "vless",
     "settings": {"vnext": [{"address": "example.com", "port": 443, "users": [{"id": "uid-1"}]}]}},
    {"tag": "direct", "protocol": "freedom", "settings": {}}
  ],
  "routing": {"domainStrategy": "IPIfNonMatch", "rules": []}
}`

func TestUpgradeConfigMigratesLegacy(t *testing.T) {
	resetUpgradeConfigFlags(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	profilesDir := filepath.Join(dir, "profiles")
	t.Setenv("XRAY_CONFIG", cfgPath)
	t.Setenv("XRAY_PROFILES_DIR", profilesDir)

	// Seed legacy regular-file config.json.
	if err := os.WriteFile(cfgPath, []byte(legacyConfigJSON), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := execCapture(t, "proxy", "upgrade-config")
	if err != nil {
		t.Fatalf("upgrade-config returned error: %v", err)
	}

	// config.json must now be a symlink (migrated), not a regular file.
	info, err := os.Lstat(cfgPath)
	if err != nil {
		t.Fatalf("lstat config.json: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.json is not a symlink after upgrade-config (migration did not run)")
	}

	// profiles/primary.json must exist and now carry sockopt.tproxy (upgraded).
	primary := filepath.Join(profilesDir, "primary.json")
	data, err := os.ReadFile(primary)
	if err != nil {
		t.Fatalf("read primary.json: %v", err)
	}
	if !strings.Contains(string(data), `"tproxy": "tproxy"`) {
		t.Errorf("primary.json was not upgraded (no sockopt.tproxy):\n%s", string(data))
	}
}

func TestUpgradeConfigNoMigrateErrorsWithoutMutation(t *testing.T) {
	resetUpgradeConfigFlags(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	profilesDir := filepath.Join(dir, "profiles")
	t.Setenv("XRAY_CONFIG", cfgPath)
	t.Setenv("XRAY_PROFILES_DIR", profilesDir)

	if err := os.WriteFile(cfgPath, []byte(legacyConfigJSON), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := execCapture(t, "proxy", "upgrade-config", "--no-migrate")
	if err == nil {
		t.Fatal("expected error with --no-migrate against a regular-file config.json")
	}

	// State must be UNMUTATED: still a regular file, no profiles/primary.json.
	info, lerr := os.Lstat(cfgPath)
	if lerr != nil {
		t.Fatalf("lstat: %v", lerr)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("config.json was mutated under --no-migrate (mode=%s)", info.Mode())
	}
	if _, serr := os.Stat(filepath.Join(profilesDir, "primary.json")); serr == nil {
		t.Error("profiles/primary.json was created under --no-migrate")
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Clear all config env vars to test defaults.
	for _, key := range []string{"WORKSPACES_DIR", "PROFILES_DIR", "SHARED_DIR", "XRAY_CONFIG"} {
		t.Setenv(key, "")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	cfg := Load()

	want := map[string]string{
		"WorkspacesDir": filepath.Join(home, "workspaces"),
		"ProfilesDir":   filepath.Join(home, ".config", "workspaces", "profiles"),
		"SharedDir":     filepath.Join(home, ".config", "workspaces", "shared"),
		"XrayConfig":    filepath.Join(home, ".config", "xray", "config.json"),
	}

	got := map[string]string{
		"WorkspacesDir": cfg.WorkspacesDir,
		"ProfilesDir":   cfg.ProfilesDir,
		"SharedDir":     cfg.SharedDir,
		"XrayConfig":    cfg.XrayConfig,
	}

	for field, wantVal := range want {
		if got[field] != wantVal {
			t.Errorf("%s = %q, want %q", field, got[field], wantVal)
		}
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	overrides := map[string]struct {
		envKey string
		value  string
		field  func(Config) string
	}{
		"WorkspacesDir": {
			envKey: "WORKSPACES_DIR",
			value:  "/tmp/custom-workspaces",
			field:  func(c Config) string { return c.WorkspacesDir },
		},
		"ProfilesDir": {
			envKey: "PROFILES_DIR",
			value:  "/tmp/custom-profiles",
			field:  func(c Config) string { return c.ProfilesDir },
		},
		"SharedDir": {
			envKey: "SHARED_DIR",
			value:  "/tmp/custom-shared",
			field:  func(c Config) string { return c.SharedDir },
		},
		"XrayConfig": {
			envKey: "XRAY_CONFIG",
			value:  "/tmp/custom-xray.json",
			field:  func(c Config) string { return c.XrayConfig },
		},
	}

	for name, tt := range overrides {
		t.Run(name, func(t *testing.T) {
			t.Setenv(tt.envKey, tt.value)
			cfg := Load()
			if got := tt.field(cfg); got != tt.value {
				t.Errorf("%s = %q, want %q", name, got, tt.value)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	if ProxyContainer != "dev-proxy" {
		t.Errorf("ProxyContainer = %q, want %q", ProxyContainer, "dev-proxy")
	}
	if ProxyImage != "devpod-proxy" {
		t.Errorf("ProxyImage = %q, want %q", ProxyImage, "devpod-proxy")
	}
}

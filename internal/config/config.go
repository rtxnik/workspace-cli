package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	WorkspacesDir  string
	ProfilesDir    string
	SharedDir      string
	XrayConfig     string
	ProxyContainer string
	ProxyImage     string
}

func Load() Config {
	home, _ := os.UserHomeDir()

	return Config{
		WorkspacesDir:  envOr("WORKSPACES_DIR", filepath.Join(home, "workspaces")),
		ProfilesDir:    envOr("PROFILES_DIR", filepath.Join(home, ".config", "workspaces", "profiles")),
		SharedDir:      envOr("SHARED_DIR", filepath.Join(home, ".config", "workspaces", "shared")),
		XrayConfig:     envOr("XRAY_CONFIG", filepath.Join(home, ".config", "xray", "config.json")),
		ProxyContainer: envOr("WS_PROXY_CONTAINER", "dev-proxy"),
		ProxyImage:     envOr("WS_PROXY_IMAGE", "devpod-proxy"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

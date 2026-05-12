package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	WorkspacesDir   string
	ProfilesDir     string
	SharedDir       string
	XrayConfig      string
	XrayProfilesDir string
	ProxyContainer  string
	ProxyImage      string
	ProxyNetwork    string
	ProxySubnet     string
	ProxyIP         string
}

func Load() Config {
	home, _ := os.UserHomeDir()

	xrayCfg := envOr("XRAY_CONFIG", filepath.Join(home, ".config", "xray", "config.json"))
	xrayProfiles := envOr("XRAY_PROFILES_DIR", filepath.Join(filepath.Dir(xrayCfg), "profiles"))

	return Config{
		WorkspacesDir:   envOr("WORKSPACES_DIR", filepath.Join(home, "workspaces")),
		ProfilesDir:     envOr("PROFILES_DIR", filepath.Join(home, ".config", "workspaces", "profiles")),
		SharedDir:       envOr("SHARED_DIR", filepath.Join(home, ".config", "workspaces", "shared")),
		XrayConfig:      xrayCfg,
		XrayProfilesDir: xrayProfiles,
		ProxyContainer:  envOr("WS_PROXY_CONTAINER", "dev-proxy"),
		ProxyImage:      envOr("WS_PROXY_IMAGE", "devpod-proxy"),
		ProxyNetwork:    envOr("WS_PROXY_NETWORK", "ws-proxy"),
		ProxySubnet:     envOr("WS_PROXY_SUBNET", "172.28.0.0/16"),
		ProxyIP:         envOr("WS_PROXY_IP", "172.28.0.2"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

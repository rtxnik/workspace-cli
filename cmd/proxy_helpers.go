package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/fsutil"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// xrayConfigRoots is the set of directories a legitimate xray-config write may
// land in: the config file's own directory and the profiles directory (which
// XRAY_PROFILES_DIR may place outside the config directory).
func xrayConfigRoots(cfg config.Config) []string {
	return []string{filepath.Dir(cfg.XrayConfig), cfg.XrayProfilesDir}
}

// setXrayLogLevel rewrites the loglevel of the active xray config.
//
// cfg.XrayConfig is normally the active-profile symlink (config.json ->
// profiles/<name>.json, D-07 layout). The path is resolved BEFORE writing so
// the write lands on the resolved profile file and the symlink is preserved;
// resolution refuses a target that escapes the config directories. A missing or
// dangling config surfaces an error and creates nothing (the read below fails
// before any write).
func setXrayLogLevel(cfg config.Config, level string) error {
	resolved, _, err := xrayconf.ResolveConfigTarget(cfg.XrayConfig, xrayConfigRoots(cfg))
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	log, ok := m["log"].(map[string]any)
	if !ok {
		log = make(map[string]any)
		m["log"] = log
	}
	log["loglevel"] = level

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return fsutil.WriteFile(resolved, out, 0o600)
}

// xrayReleaseURL is the GitHub API endpoint for the latest Xray-core release.
// A package var (not a const) so tests can point the fetch at a local server
// with no live-network dependency.
var xrayReleaseURL = "https://api.github.com/repos/XTLS/Xray-core/releases/latest"

// xrayVersionFetchTimeout bounds the latest-version lookup so an unresponsive
// GitHub (or a network black hole) fails fast instead of hanging
// `ws proxy update` indefinitely. A package var so tests can shrink it.
var xrayVersionFetchTimeout = 10 * time.Second

func fetchLatestXrayVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), xrayVersionFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xrayReleaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("no tag found in latest release")
	}
	return release.TagName, nil
}

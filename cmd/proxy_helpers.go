package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rtxnik/workspace-cli/internal/fsutil"
)

// setXrayLogLevel rewrites the loglevel of the active xray config.
//
// configPath is normally the active-profile symlink (~/.config/xray/config.json
// -> profiles/<name>.json, D-07 layout). The path is resolved BEFORE writing:
// fsutil.WriteFile renames a temp file over the path it is given; aimed at
// the symlink itself, that write would replace the symlink with a regular
// file and silently destroy the active-profile pointer. Writing the resolved target
// keeps the pointer intact, and the rename keeps the rewrite atomic for the
// running proxy that reads the same file through the whole-directory bind
// mount (a plain os.WriteFile here risked a torn read).
func setXrayLogLevel(configPath, level string) error {
	resolved, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	log, ok := cfg["log"].(map[string]any)
	if !ok {
		log = make(map[string]any)
		cfg["log"] = log
	}
	log["loglevel"] = level

	out, err := json.MarshalIndent(cfg, "", "  ")
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

package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func setXrayLogLevel(configPath, level string) error {
	data, err := os.ReadFile(configPath)
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

	return os.WriteFile(configPath, out, 0o644)
}

func fetchLatestXrayVersion() (string, error) {
	resp, err := http.Get("https://api.github.com/repos/XTLS/Xray-core/releases/latest")
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

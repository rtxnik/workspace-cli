package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// proxyInitCmd must accept an uppercase scheme and write a valid config.
// Run() only calls os.Exit on the error branch, so the happy path returns
// normally; a pre-fix binary would Die here. Output is redirected to keep the
// test log pristine.
func TestL3_lowB_ProxyInitAcceptsUppercaseScheme(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("XRAY_CONFIG", cfgPath)

	oldOut, oldErr := os.Stdout, os.Stderr
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	os.Stdout, os.Stderr = devnull, devnull
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr; _ = devnull.Close() }()

	proxyInitCmd.Run(proxyInitCmd, []string{"HY2://pw@h.example:443?sni=h.example"})

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written for uppercase scheme: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("written config is not valid JSON: %v", err)
	}
	if _, ok := v["outbounds"]; !ok {
		t.Errorf("written config missing outbounds: %s", data)
	}
}

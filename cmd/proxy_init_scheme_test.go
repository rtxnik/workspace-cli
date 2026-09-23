package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// proxyInitCmd must accept an uppercase scheme and write a valid config.
// A pre-fix binary returned "unsupported URI scheme" here. Output is
// redirected to keep the test log pristine.
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

	if err := proxyInitCmd.RunE(proxyInitCmd, []string{"HY2://pw@h.example:443?sni=h.example"}); err != nil {
		t.Fatalf("init on an uppercase scheme: %v", err)
	}

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

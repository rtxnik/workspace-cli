package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyInitHysteria2(t *testing.T) {
	dir := t.TempDir()
	xrayConfig := filepath.Join(dir, "config.json")
	t.Setenv("XRAY_CONFIG", xrayConfig) // proxyInitCmd.Run calls config.Load(), which honors XRAY_CONFIG
	out, _, err := execCapture(t, "proxy", "init", "hysteria2://pw@h.example:443?sni=h.example")
	if err != nil {
		t.Fatalf("init: %v (%s)", err, out)
	}
	data, rerr := os.ReadFile(xrayConfig)
	if rerr != nil {
		t.Fatalf("read config: %v", rerr)
	}
	if !strings.Contains(string(data), `"protocol": "hysteria"`) {
		t.Errorf("init did not write a hysteria config:\n%s", data)
	}
}

// TestProxyInitHasNoAddFlag guards the v0.9.0 removal of the deprecated
// `ws proxy init --add` flag: it must no longer be registered on the command.
func TestProxyInitHasNoAddFlag(t *testing.T) {
	if f := proxyInitCmd.Flags().Lookup("add"); f != nil {
		t.Fatalf("`ws proxy init` still registers the removed --add flag: %+v", f)
	}
}

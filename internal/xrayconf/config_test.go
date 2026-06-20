package xrayconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestAssembleConfig verifies the structural scaffold produced by AssembleConfig.
func TestAssembleConfig(t *testing.T) {
	proxy := Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		Settings: json.RawMessage(`{"vnext":[]}`),
	}
	xc := AssembleConfig(proxy)

	if xc.Log.Level != "warning" {
		t.Errorf("log level = %q, want %q", xc.Log.Level, "warning")
	}
	if len(xc.Inbounds) != 1 {
		t.Fatalf("inbounds count = %d, want 1", len(xc.Inbounds))
	}
	if xc.Inbounds[0].Protocol != "dokodemo-door" {
		t.Errorf("inbound protocol = %q, want %q", xc.Inbounds[0].Protocol, "dokodemo-door")
	}
	if xc.Inbounds[0].Port != 12345 {
		t.Errorf("inbound port = %d, want %d", xc.Inbounds[0].Port, 12345)
	}
	if len(xc.Outbounds) != 2 {
		t.Fatalf("outbounds count = %d, want 2", len(xc.Outbounds))
	}
	if xc.Outbounds[0].Tag != "proxy-1" {
		t.Errorf("first outbound tag = %q, want %q", xc.Outbounds[0].Tag, "proxy-1")
	}
	if xc.Outbounds[1].Tag != "direct" {
		t.Errorf("second outbound tag = %q, want %q", xc.Outbounds[1].Tag, "direct")
	}
	if len(xc.Routing.Balancers) != 1 {
		t.Fatalf("balancers count = %d, want 1", len(xc.Routing.Balancers))
	}
	if xc.Routing.Balancers[0].Tag != "proxy-balancer" {
		t.Errorf("balancer tag = %q, want %q", xc.Routing.Balancers[0].Tag, "proxy-balancer")
	}
}

// TestAssembleGolden is the byte-identical guard: AssembleConfig of a fixed
// vless outbound must marshal to the bytes in testdata/assemble_vless.golden.json.
// Run with -update to regenerate the golden file.
func TestAssembleGolden(t *testing.T) {
	proxy := Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		Settings: json.RawMessage(`{"vnext":[{"address":"example.com","port":443,"users":[{"id":"test-uuid-1234","encryption":"none","flow":"xtls-rprx-vision"}]}]}`),
		StreamSettings: json.RawMessage(`{"network":"tcp","security":"reality","realitySettings":{"serverName":"www.google.com","fingerprint":"chrome","publicKey":"pub-key-123","shortId":"ab","spiderX":"/"}}`),
	}
	xc := AssembleConfig(proxy)

	got, err := json.MarshalIndent(xc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "assemble_vless.golden.json")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden updated: %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to generate)", goldenPath, err)
	}

	if string(got) != string(want) {
		t.Errorf("AssembleConfig output differs from golden.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestWriteConfig verifies WriteConfig creates the file with correct perms.
func TestWriteConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")

	xc := AssembleConfig(Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		Settings: json.RawMessage(`{}`),
	})
	if err := WriteConfig(path, xc); err != nil {
		t.Fatalf("WriteConfig() error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms = %o, want 600", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var check XrayConfig
	if err := json.Unmarshal(data, &check); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if check.Log.Level != "warning" {
		t.Errorf("log level = %q, want %q", check.Log.Level, "warning")
	}
}

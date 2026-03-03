package vless

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func tcpRealityCfg() VLESSConfig {
	return VLESSConfig{
		UUID:       "test-uuid-1234",
		Address:    "example.com",
		Port:       443,
		Encryption: "none",
		Flow:       "xtls-rprx-vision",
		Network:    "tcp",
		Security:   "reality",
		SNI:        "www.google.com",
		Fp:         "chrome",
		PublicKey:  "pub-key-123",
		ShortID:    "ab",
		SpiderX:    "/",
	}
}

func wsTLSCfg() VLESSConfig {
	return VLESSConfig{
		UUID:       "test-uuid-ws",
		Address:    "ws.example.com",
		Port:       443,
		Encryption: "none",
		Network:    "ws",
		Security:   "tls",
		SNI:        "ws.example.com",
		Fp:         "firefox",
		Host:       "ws.example.com",
		Path:       "/vless-ws",
	}
}

func TestGenerateConfig(t *testing.T) {
	cfg := tcpRealityCfg()

	xray, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		t.Fatalf("GenerateConfig() error: %v", err)
	}

	// Verify structure.
	if xray.Log.Level != "warning" {
		t.Errorf("log level = %q, want %q", xray.Log.Level, "warning")
	}
	if len(xray.Inbounds) != 1 {
		t.Fatalf("inbounds count = %d, want 1", len(xray.Inbounds))
	}
	if xray.Inbounds[0].Protocol != "dokodemo-door" {
		t.Errorf("inbound protocol = %q, want %q", xray.Inbounds[0].Protocol, "dokodemo-door")
	}
	if xray.Inbounds[0].Port != 12345 {
		t.Errorf("inbound port = %d, want %d", xray.Inbounds[0].Port, 12345)
	}

	// Outbounds: proxy + direct.
	if len(xray.Outbounds) != 2 {
		t.Fatalf("outbounds count = %d, want 2", len(xray.Outbounds))
	}
	if xray.Outbounds[0].Tag != "proxy-1" {
		t.Errorf("first outbound tag = %q, want %q", xray.Outbounds[0].Tag, "proxy-1")
	}
	if xray.Outbounds[0].Protocol != "vless" {
		t.Errorf("first outbound protocol = %q, want %q", xray.Outbounds[0].Protocol, "vless")
	}
	if xray.Outbounds[1].Tag != "direct" {
		t.Errorf("second outbound tag = %q, want %q", xray.Outbounds[1].Tag, "direct")
	}

	// Routing has balancer.
	if len(xray.Routing.Balancers) != 1 {
		t.Fatalf("balancers count = %d, want 1", len(xray.Routing.Balancers))
	}
	if xray.Routing.Balancers[0].Tag != "proxy-balancer" {
		t.Errorf("balancer tag = %q, want %q", xray.Routing.Balancers[0].Tag, "proxy-balancer")
	}

	// Result must be valid JSON.
	data, err := json.Marshal(xray)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	if !json.Valid(data) {
		t.Error("generated config is not valid JSON")
	}
}

func TestGenerateConfigTransports(t *testing.T) {
	tests := []struct {
		name    string
		cfg     VLESSConfig
		wantKey string
	}{
		{
			name:    "tcp-reality has realitySettings",
			cfg:     tcpRealityCfg(),
			wantKey: "realitySettings",
		},
		{
			name:    "ws-tls has wsSettings",
			cfg:     wsTLSCfg(),
			wantKey: "wsSettings",
		},
		{
			name: "grpc has grpcSettings",
			cfg: VLESSConfig{
				UUID: "u", Address: "a", Port: 443,
				Encryption: "none", Network: "grpc",
				Security: "reality", SNI: "sni", Fp: "chrome",
				PublicKey: "pk", ShortID: "s",
				ServiceName: "svc",
			},
			wantKey: "grpcSettings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xray, err := GenerateConfig(tt.cfg, "proxy-1")
			if err != nil {
				t.Fatalf("GenerateConfig() error: %v", err)
			}
			var ss map[string]any
			if err := json.Unmarshal(xray.Outbounds[0].StreamSettings, &ss); err != nil {
				t.Fatalf("unmarshal stream settings: %v", err)
			}
			if _, ok := ss[tt.wantKey]; !ok {
				t.Errorf("stream settings missing key %q, got keys: %v", tt.wantKey, keys(ss))
			}
		})
	}
}

func TestWriteNewConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.json")

	cfg := tcpRealityCfg()
	if err := WriteNewConfig(path, cfg); err != nil {
		t.Fatalf("WriteNewConfig() error: %v", err)
	}

	// File must exist.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("file permissions = %o, want 644", info.Mode().Perm())
	}

	// File must be valid JSON with expected structure.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var xray XrayConfig
	if err := json.Unmarshal(data, &xray); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if len(xray.Outbounds) != 2 {
		t.Errorf("outbounds count = %d, want 2", len(xray.Outbounds))
	}
	if xray.Outbounds[0].Tag != "proxy-1" {
		t.Errorf("first outbound tag = %q, want %q", xray.Outbounds[0].Tag, "proxy-1")
	}
}

func TestAddNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Create initial config with one proxy node.
	initial := tcpRealityCfg()
	if err := WriteNewConfig(path, initial); err != nil {
		t.Fatalf("WriteNewConfig() error: %v", err)
	}

	// Add a second node.
	second := wsTLSCfg()
	if err := AddNode(path, second); err != nil {
		t.Fatalf("AddNode() error: %v", err)
	}

	// Read and verify.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var xray XrayConfig
	if err := json.Unmarshal(data, &xray); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	// Expected order: proxy-1, proxy-2, direct.
	if len(xray.Outbounds) != 3 {
		t.Fatalf("outbounds count = %d, want 3", len(xray.Outbounds))
	}
	wantTags := []string{"proxy-1", "proxy-2", "direct"}
	for i, want := range wantTags {
		if xray.Outbounds[i].Tag != want {
			t.Errorf("outbound[%d].Tag = %q, want %q", i, xray.Outbounds[i].Tag, want)
		}
	}

	// Direct must be last.
	last := xray.Outbounds[len(xray.Outbounds)-1]
	if last.Protocol != "freedom" {
		t.Errorf("last outbound protocol = %q, want %q", last.Protocol, "freedom")
	}
}

func TestAddNodeThirdProxy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Build config with two proxies.
	if err := WriteNewConfig(path, tcpRealityCfg()); err != nil {
		t.Fatal(err)
	}
	if err := AddNode(path, wsTLSCfg()); err != nil {
		t.Fatal(err)
	}

	// Add third node.
	third := VLESSConfig{
		UUID: "u3", Address: "grpc.example.com", Port: 443,
		Encryption: "none", Network: "grpc",
		Security: "reality", SNI: "sni", Fp: "chrome",
		PublicKey: "pk", ShortID: "s",
		ServiceName: "svc",
	}
	if err := AddNode(path, third); err != nil {
		t.Fatalf("AddNode() third: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var xray XrayConfig
	if err := json.Unmarshal(data, &xray); err != nil {
		t.Fatal(err)
	}

	// Expected: proxy-1, proxy-2, proxy-3, direct.
	if len(xray.Outbounds) != 4 {
		t.Fatalf("outbounds count = %d, want 4", len(xray.Outbounds))
	}
	if xray.Outbounds[2].Tag != "proxy-3" {
		t.Errorf("outbound[2].Tag = %q, want %q", xray.Outbounds[2].Tag, "proxy-3")
	}
	if xray.Outbounds[3].Tag != "direct" {
		t.Errorf("last outbound must be 'direct', got %q", xray.Outbounds[3].Tag)
	}
}

func TestAddNodeMissingFile(t *testing.T) {
	err := AddNode("/nonexistent/config.json", tcpRealityCfg())
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func keys(m map[string]any) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}

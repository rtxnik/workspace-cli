package vless

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/xrayconf"
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

// TestGenerateConfigH2MigratesToXHTTP guards the xray v26 h2 fast-follow (see
// workspace-meta seeds): xray v26 removed the HTTP/2 transport ("migrated to
// XHTTP stream-one H2 & H3"), so a parsed type=h2 URI must generate an xhttp
// stream-one config — never network:"h2" with httpSettings, which a current
// xray rejects. The parser still yields Network:"h2" for back-compat import;
// the migration happens at generation.
func TestGenerateConfigH2MigratesToXHTTP(t *testing.T) {
	cfg := VLESSConfig{
		UUID: "11111111-1111-1111-1111-111111111111", Address: "h2.example.com", Port: 443,
		Encryption: "none", Network: "h2",
		Security: "tls", SNI: "h2.example.com", Fp: "chrome",
		Host: "h2.example.com", Path: "/h2path",
	}
	xray, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		t.Fatalf("GenerateConfig() error: %v", err)
	}
	var ss map[string]any
	if err := json.Unmarshal(xray.Outbounds[0].StreamSettings, &ss); err != nil {
		t.Fatalf("unmarshal stream settings: %v", err)
	}
	if ss["network"] != "xhttp" {
		t.Errorf("network = %v, want xhttp (h2 migrated)", ss["network"])
	}
	if _, ok := ss["httpSettings"]; ok {
		t.Errorf("httpSettings must not be emitted — xray v26 rejects the h2 transport")
	}
	xh, ok := ss["xhttpSettings"].(map[string]any)
	if !ok {
		t.Fatalf("xhttpSettings missing/wrong type: %v", ss["xhttpSettings"])
	}
	if xh["mode"] != "stream-one" {
		t.Errorf("xhttpSettings.mode = %v, want stream-one", xh["mode"])
	}
	if xh["path"] != "/h2path" {
		t.Errorf("xhttpSettings.path = %v, want /h2path", xh["path"])
	}
	if xh["host"] != "h2.example.com" {
		t.Errorf("xhttpSettings.host = %v, want h2.example.com", xh["host"])
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
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file permissions = %o, want 600", info.Mode().Perm())
	}

	// File must be valid JSON with expected structure.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var xray xrayconf.XrayConfig
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

func keys(m map[string]any) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}

// TestRoutingCopyPreservesAllFields is the regression for the D-05 routing
// copy bug: AddProfile's unmarshal -> marshal round-trip through
// xrayconf.XrayConfig must preserve every field xray honours on a routing rule.
// The pre-refactor `Rule` struct silently dropped fields it did not declare
// (port, domain, source, user, inboundTag, protocol, attrs, sourcePort) —
// this test fails on the typed-struct version and passes once Rules is
// []json.RawMessage (byte-passthrough, matching the Outbound.Settings
// pattern already established in this file).
func TestRoutingCopyPreservesAllFields(t *testing.T) {
	input := []byte(`{
  "log": {"loglevel": "warning"},
  "inbounds": [],
  "outbounds": [],
  "routing": {
    "domainStrategy": "IPIfNonMatch",
    "rules": [
      {"type":"field","port":"22","outboundTag":"direct"},
      {"type":"field","domain":["example.com","geosite:google"],"outboundTag":"proxy"},
      {"type":"field","source":["10.0.0.0/8"],"outboundTag":"direct"},
      {"type":"field","user":["alice@example.com"],"outboundTag":"proxy"},
      {"type":"field","inboundTag":["transparent"],"network":"tcp,udp","balancerTag":"proxy-balancer"},
      {"type":"field","protocol":["bittorrent"],"outboundTag":"block"},
      {"type":"field","attrs":{"user-agent":"curl"},"outboundTag":"direct"},
      {"type":"field","sourcePort":"1024-65535","outboundTag":"direct"},
      {"type":"field","ip":["10.0.0.0/8","172.16.0.0/12"],"outboundTag":"direct"}
    ]
  }
}`)

	var xc xrayconf.XrayConfig
	if err := json.Unmarshal(input, &xc); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	out, err := json.MarshalIndent(&xc, "", "  ")
	if err != nil {
		t.Fatalf("marshal round-trip: %v", err)
	}

	// Re-parse both byte streams into generic maps so the comparison is
	// tolerant of whitespace / key order but strict on field presence + value.
	var inMap, outMap map[string]any
	if err := json.Unmarshal(input, &inMap); err != nil {
		t.Fatalf("reparse input: %v", err)
	}
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("reparse output: %v", err)
	}

	inRules, ok := nestedSlice(inMap, "routing", "rules")
	if !ok {
		t.Fatalf("input routing.rules not extractable: %#v", inMap)
	}
	outRules, ok := nestedSlice(outMap, "routing", "rules")
	if !ok {
		t.Fatalf("output routing.rules not extractable: %#v", outMap)
	}

	if !reflect.DeepEqual(inRules, outRules) {
		t.Errorf("routing.rules byte-fidelity broken: fields dropped or mutated by Unmarshal->Marshal round-trip.\n  input:  %#v\n  output: %#v", inRules, outRules)
	}
}

// nestedSlice walks m via the given keys and returns the value as []any.
func nestedSlice(m map[string]any, keys ...string) ([]any, bool) {
	var cur any = m
	for _, k := range keys {
		asMap, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = asMap[k]
	}
	s, ok := cur.([]any)
	return s, ok
}

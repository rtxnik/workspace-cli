package hysteria2

import (
	"encoding/json"
	"testing"
)

func TestParseFullLink(t *testing.T) {
	uri := "hysteria2://AUTHREDACTED@example.com:443?alpn=h3&fp=chrome&obfs=salamander&obfs-password=OBFSREDACTED&security=tls&sni=example.com#hy2-test"
	cfg, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Auth != "AUTHREDACTED" {
		t.Errorf("Auth = %q, want AUTHREDACTED", cfg.Auth)
	}
	if cfg.Address != "example.com" || cfg.Port != 443 {
		t.Errorf("Address:Port = %s:%d, want example.com:443", cfg.Address, cfg.Port)
	}
	if cfg.SNI != "example.com" {
		t.Errorf("SNI = %q", cfg.SNI)
	}
	if len(cfg.ALPN) != 1 || cfg.ALPN[0] != "h3" {
		t.Errorf("ALPN = %v, want [h3]", cfg.ALPN)
	}
	if cfg.Fingerprint != "chrome" {
		t.Errorf("Fingerprint = %q", cfg.Fingerprint)
	}
	if cfg.Obfs != "salamander" || cfg.ObfsPassword != "OBFSREDACTED" {
		t.Errorf("Obfs = %q/%q", cfg.Obfs, cfg.ObfsPassword)
	}
	if cfg.AllowInsecure {
		t.Errorf("AllowInsecure = true, want false")
	}
	if cfg.Remark != "hy2-test" {
		t.Errorf("Remark = %q", cfg.Remark)
	}
}

func TestParseDefaultsAndAlias(t *testing.T) {
	cfg, err := Parse("hy2://pw@example.com:443")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SNI != "example.com" {
		t.Errorf("default SNI = %q, want example.com", cfg.SNI)
	}
	if len(cfg.ALPN) != 1 || cfg.ALPN[0] != "h3" {
		t.Errorf("default ALPN = %v, want [h3]", cfg.ALPN)
	}
	if cfg.Fingerprint != "chrome" {
		t.Errorf("default Fingerprint = %q, want chrome", cfg.Fingerprint)
	}
	if cfg.Obfs != "" {
		t.Errorf("Obfs = %q, want empty", cfg.Obfs)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{"wrong scheme", "vless://uuid@host:443"},
		{"missing auth", "hysteria2://@host:443"},
		{"bad port", "hysteria2://pw@host:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.uri); err == nil {
				t.Errorf("Parse(%q) = nil err, want error", tt.uri)
			}
		})
	}
}

func TestParsePortHopping(t *testing.T) {
	cfg, err := Parse("hysteria2://pw@host.example:443,5000-6000?sni=host.example")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Port != 443 {
		t.Errorf("base Port = %d, want 443", cfg.Port)
	}
	if !cfg.PortHopping {
		t.Errorf("PortHopping = false, want true (ranges dropped)")
	}
}

func TestGenerateConfigWithObfs(t *testing.T) {
	cfg, err := Parse("hysteria2://AUTHREDACTED@example.com:443?alpn=h3&fp=chrome&obfs=salamander&obfs-password=OBFSREDACTED&security=tls&sni=example.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	xc, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if len(xc.Outbounds) != 2 {
		t.Fatalf("outbounds = %d, want 2 (proxy + direct)", len(xc.Outbounds))
	}
	ob := xc.Outbounds[0]
	if ob.Protocol != "hysteria" || ob.Tag != "proxy-1" {
		t.Fatalf("outbound = %s/%s, want hysteria/proxy-1", ob.Protocol, ob.Tag)
	}

	var settings map[string]any
	if err := json.Unmarshal(ob.Settings, &settings); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings["version"].(float64) != 2 {
		t.Errorf("settings.version = %v, want 2", settings["version"])
	}
	if settings["address"] != "example.com" || settings["port"].(float64) != 443 {
		t.Errorf("settings address/port = %v/%v", settings["address"], settings["port"])
	}
	if _, ok := settings["auth"]; ok {
		t.Errorf("settings must NOT carry auth (it belongs in hysteriaSettings)")
	}

	var ss map[string]any
	if err := json.Unmarshal(ob.StreamSettings, &ss); err != nil {
		t.Fatalf("streamSettings: %v", err)
	}
	if ss["network"] != "hysteria" || ss["security"] != "tls" {
		t.Errorf("network/security = %v/%v", ss["network"], ss["security"])
	}
	hs := ss["hysteriaSettings"].(map[string]any)
	if hs["version"].(float64) != 2 || hs["auth"] != "AUTHREDACTED" {
		t.Errorf("hysteriaSettings = %v", hs)
	}
	fm, ok := ss["finalmask"].(map[string]any)
	if !ok {
		t.Fatalf("finalmask missing for salamander obfs")
	}
	udp := fm["udp"].([]any)
	mask := udp[0].(map[string]any)
	if mask["type"] != "salamander" {
		t.Errorf("finalmask type = %v, want salamander", mask["type"])
	}
	if mask["settings"].(map[string]any)["password"] != "OBFSREDACTED" {
		t.Errorf("finalmask password mismatch")
	}
}

func TestGenerateConfigNoObfs(t *testing.T) {
	cfg, err := Parse("hy2://pw@example.com:443")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	xc, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	var ss map[string]any
	if err := json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss); err != nil {
		t.Fatalf("streamSettings: %v", err)
	}
	if _, ok := ss["finalmask"]; ok {
		t.Errorf("finalmask must be absent when obfs is unset")
	}
}

func TestGenerateConfigPinNoInsecure(t *testing.T) {
	pinHex := "00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00"
	cfg, err := Parse("hy2://pw@example.com:443?pinSHA256=" + pinHex)
	if err != nil {
		t.Fatal(err)
	}
	xc, _ := GenerateConfig(cfg, "proxy-1")
	var ss map[string]any
	_ = json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss)
	tls := ss["tlsSettings"].(map[string]any)
	if _, ok := tls["allowInsecure"]; ok {
		t.Errorf("allowInsecure must never be emitted")
	}
	if tls["pinnedPeerCertSha256"] != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Errorf("pin = %v", tls["pinnedPeerCertSha256"])
	}
}

func TestGenerateConfigNoInsecureWithoutPin(t *testing.T) {
	cfg, err := Parse("hy2://pw@example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	xc, _ := GenerateConfig(cfg, "proxy-1")
	var ss map[string]any
	_ = json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss)
	tls := ss["tlsSettings"].(map[string]any)
	if _, ok := tls["allowInsecure"]; ok {
		t.Errorf("allowInsecure must never be emitted (even without pin)")
	}
	if _, ok := tls["pinnedPeerCertSha256"]; ok {
		t.Errorf("pinnedPeerCertSha256 must be absent when no pin provided")
	}
}

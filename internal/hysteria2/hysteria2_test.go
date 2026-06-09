package hysteria2

import "testing"

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

package xray

import "testing"

// GenerateProfileConfig must route uppercase schemes to the right parser.
func TestL3_lowB_GenerateProfileConfigSchemeCase(t *testing.T) {
	cases := []string{
		"HY2://pw@example.com:443?sni=example.com",
		"Hysteria2://pw@example.com:443?sni=example.com",
		"VLESS://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&type=tcp&security=none",
	}
	for _, uri := range cases {
		cfg, err := GenerateProfileConfig(uri)
		if err != nil {
			t.Errorf("GenerateProfileConfig(%q): %v, want accepted", uri, err)
			continue
		}
		if cfg == nil || len(cfg.Outbounds) == 0 {
			t.Errorf("GenerateProfileConfig(%q): empty config", uri)
		}
	}
	if _, err := GenerateProfileConfig("vmess://x@example.com:443"); err == nil {
		t.Errorf("GenerateProfileConfig(vmess://...) = nil err, want unsupported-scheme error")
	}
}

package hysteria2

import "testing"

// Contrast: a percent-encoded colon in the auth already survives today; it must
// keep surviving after the raw-colon fix (proves the loss is raw-colon-specific).
func TestL3_01_PercentEncodedColonSurvives(t *testing.T) {
	cfg, err := Parse("hy2://user%3Asecretpass@example.com:443?sni=example.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Auth != "user:secretpass" {
		t.Fatalf("auth = %q, want %q", cfg.Auth, "user:secretpass")
	}
}

// Contrast: the comma port-hopping form works today and must not regress.
func TestL3_02_CommaPortHoppingWorks(t *testing.T) {
	cfg, err := Parse("hy2://pw@example.com:443,5000-6000?sni=example.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Port != 443 {
		t.Errorf("base port = %d, want 443", cfg.Port)
	}
	if cfg.HopPorts != "443,5000-6000" {
		t.Errorf("HopPorts = %q, want %q", cfg.HopPorts, "443,5000-6000")
	}
	if !cfg.PortHopping {
		t.Errorf("PortHopping = false, want true")
	}
}

// Guard: IPv6 host parses (bracket-aware).
func TestL3_clean_IPv6HostParses(t *testing.T) {
	cfg, err := Parse("hy2://pw@[2001:db8::1]:443")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Address != "2001:db8::1" || cfg.Port != 443 {
		t.Errorf("addr/port = %q/%d, want 2001:db8::1/443", cfg.Address, cfg.Port)
	}
}

// Guard: IPv6 with comma port-hopping keeps its brackets and base port.
func TestL3_clean_IPv6WithHopping(t *testing.T) {
	cfg, err := Parse("hy2://pw@[2001:db8::1]:443,5000-6000")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Address != "2001:db8::1" || cfg.Port != 443 {
		t.Errorf("addr/port = %q/%d, want 2001:db8::1/443", cfg.Address, cfg.Port)
	}
	if cfg.HopPorts != "443,5000-6000" {
		t.Errorf("HopPorts = %q, want %q", cfg.HopPorts, "443,5000-6000")
	}
}

// Guard: a comma inside the query string must never be read as port hopping.
func TestL3_clean_QueryCommasUntouched(t *testing.T) {
	cfg, err := Parse("hy2://pw@example.com:443?alpn=h3,h2&sni=example.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.PortHopping || cfg.HopPorts != "" {
		t.Errorf("query comma misread as hopping: HopPorts=%q PortHopping=%v", cfg.HopPorts, cfg.PortHopping)
	}
	if len(cfg.ALPN) != 2 || cfg.ALPN[0] != "h3" || cfg.ALPN[1] != "h2" {
		t.Errorf("ALPN = %v, want [h3 h2]", cfg.ALPN)
	}
}

// Guard: out-of-range / non-numeric ports are still rejected.
func TestL3_clean_PortValidation(t *testing.T) {
	for _, uri := range []string{
		"hy2://pw@example.com:0",
		"hy2://pw@example.com:65536",
		"hy2://pw@example.com:-1",
	} {
		if _, err := Parse(uri); err == nil {
			t.Errorf("Parse(%q) = nil err, want error", uri)
		}
	}
}

// Guard: pin normalization (colon-hex in, canonical lowercase hex out).
func TestL3_clean_PinNormalization(t *testing.T) {
	colonHex := "00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00"
	cfg, err := Parse("hy2://pw@example.com:443?pinSHA256=" + colonHex)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.PinSHA256 != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Errorf("PinSHA256 = %q", cfg.PinSHA256)
	}
}

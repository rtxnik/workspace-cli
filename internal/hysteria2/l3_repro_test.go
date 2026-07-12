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

// A hy2 password may legitimately contain ':'. Taking only the pre-colon part
// silently sends the wrong secret. Both a single and multiple raw colons.
func TestL3_01_AuthWithColonIsTruncated(t *testing.T) {
	cases := []struct{ uri, want string }{
		{"hy2://user:secretpass@example.com:443?sni=example.com", "user:secretpass"},
		{"hy2://user:p:a:ss@example.com:443", "user:p:a:ss"},
		// An auth that legitimately begins with a colon must survive the guard.
		{"hy2://:secret@example.com:443", ":secret"},
	}
	for _, c := range cases {
		cfg, err := Parse(c.uri)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.uri, err)
		}
		if cfg.Auth != c.want {
			t.Errorf("auth mismatch: got %q, want %q", cfg.Auth, c.want)
		}
	}
	// A genuinely empty auth is still rejected (guard must test the full auth,
	// not just the pre-colon username).
	for _, uri := range []string{"hy2://@example.com:443", "hy2://example.com:443"} {
		if _, err := Parse(uri); err == nil {
			t.Errorf("Parse(%q) = nil err, want missing-auth error", uri)
		}
	}
}

// Port hopping also arrives as a bare range (no comma). It must parse: base
// port = low end of the range, HopPorts = the original spec.
func TestL3_02_RangeOnlyPortHoppingRejected(t *testing.T) {
	cfg, err := Parse("hysteria2://pass@example.com:20000-50000?sni=example.com")
	if err != nil {
		t.Fatalf("range-only port-hopping URI rejected: %v", err)
	}
	if cfg.Port != 20000 {
		t.Errorf("base port = %d, want 20000", cfg.Port)
	}
	if cfg.HopPorts != "20000-50000" {
		t.Errorf("HopPorts = %q, want %q", cfg.HopPorts, "20000-50000")
	}
	if !cfg.PortHopping {
		t.Errorf("PortHopping = false, want true")
	}
}

// Guard: a bare range on an IPv6 host keeps its brackets and low-end base.
func TestL3_clean_IPv6RangeOnlyHopping(t *testing.T) {
	cfg, err := Parse("hy2://pw@[2001:db8::1]:20000-50000")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Address != "2001:db8::1" || cfg.Port != 20000 {
		t.Errorf("addr/port = %q/%d, want 2001:db8::1/20000", cfg.Address, cfg.Port)
	}
	if cfg.HopPorts != "20000-50000" {
		t.Errorf("HopPorts = %q, want %q", cfg.HopPorts, "20000-50000")
	}
}

// A malformed port token must NOT be widened into an accepted (broken) config:
// it stays rejected, as today. Covers the dash-regression the fix could add.
func TestL3_02_MalformedRangeRejected(t *testing.T) {
	for _, uri := range []string{
		"hy2://pw@example.com:443-",        // empty high
		"hy2://pw@example.com:443-abc",     // non-numeric high
		"hy2://pw@example.com:443--444",    // double dash
		"hy2://pw@example.com:0-100",       // zero endpoint (out of 1..65535)
		"hy2://pw@example.com:20000-99999", // high out of range
		"hy2://pw@example.com:443,",        // trailing empty item
		"hy2://pw@example.com:443,abc",     // non-numeric list item
		"hy2://pw@example.com:443-100",     // inverted range (low > high)
	} {
		if _, err := Parse(uri); err == nil {
			t.Errorf("Parse(%q) = nil err, want rejected (malformed port spec)", uri)
		}
	}
}

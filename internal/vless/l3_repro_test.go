package vless

import "testing"

// RFC 3986 schemes are case-insensitive; some exporters emit Vless://.
func TestL3_lowB_SchemeCaseSensitive(t *testing.T) {
	const uuid = "11111111-1111-1111-1111-111111111111"
	for _, uri := range []string{
		"VLESS://" + uuid + "@example.com:443",
		"Vless://" + uuid + "@example.com:443",
	} {
		if _, err := Parse(uri); err != nil {
			t.Errorf("Parse(%q): %v, want accepted", uri, err)
		}
	}
	// A different scheme (e.g. vmess) is still rejected — case-folding must not
	// blur the scheme identity.
	if _, err := Parse("vmess://" + uuid + "@example.com:443"); err == nil {
		t.Errorf("Parse(vmess://...) = nil err, want rejected")
	}
}

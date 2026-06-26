package hysteria2

import "testing"

func TestNormalizePinSHA256(t *testing.T) {
	// xray-core v26 hex-decodes tlsSettings.pinnedPeerCertSha256
	// (hex.DecodeString after stripping ':'), so the normalized form ws stores
	// and emits MUST be lowercase hex — never base64 (the '=' padding trips
	// xray's hex decoder). Every accepted input encoding (hex-with-colons,
	// bare hex, base64) normalizes to the same 64-char lowercase hex string.
	const wantHex = "0000000000000000000000000000000000000000000000000000000000000000"
	inputs := map[string]string{
		"hex-colon": "00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00",
		"bare-hex":  wantHex,
		"base64":    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // base64 of the same 32 zero bytes
	}
	for name, in := range inputs {
		got, err := NormalizePinSHA256(in)
		if err != nil || got != wantHex {
			t.Fatalf("%s: NormalizePinSHA256(%q) = %q, err %v; want %q", name, in, got, err, wantHex)
		}
	}

	if _, err := NormalizePinSHA256("not-a-pin"); err == nil {
		t.Fatalf("expected error for junk input")
	}
	if got, _ := NormalizePinSHA256(""); got != "" {
		t.Fatalf("empty must stay empty, got %q", got)
	}
}

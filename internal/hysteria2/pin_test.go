package hysteria2

import "testing"

func TestNormalizePinSHA256(t *testing.T) {
	// 32 zero bytes -> base64 "AAAA...=" ; hex-colon form must map to the same base64.
	hexColon := "00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00"
	wantB64 := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	got, err := normalizePinSHA256(hexColon)
	if err != nil || got != wantB64 {
		t.Fatalf("hex-colon: got %q err %v, want %q", got, err, wantB64)
	}
	if got, err := normalizePinSHA256(wantB64); err != nil || got != wantB64 {
		t.Fatalf("base64 passthrough: got %q err %v", got, err)
	}
	if _, err := normalizePinSHA256("not-a-pin"); err == nil {
		t.Fatalf("expected error for junk input")
	}
	if got, _ := normalizePinSHA256(""); got != "" {
		t.Fatalf("empty must stay empty, got %q", got)
	}
}

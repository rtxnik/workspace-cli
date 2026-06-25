package proxyengine

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestCertPinSHA256Encoding locks the doctor's observed-pin encoding to lowercase
// hex of sha256(leaf DER) — the same form xray-core v26 hex-decodes for
// tlsSettings.pinnedPeerCertSha256, and the same form ws emits into profiles via
// internal/hysteria2.normalizePinSHA256. If these diverge (e.g. one base64, one
// hex) `ws proxy doctor` compares observed != pin and reports a false mismatch
// on every cert-pinned endpoint.
func TestCertPinSHA256Encoding(t *testing.T) {
	der := []byte("dummy-certificate-DER-bytes")
	got := certPinSHA256(der)

	sum := sha256.Sum256(der)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("certPinSHA256 = %q, want hex %q", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("pin len = %d, want 64 lowercase-hex chars", len(got))
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("pin %q is not valid hex (xray hex-decodes it): %v", got, err)
	}
	// Guard the specific regression: base64 introduces '=' padding and uppercase.
	for _, r := range got {
		if r == '=' || (r >= 'A' && r <= 'Z') {
			t.Fatalf("pin %q has a non-lowercase-hex byte %q (base64 regression)", got, string(r))
		}
	}
}

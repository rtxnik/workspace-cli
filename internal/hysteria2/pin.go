package hysteria2

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// normalizePinSHA256 converts a certificate SHA-256 pin into xray-core's
// expected form: base64 of the raw 32 sha256 bytes (tlsSettings.pinnedPeerCertSha256).
// Accepts hysteria-style hex-with-colons ("AA:BB:.."), bare hex, or an
// already-base64 value. Returns "" for empty input; an error for anything
// that is not exactly 32 bytes once decoded.
func normalizePinSHA256(pin string) (string, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return "", nil
	}
	if strings.Contains(pin, ":") || isHex64(pin) {
		b, err := hex.DecodeString(strings.ReplaceAll(pin, ":", ""))
		if err != nil || len(b) != 32 {
			return "", fmt.Errorf("invalid pin sha256 %q: want 32-byte hex or base64", pin)
		}
		return base64.StdEncoding.EncodeToString(b), nil
	}
	b, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("invalid pin sha256 %q: want 32-byte hex or base64", pin)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

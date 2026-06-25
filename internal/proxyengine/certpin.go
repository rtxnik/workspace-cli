package proxyengine

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"time"
)

// LeafCertSHA256 opens a TCP TLS connection to host:port, fetches the server's
// leaf certificate, and returns hex(sha256(rawDER of leaf)) — the exact format
// of the pin we emit into hysteria2 profiles (tlsSettings.pinnedPeerCertSha256,
// which xray v26 hex-decodes) and accept via ?pinSHA256=. The encoding MUST
// match internal/hysteria2.normalizePinSHA256, or `ws proxy doctor` compares an
// observed hash against a differently-encoded pin and reports a false mismatch.
//
// CAVEAT — this is a TCP-TLS dial (best-effort): a hysteria2 endpoint speaks
// QUIC over UDP, not TCP. If the server only serves the hysteria2/QUIC listener
// on that port and has nothing on TCP, this dial fails or — if a different TCP
// service answers — returns a sha256 that will NOT match the QUIC leaf. The
// doctor surfaces the observed pin and the match result as informational only;
// a mismatch on a hy2 endpoint is expected and must not be treated as a hard
// failure. For vless/reality the TCP-TLS path is representative.
//
// InsecureSkipVerify is intentional: we only want to read the presented leaf to
// fingerprint it, not to validate a chain. No traffic is sent over the
// connection beyond the handshake.
func LeafCertSHA256(host, port string) (string, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port), &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // fingerprinting only; we read, never trust the chain
		ServerName:         host,
	})
	if err != nil {
		return "", fmt.Errorf("tls dial %s:%s: %w", host, port, err)
	}
	defer func() { _ = conn.Close() }()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("no peer certificate presented by %s:%s", host, port)
	}
	return certPinSHA256(certs[0].Raw), nil
}

// certPinSHA256 returns the lowercase-hex sha256 of a certificate's raw DER —
// the cert-pin fingerprint format xray v26 hex-decodes. Kept as a pure helper
// so the encoding is unit-testable without a live TLS dial (see certpin_test.go).
func certPinSHA256(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

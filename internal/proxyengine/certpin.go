package proxyengine

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"time"
)

// LeafCertSHA256 opens a TCP TLS connection to host:port, fetches the server's
// leaf certificate, and returns base64(sha256(rawDER of leaf)) — the exact
// format of the pin we emit into hysteria2 profiles
// (tlsSettings.pinnedPeerCertSha256) and accept via ?pinSHA256=.
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
	sum := sha256.Sum256(certs[0].Raw)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

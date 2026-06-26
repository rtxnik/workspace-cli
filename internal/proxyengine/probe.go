package proxyengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
)

// ProbeResult holds the outcome of a live tunnel-connectivity probe.
// Latency is the raw duration used for human display; JSON output uses the
// derived latencyMs field (integer milliseconds) per the API contract.
type ProbeResult struct {
	DirectIP  string        `json:"directIP"`
	ProxiedIP string        `json:"proxiedIP"`
	Tunneled  bool          `json:"tunneled"`
	Latency   time.Duration `json:"-"`
}

// MarshalJSON implements json.Marshaler so that latencyMs is always emitted as
// integer milliseconds (Latency.Milliseconds()), not nanoseconds. The struct
// tag `json:"-"` on Latency suppresses the raw int64 nanosecond value.
func (r ProbeResult) MarshalJSON() ([]byte, error) {
	type wire struct {
		DirectIP  string `json:"directIP"`
		ProxiedIP string `json:"proxiedIP"`
		Tunneled  bool   `json:"tunneled"`
		LatencyMs int64  `json:"latencyMs"`
	}
	return json.Marshal(wire{
		DirectIP:  r.DirectIP,
		ProxiedIP: r.ProxiedIP,
		Tunneled:  r.Tunneled,
		LatencyMs: r.Latency.Milliseconds(),
	})
}

// tunneled returns true only when both IPs are non-empty and different.
// This is the pure compare helper — no docker, no network; unit-testable.
func tunneled(direct, proxied string) bool {
	return direct != "" && proxied != "" && direct != proxied
}

// validateIP returns the trimmed string if it looks like a valid IP address,
// or an empty string otherwise (rejects HTML error pages, error strings, etc).
func validateIP(s string) string {
	s = strings.TrimSpace(s)
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}

// fetchDirectIP does a plain host HTTP GET to the ip-echo service as the
// baseline (direct egress, no proxy). Short timeout to avoid blocking.
func fetchDirectIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ifconfig.me", nil)
	if err != nil {
		return "", fmt.Errorf("build direct-IP request: %w", err)
	}
	req.Header.Set("User-Agent", "curl/7.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch direct IP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("read direct IP body: %w", err)
	}

	ip := validateIP(string(body))
	if ip == "" {
		return "", fmt.Errorf("direct IP response is not a valid IP: %q", strings.TrimSpace(string(body)))
	}
	return ip, nil
}

// fetchProxiedIP runs curl inside the proxy container to obtain the proxied
// egress IP — i.e. the IP that the tunnel endpoint sees. Docker-dependent;
// only called from the Probe command path, not from unit tests.
func fetchProxiedIP(cfg config.Config) (string, error) {
	out, err := docker.ProxyExec(cfg, "curl", "-s", "--max-time", "5", "https://ifconfig.me")
	if err != nil {
		return "", fmt.Errorf("proxy exec curl: %w", err)
	}

	ip := validateIP(string(out))
	if ip == "" {
		return "", fmt.Errorf("proxied IP response is not a valid IP: %q", strings.TrimSpace(string(out)))
	}
	return ip, nil
}

// DNSVerdict is the severity-split classification of the UDP/DNS exit-IP probe.
type DNSVerdict int

const (
	// DNSInconclusive: the probe produced no exit IP (no UDP/DNS egress observed,
	// resolver unreachable, or blocked). Advisory -- does not prove a leak.
	DNSInconclusive DNSVerdict = iota
	// DNSTunneled: the DNS exit IP is the tunnel exit (== proxied) or some other
	// non-direct IP (e.g. a multi-homed exit). Not the real IP, so not a leak.
	DNSTunneled
	// DNSLeak: the DNS exit IP equals the direct (untunnelled) host IP -- the
	// UDP/DNS query egressed around the tunnel. HARD failure.
	DNSLeak
)

// ClassifyDNS is the pure severity-split decision. direct/proxied come from the
// TCP probe (already proven to differ when tunnelling works); dnsExit is the
// UDP/53-observed exit IP ("" when the probe was inconclusive).
func ClassifyDNS(direct, proxied, dnsExit string) DNSVerdict {
	if dnsExit == "" {
		return DNSInconclusive
	}
	if dnsExit == direct {
		return DNSLeak
	}
	return DNSTunneled
}

// DNSProbeResult is the observable outcome of the UDP/53 DNS exit-IP probe.
// ExitIP is the public IP the resolver reports for the caller ("" when the
// probe was inconclusive: no UDP egress, resolver unreachable, or blocked).
type DNSProbeResult struct {
	ExitIP  string
	Latency time.Duration
}

// dnsEchoServer/dnsEchoName: OpenDNS returns the caller's public IP as the A
// record of myip.opendns.com over UDP/53. Replaceable; see the spec for
// alternates (Akamai whoami.ds.akahelp.net, Google o-o.myaddr.l.google.com TXT).
const (
	dnsEchoServer = "resolver1.opendns.com"
	dnsEchoName   = "myip.opendns.com"
)

// dnsProbeExec issues the UDP/53 DNS exit-IP query from INSIDE the proxy
// container (self-egress vantage, mirroring fetchProxiedIP) via `dig`. Package
// var so tests can inject a fake without docker (mirrors the execInContainer
// seam). `+notcp` forces a genuine UDP/53 datagram -- a TCP fallback would mask a
// UDP leak, which is the whole point of H10. The command runs as root (the
// default `docker exec` uid), NOT uid xray: the mangle XRAY_SELF loop guard
// RETURNs xray-uid traffic around the tunnel, which would produce a false leak.
var dnsProbeExec = func(cfg config.Config) ([]byte, error) {
	return docker.ProxyExec(cfg, "dig", "+short", "+notcp", "+timeout=3", "+tries=1",
		"@"+dnsEchoServer, dnsEchoName, "A")
}

// ProbeDNS issues a UDP/53 DNS A-lookup of an echo name that returns the caller's
// public IP as the resolver sees it, egressing from inside the proxy container so
// the query traverses the real self-egress contour (XRAY_SELF mangle-OUTPUT
// MARK). Any exec error or non-IP output is promoted to an empty ExitIP
// (inconclusive), never a hard error, so the classifier never reports a false
// leak. The dig +timeout/+tries flags bound latency; a drop yields empty output.
func ProbeDNS(cfg config.Config) (DNSProbeResult, error) {
	start := time.Now()
	out, err := dnsProbeExec(cfg)
	lat := time.Since(start)
	if err != nil {
		return DNSProbeResult{Latency: lat}, nil // inconclusive
	}
	return DNSProbeResult{ExitIP: firstValidIP(out), Latency: lat}, nil
}

// firstValidIP returns the first newline-delimited token of out that validateIP
// accepts (dig +short may emit a CNAME line before the A record), or "" if none
// qualifies (inconclusive).
func firstValidIP(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if ip := validateIP(line); ip != "" {
			return ip
		}
	}
	return ""
}

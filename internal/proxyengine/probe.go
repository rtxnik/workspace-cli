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

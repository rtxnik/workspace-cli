package cmd

import (
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/proxyengine"
)

// A real tunnel has direct != proxied. The DNS leg's severity must follow the
// exit IP: proxied => tunnelled (OK), direct => proven leak (HARD), "" =>
// inconclusive (advisory OK). This is the deterministic guard against a vantage
// or wiring regression — it needs no docker.
func TestDNSEgressOutcome_SeveritySplit(t *testing.T) {
	probe := proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "198.51.100.9"}

	cases := []struct {
		name    string
		dnsExit string
		wantOK  bool
		detail  string
	}{
		{"tunnelled when DNS exit == proxied", probe.ProxiedIP, true, "tunnelled"},
		{"proven leak when DNS exit == direct", probe.DirectIP, false, "== direct"},
		{"inconclusive when no DNS exit", "", true, "inconclusive"},
	}
	for _, c := range cases {
		got := dnsEgressOutcome(probe, c.dnsExit)
		if got.OK != c.wantOK {
			t.Errorf("%s: OK=%v want %v (detail %q)", c.name, got.OK, c.wantOK, got.Detail)
		}
		if !strings.Contains(got.Detail, c.detail) {
			t.Errorf("%s: detail %q must contain %q", c.name, got.Detail, c.detail)
		}
	}
}

package docker

import (
	"errors"
	"testing"
)

// TestParseDefaultRouteVia covers the pure `ip route show default` parser
// without docker: it must extract the gateway after `via` and reject lines with
// no gateway or a non-IP value.
func TestParseDefaultRouteVia(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"typical", "default via 172.28.0.2 dev eth0\n", "172.28.0.2", false},
		{"extra lines", "something else\ndefault via 10.0.0.1 dev eth0 metric 100\n", "10.0.0.1", false},
		{"no via", "default dev eth0 scope link\n", "", true},
		{"non-ip via", "default via notanip dev eth0\n", "", true},
		{"empty", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseDefaultRouteVia(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got via=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("via = %q, want %q", got, c.want)
			}
		})
	}
}

// TestClassifyRouteProtection covers the pure route-protection decision without
// docker: a route via the proxy is PROTECTED; via anything else is UNPROTECTED
// (direct egress); a lookup error is UNKNOWN (fail-open -> never PROTECTED).
func TestClassifyRouteProtection(t *testing.T) {
	const proxyIP = "172.28.0.2"
	cases := []struct {
		name        string
		via         string
		lookupErr   error
		wantVerdict RouteProtectionVerdict
	}{
		{"via proxy is protected", proxyIP, nil, RouteProtected},
		{"via other gateway is unprotected", "172.28.0.1", nil, RouteUnprotected},
		{"lookup error is unknown", "", errors.New("docker exec failed"), RouteUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyRouteProtection(c.via, c.lookupErr, proxyIP)
			if got.Verdict != c.wantVerdict {
				t.Errorf("verdict = %v, want %v (detail %q)", got.Verdict, c.wantVerdict, got.Detail)
			}
		})
	}
}

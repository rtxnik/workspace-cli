package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
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

// TestWorkspaceRouteProtection_PropagatesEnumerationError recreates the review
// finding: an uninspectable proxy network must surface as an error, never a
// silent empty result. WorkspaceRouteProtection runs its own NetworkInspect
// scan (historically because ProxyConnectedContainers swallowed inspect errors
// into (nil, nil) -- an empty scan with no error rendered as a false "all clear"
// in `ws proxy status` instead of UNKNOWN; that swallow is now fixed too). This
// test pins that an enumeration failure surfaces as an error, never a silent scan.
func TestWorkspaceRouteProtection_PropagatesEnumerationError(t *testing.T) {
	mock := &mockClient{networkInspFn: func(_ context.Context, _ string, _ network.InspectOptions) (network.Inspect, error) {
		return network.Inspect{}, errors.New("network gone")
	}}
	defer withMock(mock)()
	if _, err := WorkspaceRouteProtection(testCfg()); err == nil {
		t.Fatal("enumeration failure must surface as an error, not a silent empty result")
	}
}

// TestWorkspaceRouteProtection_NameOrder: the result is in name order, the
// proxy left out, whatever order the daemon's endpoint map iterates in — the
// order ws proxy status lists the workspaces in, and its --json arrays. No
// docker is on PATH, so every route is unreadable; the order is what is
// measured. The scan runs repeatedly because one unsorted run can come out
// sorted by chance.
func TestWorkspaceRouteProtection_NameOrder(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	names := []string{"web", "api", "ws-proxy", "ops", "ml-training", "billing", "docs", "search", "auth", "cache", "queue"}
	endpoints := map[string]network.EndpointResource{}
	for i, n := range names {
		endpoints[fmt.Sprintf("%064d", i)] = network.EndpointResource{Name: n}
	}
	mock := &mockClient{networkInspFn: func(_ context.Context, _ string, _ network.InspectOptions) (network.Inspect, error) {
		return network.Inspect{Containers: endpoints}, nil
	}}
	defer withMock(mock)()

	const want = "api,auth,billing,cache,docs,ml-training,ops,queue,search,web"
	for run := 1; run <= 20; run++ {
		prot, err := WorkspaceRouteProtection(testCfg())
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		got := make([]string, len(prot))
		for i, p := range prot {
			got[i] = p.Name
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("run %d: workspaces %s; want %s", run, strings.Join(got, ","), want)
		}
	}
}

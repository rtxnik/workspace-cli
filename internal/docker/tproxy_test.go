package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestForwardingVerdict(t *testing.T) {
	cases := []struct {
		name                    string
		self, forwarded, direct string
		wantOK                  bool
	}{
		{"tunneled", "203.0.113.9", "203.0.113.9", "198.51.100.7", true},
		{"leaks direct", "203.0.113.9", "198.51.100.7", "198.51.100.7", false},
		{"third path", "203.0.113.9", "192.0.2.5", "198.51.100.7", false},
		{"empty forwarded", "203.0.113.9", "", "198.51.100.7", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := ForwardingVerdict(c.self, c.forwarded, c.direct)
			if got != c.wantOK {
				t.Errorf("ForwardingVerdict(%q,%q,%q) = %v, want %v", c.self, c.forwarded, c.direct, got, c.wantOK)
			}
		})
	}
}

func TestParseCapNetAdmin(t *testing.T) {
	// CAP_NET_ADMIN is bit 12 -> 0x1000.
	withCap := "Name:\txray\nCapEff:\t0000000000001000\n"
	without := "Name:\txray\nCapEff:\t0000000000000000\n"
	if !parseCapNetAdmin(withCap) {
		t.Error("expected cap_net_admin present")
	}
	if parseCapNetAdmin(without) {
		t.Error("expected cap_net_admin absent")
	}
}

func TestParseRpFilterAllZero(t *testing.T) {
	if !parseRpFilterAllZero("0\n0\n0\n") {
		t.Error("all zero should pass")
	}
	if parseRpFilterAllZero("0\n1\n0\n") {
		t.Error("any non-zero should fail")
	}
}

func TestParseListens(t *testing.T) {
	ss := "LISTEN 0 4096 *:12345 *:*\n"
	if !parseListens(ss, 12345) {
		t.Error("expected :12345 listening")
	}
	if parseListens(ss, 1080) {
		t.Error("did not expect :1080")
	}
}

func TestParseMangleHasTproxy(t *testing.T) {
	rules := "-N XRAY\n-A XRAY -p tcp -j TPROXY --on-port 12345 --tproxy-mark 0x1\n"
	if !parseMangleHasTproxy(rules, 12345) {
		t.Error("expected TPROXY on 12345")
	}
	if parseMangleHasTproxy("-N XRAY\n", 12345) {
		t.Error("empty chain should fail")
	}
}

func TestParseIPRuleHasFwmark(t *testing.T) {
	rules := "0:\tfrom all lookup local\n32765:\tfrom all fwmark 0x1 lookup 100\n"
	if !parseIPRuleHasFwmark(rules, 1) {
		t.Error("expected fwmark 0x1 rule")
	}
	if parseIPRuleHasFwmark("0:\tfrom all lookup local\n", 1) {
		t.Error("missing fwmark rule should fail")
	}
}

func TestForwardingEgressProbe_HappyPathAndCleanup(t *testing.T) {
	var gotHost *container.HostConfig
	var gotNet *network.NetworkingConfig
	removed := false
	mock := &mockClient{
		createFn: func(_ context.Context, _ *container.Config, host *container.HostConfig, n *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			gotHost, gotNet = host, n
			return container.CreateResponse{ID: "sc-id"}, nil
		},
		removeFn: func(_ context.Context, _ string, opts container.RemoveOptions) error {
			removed = true
			if !opts.Force {
				t.Error("sidecar must be force-removed")
			}
			return nil
		},
	}
	defer withMock(mock)()

	orig := execInContainer
	defer func() { execInContainer = orig }()
	calls := 0
	execInContainer = func(_ string, args ...string) ([]byte, error) {
		calls++
		switch {
		case args[0] == "curl" && calls == 1:
			return []byte("198.51.100.7\n"), nil // direct
		case args[0] == "ip":
			return []byte(""), nil // reroute
		case args[0] == "curl":
			return []byte("203.0.113.9\n"), nil // forwarded
		}
		return nil, nil
	}

	got, err := ForwardingEgressProbe(testCfg())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got.DirectIP != "198.51.100.7" || got.ForwardedIP != "203.0.113.9" {
		t.Errorf("got %+v", got)
	}
	if !removed {
		t.Error("sidecar leaked (ContainerRemove not called)")
	}
	if gotHost == nil || len(gotHost.CapAdd) != 1 || gotHost.CapAdd[0] != "NET_ADMIN" {
		t.Errorf("sidecar HostConfig CapAdd = %+v, want [NET_ADMIN]", gotHost)
	}
	if gotNet == nil || gotNet.EndpointsConfig[testCfg().ProxyNetwork] == nil {
		t.Error("sidecar not attached to the ws-proxy network")
	}
}

func TestForwardingEgressProbe_CleansUpOnExecError(t *testing.T) {
	removed := false
	mock := &mockClient{
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "sc-id"}, nil
		},
		removeFn: func(_ context.Context, _ string, _ container.RemoveOptions) error {
			removed = true
			return nil
		},
	}
	defer withMock(mock)()

	orig := execInContainer
	defer func() { execInContainer = orig }()
	execInContainer = func(_ string, _ ...string) ([]byte, error) {
		return nil, errors.New("exec boom")
	}

	if _, err := ForwardingEgressProbe(testCfg()); err == nil {
		t.Error("expected error from failing exec")
	}
	if !removed {
		t.Error("sidecar must be removed even when a probe step fails")
	}
}

func TestParseXraySelfLoopGuardFirst(t *testing.T) {
	// -S renders the uid numerically (e.g. 102) and MARK as --set-xmark.
	good := "-N XRAY_SELF\n" +
		"-A XRAY_SELF -m owner --uid-owner 102 -j RETURN\n" +
		"-A XRAY_SELF -d 127.0.0.0/8 -j RETURN\n" +
		"-A XRAY_SELF -p tcp -j MARK --set-xmark 0x1/0xffffffff\n" +
		"-A XRAY_SELF -p udp -j MARK --set-xmark 0x1/0xffffffff\n"
	if !parseXraySelfLoopGuardFirst(good) {
		t.Error("expected uid-owner RETURN recognized as the first XRAY_SELF rule")
	}
	// Mis-ordered: a MARK rule precedes the loop guard.
	bad := "-N XRAY_SELF\n" +
		"-A XRAY_SELF -p tcp -j MARK --set-xmark 0x1/0xffffffff\n" +
		"-A XRAY_SELF -m owner --uid-owner 102 -j RETURN\n"
	if parseXraySelfLoopGuardFirst(bad) {
		t.Error("mis-ordered chain (MARK before guard) must fail")
	}
	// Chain absent (iptables prints an error to combined output) must fail, not panic.
	if parseXraySelfLoopGuardFirst("iptables: No chain/target/match by that name.\n") {
		t.Error("absent chain must fail")
	}
}

func TestParseInputAcceptsMark(t *testing.T) {
	withAccept := "-P INPUT ACCEPT\n-A INPUT -m mark --mark 0x1 -j ACCEPT\n"
	if !parseInputAcceptsMark(withAccept, 1) {
		t.Error("expected mark-accept rule recognized")
	}
	if parseInputAcceptsMark("-P INPUT ACCEPT\n", 1) {
		t.Error("missing mark-accept must fail")
	}
	if parseInputAcceptsMark("-A INPUT -m mark --mark 0x2 -j ACCEPT\n", 1) {
		t.Error("a different mark must not match")
	}
	// A mark-accept on a DIFFERENT chain must not satisfy the INPUT parser.
	if parseInputAcceptsMark("-A FORWARD -m mark --mark 0x1 -j ACCEPT\n", 1) {
		t.Error("a mark-accept on FORWARD must not count as an INPUT accept")
	}
}

func TestParseXraySelfMarksUDP(t *testing.T) {
	withUDP := "-A XRAY_SELF -p tcp -j MARK --set-xmark 0x1/0xffffffff\n" +
		"-A XRAY_SELF -p udp -j MARK --set-xmark 0x1/0xffffffff\n"
	if !parseXraySelfMarksUDP(withUDP) {
		t.Error("expected udp MARK rule recognized")
	}
	if parseXraySelfMarksUDP("-A XRAY_SELF -p tcp -j MARK --set-xmark 0x1/0xffffffff\n") {
		t.Error("tcp-only chain must fail (udp self-egress would leak)")
	}
	// Absent chain (iptables error text) must return false, not panic.
	if parseXraySelfMarksUDP("iptables: No chain/target/match by that name.\n") {
		t.Error("absent chain must fail")
	}
}

func TestParseIPv6FailClosed(t *testing.T) {
	// v6 disabled => OK regardless of the ip6tables rc.
	if !parseIPv6FailClosed("1\n", "1\n") {
		t.Error("disable_ipv6=1 must be fail-closed even with no FORWARD DROP")
	}
	// v6 active + FORWARD DROP present (rc 0) => OK.
	if !parseIPv6FailClosed("0\n", "0\n") {
		t.Error("active v6 with FORWARD DROP present must be fail-closed")
	}
	// v6 active + no FORWARD DROP (rc != 0) => FAIL (real leak path).
	if parseIPv6FailClosed("0\n", "1\n") {
		t.Error("active v6 without FORWARD DROP must NOT be fail-closed")
	}
	// Unreadable disable file (empty) is treated as active; no DROP => FAIL.
	if parseIPv6FailClosed("", "1\n") {
		t.Error("unknown v6 state without FORWARD DROP must NOT be fail-closed")
	}
}

// TestParseV6RouteFailClosed covers the pure `ip -6 route show default`
// classifier without docker: an active global default route means the container
// can egress IPv6 around the v4-only proxy (V6Leak); no active default route
// means there is no v6 egress path (V6FailClosed). unreachable/blackhole
// defaults are not egress paths.
func TestParseV6RouteFailClosed(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want WorkspaceV6Verdict
	}{
		{"global default present is a leak", "default via fe80::1 dev eth0 metric 1024\n", V6Leak},
		{"global default via GUA is a leak", "default via 2001:db8::1 dev eth0\n", V6Leak},
		{"no default route is fail-closed", "", V6FailClosed},
		{"only non-default routes is fail-closed", "2001:db8::/64 dev eth0 proto kernel\n", V6FailClosed},
		{"unreachable default is not an egress path", "unreachable default dev lo metric 1024\n", V6FailClosed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseV6RouteFailClosed(c.in); got != c.want {
				t.Errorf("parseV6RouteFailClosed(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

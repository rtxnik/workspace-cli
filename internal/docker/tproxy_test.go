package docker

import "testing"

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

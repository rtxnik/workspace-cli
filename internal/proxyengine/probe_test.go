package proxyengine

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// TestProbeResultLatencyMsJSON verifies that ProbeResult marshals latencyMs as
// milliseconds (not nanoseconds). Contract: latencyMs = Latency.Milliseconds().
func TestProbeResultLatencyMsJSON(t *testing.T) {
	r := ProbeResult{
		DirectIP:  "1.2.3.4",
		ProxiedIP: "5.6.7.8",
		Tunneled:  true,
		Latency:   1500 * time.Millisecond,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	v, ok := m["latencyMs"]
	if !ok {
		t.Fatal("latencyMs field missing from JSON output")
	}
	got := int64(v.(float64))
	const want = int64(1500)
	if got != want {
		t.Errorf("latencyMs = %d, want %d (got nanoseconds instead of milliseconds?)", got, want)
	}
}

// TestExitIPCompare verifies the pure tunneling heuristic without docker.
// tunneled returns true only when both IPs are non-empty and different.
func TestExitIPCompare(t *testing.T) {
	direct := "203.0.113.7"
	if !tunneled(direct, "198.51.100.9") {
		t.Errorf("different IPs => tunneled")
	}
	if tunneled(direct, direct) {
		t.Errorf("same IP => not tunneled")
	}
	if tunneled("", "198.51.100.9") {
		t.Errorf("empty direct => not tunneled")
	}
	if tunneled(direct, "") {
		t.Errorf("empty proxied => not tunneled")
	}
	if tunneled("", "") {
		t.Errorf("both empty => not tunneled")
	}
}

func TestClassifyDNS(t *testing.T) {
	const direct, proxied = "203.0.113.7", "198.51.100.9"
	cases := []struct {
		name    string
		dnsExit string
		want    DNSVerdict
	}{
		{"inconclusive when no exit IP", "", DNSInconclusive},
		{"leak when DNS exit equals direct", direct, DNSLeak},
		{"tunneled when DNS exit equals proxied", proxied, DNSTunneled},
		{"tunneled on a third non-direct IP", "192.0.2.1", DNSTunneled},
	}
	for _, c := range cases {
		if got := ClassifyDNS(direct, proxied, c.dnsExit); got != c.want {
			t.Errorf("%s: ClassifyDNS(%q,%q,%q)=%v want %v", c.name, direct, proxied, c.dnsExit, got, c.want)
		}
	}
}

func TestProbeDNS_InconclusiveOnExecError(t *testing.T) {
	orig := dnsProbeExec
	defer func() { dnsProbeExec = orig }()
	dnsProbeExec = func(config.Config) ([]byte, error) {
		return nil, errors.New("synthetic exec failure")
	}
	res, err := ProbeDNS(config.Config{})
	if err != nil {
		t.Fatalf("ProbeDNS must not return a hard error on exec failure: %v", err)
	}
	if res.ExitIP != "" {
		t.Errorf("exec failure must yield empty ExitIP (inconclusive), got %q", res.ExitIP)
	}
}

func TestProbeDNS_ParsesExitIP(t *testing.T) {
	orig := dnsProbeExec
	defer func() { dnsProbeExec = orig }()
	dnsProbeExec = func(config.Config) ([]byte, error) {
		return []byte("203.0.113.7\n"), nil
	}
	res, _ := ProbeDNS(config.Config{})
	if res.ExitIP != "203.0.113.7" {
		t.Errorf("ExitIP = %q, want 203.0.113.7", res.ExitIP)
	}
}

func TestProbeDNS_RejectsNonIP(t *testing.T) {
	orig := dnsProbeExec
	defer func() { dnsProbeExec = orig }()
	dnsProbeExec = func(config.Config) ([]byte, error) {
		return []byte(";; connection timed out; no servers could be reached\n"), nil
	}
	res, _ := ProbeDNS(config.Config{})
	if res.ExitIP != "" {
		t.Errorf("non-IP dig output must be inconclusive, got %q", res.ExitIP)
	}
}

func TestProbeDNS_SkipsCNAMEPicksFirstIP(t *testing.T) {
	orig := dnsProbeExec
	defer func() { dnsProbeExec = orig }()
	dnsProbeExec = func(config.Config) ([]byte, error) {
		return []byte("myip.opendns.com.\n198.51.100.9\n"), nil
	}
	res, _ := ProbeDNS(config.Config{})
	if res.ExitIP != "198.51.100.9" {
		t.Errorf("ExitIP = %q, want 198.51.100.9 (first valid IP line)", res.ExitIP)
	}
}

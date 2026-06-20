package proxyengine

import (
	"encoding/json"
	"testing"
	"time"
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

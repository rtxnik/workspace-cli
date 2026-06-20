package proxyengine

import "testing"

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

package cmd

import (
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
)

// TestDoctorStopsAtFirstFailure proves the runner is fail-fast: it stops at the
// first failing HARD check, records its index in FailedAt, and never invokes any
// subsequent check. Pure — no docker, no network; the checks are injected fakes.
func TestDoctorStopsAtFirstFailure(t *testing.T) {
	checks := []Check{
		{Name: "a", Run: func() CheckOutcome { return CheckOutcome{OK: true} }},
		{Name: "b", Run: func() CheckOutcome { return CheckOutcome{OK: false, Fix: "do x"} }},
		{Name: "c", Run: func() CheckOutcome { t.Fatal("must not run after b"); return CheckOutcome{} }},
	}
	res := runChecks(checks)
	if res.FailedAt != 1 || res.OK {
		t.Fatalf("got %+v", res)
	}
}

// TestDoctorAllPass proves a clean run: every check OK, FailedAt sentinel -1,
// Result.OK true, and all outcomes recorded in order.
func TestDoctorAllPass(t *testing.T) {
	var order []string
	checks := []Check{
		{Name: "a", Run: func() CheckOutcome { order = append(order, "a"); return CheckOutcome{OK: true, Detail: "d-a"} }},
		{Name: "b", Run: func() CheckOutcome { order = append(order, "b"); return CheckOutcome{OK: true, Detail: "d-b"} }},
	}
	res := runChecks(checks)
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if res.FailedAt != -1 {
		t.Fatalf("expected FailedAt=-1 on all-pass, got %d", res.FailedAt)
	}
	if len(res.Outcomes) != 2 || res.Outcomes[0].Detail != "d-a" || res.Outcomes[1].Detail != "d-b" {
		t.Fatalf("outcomes not recorded in order: %+v", res.Outcomes)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("checks ran out of order: %v", order)
	}
}

// TestDoctorSoftCheckDoesNotStop proves a soft check (modelled as OK=true with a
// Warn detail) does not halt the run — the soft/hard split is encoded in the
// OK bool: hard failures set OK=false, soft warnings keep OK=true.
func TestDoctorSoftCheckDoesNotStop(t *testing.T) {
	ran := false
	checks := []Check{
		{Name: "soft-warn", Run: func() CheckOutcome { return CheckOutcome{OK: true, Detail: "UDP best-effort: SKIP"} }},
		{Name: "after", Run: func() CheckOutcome { ran = true; return CheckOutcome{OK: true} }},
	}
	res := runChecks(checks)
	if !res.OK || res.FailedAt != -1 {
		t.Fatalf("soft warn must not fail the run: %+v", res)
	}
	if !ran {
		t.Fatal("check after a soft warn must still run")
	}
}

// TestProxyDoctorChecks_IncludeTproxyRuntimeChecks asserts that the TPROXY
// runtime checks (tproxy preconditions and forwarding datapath) are registered
// in the ordered check list, at the correct positions relative to the container
// health and self-egress checks.
func TestProxyDoctorChecks_IncludeTproxyRuntimeChecks(t *testing.T) {
	checks := proxyDoctorChecks(config.Config{}, proxyengine.Default())
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	wantOrdered := []string{
		"proxy container running and healthy",
		"tproxy preconditions",
		"self-egress (proxy tunnel exit-IP)",
		"forwarding datapath (dev-container exit-IP)",
	}
	idx := 0
	for _, n := range names {
		if idx < len(wantOrdered) && n == wantOrdered[idx] {
			idx++
		}
	}
	if idx != len(wantOrdered) {
		t.Errorf("checks missing or out of order.\n got: %v\nwant subsequence: %v", names, wantOrdered)
	}
}

func TestDatapathContractVerdict(t *testing.T) {
	cases := []struct {
		name    string
		labels  map[string]string
		profile string
		wantOK  bool
	}{
		{"match tproxy", map[string]string{docker.LabelDatapath: "tproxy"}, "tproxy", true},
		{"match redirect", map[string]string{docker.LabelDatapath: "redirect"}, "redirect", true},
		{"mismatch black-hole", map[string]string{docker.LabelDatapath: "redirect"}, "tproxy", false},
		{"absent label fails closed", map[string]string{}, "tproxy", false},
		{"unverified drift build fails", map[string]string{docker.LabelDatapath: "unverified"}, "tproxy", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := datapathContractVerdict(c.labels, c.profile)
			if out.OK != c.wantOK {
				t.Errorf("verdict OK = %v, want %v (detail=%q)", out.OK, c.wantOK, out.Detail)
			}
			if !out.OK && out.Fix == "" {
				t.Error("a failing verdict must carry a Fix hint")
			}
		})
	}
}

// The datapath-contract check is registered before the container-health check
// (a wrong-datapath image makes downstream health/preconditions meaningless).
func TestProxyDoctorChecks_IncludeDatapathContract(t *testing.T) {
	checks := proxyDoctorChecks(config.Config{}, proxyengine.Default())
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	wantOrdered := []string{
		"active profile valid (xray -test)",
		"datapath contract (image ↔ profile)",
		"proxy container running and healthy",
	}
	idx := 0
	for _, n := range names {
		if idx < len(wantOrdered) && n == wantOrdered[idx] {
			idx++
		}
	}
	if idx != len(wantOrdered) {
		t.Errorf("datapath-contract check missing or out of order.\n got: %v\nwant subsequence: %v", names, wantOrdered)
	}
}

// TestDoctorExitCode maps FailedAt to the process exit code: FailedAt+1 on
// failure (1-based so the first check failing exits 1), 0 on all-pass.
func TestDoctorExitCode(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		want int
	}{
		{"all pass", Result{OK: true, FailedAt: -1}, 0},
		{"first fails", Result{OK: false, FailedAt: 0}, 1},
		{"third fails", Result{OK: false, FailedAt: 2}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := doctorExitCode(c.res); got != c.want {
				t.Errorf("doctorExitCode(%+v) = %d, want %d", c.res, got, c.want)
			}
		})
	}
}

package cmd

import "testing"

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

package cmd

import (
	"errors"
	"strings"
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

// TestV6FailClosedOutcome covers the pure aggregation of per-workspace v6
// verdicts into a doctor CheckOutcome (SEC2-04): any proven leak is HARD
// (OK=false) and names the leaking workspaces; otherwise any UNKNOWN is advisory
// (OK=true, detail says UNKNOWN -- never a PROTECTED claim); all fail-closed is a
// pass; no workspaces is a pass with an informational detail.
func TestV6FailClosedOutcome(t *testing.T) {
	t.Run("proven leak is HARD and names the workspace", func(t *testing.T) {
		got := v6FailClosedOutcome(
			[]string{"a", "b"},
			[]docker.WorkspaceV6Verdict{docker.V6FailClosed, docker.V6Leak},
		)
		if got.OK {
			t.Fatalf("a proven v6 leak must be HARD (OK=false); got %+v", got)
		}
		if !strings.Contains(got.Detail, "b") {
			t.Errorf("detail must name the leaking workspace; got %q", got.Detail)
		}
	})
	t.Run("unknown is advisory, not a leak", func(t *testing.T) {
		got := v6FailClosedOutcome([]string{"a"}, []docker.WorkspaceV6Verdict{docker.V6Unknown})
		if !got.OK {
			t.Fatalf("an unreadable posture must be advisory (OK=true); got %+v", got)
		}
		if !strings.Contains(got.Detail, "UNKNOWN") {
			t.Errorf("advisory detail must say UNKNOWN; got %q", got.Detail)
		}
	})
	t.Run("all fail-closed passes", func(t *testing.T) {
		got := v6FailClosedOutcome([]string{"a"}, []docker.WorkspaceV6Verdict{docker.V6FailClosed})
		if !got.OK {
			t.Fatalf("all fail-closed must pass; got %+v", got)
		}
	})
	t.Run("no workspaces passes informationally", func(t *testing.T) {
		got := v6FailClosedOutcome(nil, nil)
		if !got.OK {
			t.Fatalf("no workspaces must pass; got %+v", got)
		}
	})
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

// countingEngine is a proxyengine.Engine fake that counts Probe invocations so a
// test can prove the doctor's live egress probe is computed once per run.
type countingEngine struct {
	buildCalls    int
	validateCalls int
	probeCalls    int
	probeRes      proxyengine.ProbeResult
	probeErr      error
}

func (e *countingEngine) BuildConfig(proxyengine.Profile) ([]byte, error) {
	e.buildCalls++
	return nil, nil
}

func (e *countingEngine) Validate(config.Config, string) error {
	e.validateCalls++
	return nil
}

func (e *countingEngine) Probe(config.Config) (proxyengine.ProbeResult, error) {
	e.probeCalls++
	return e.probeRes, e.probeErr
}

// runCheckByName runs the single registered check with the given name (the doctor
// check list is stable and name-addressed elsewhere in this file).
func runCheckByName(t *testing.T, checks []Check, name string) CheckOutcome {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c.Run()
		}
	}
	t.Fatalf("check %q not registered", name)
	return CheckOutcome{}
}

// TestDoctorMemo_EachExpensiveOpRunsOnce proves the three duplicated prerequisite
// computations are memoized: the docker prerequisite scan, the live egress probe,
// and the connected-container enumeration each run exactly once across the two
// checks that consume them. Fakes are wired so each consumer short-circuits before
// any real docker/network I/O (all-passed scan; probe error; empty container list).
func TestDoctorMemo_EachExpensiveOpRunsOnce(t *testing.T) {
	var proxyCheckCalls int
	origPC := doctorProxyCheckFn
	doctorProxyCheckFn = func(config.Config) []docker.CheckResult {
		proxyCheckCalls++
		return []docker.CheckResult{
			{Name: "Docker running", Passed: true},
			{Name: "Xray config exists", Passed: true},
			{Name: "Proxy image built", Passed: true},
			{Name: "Proxy container running", Passed: true},
		}
	}
	defer func() { doctorProxyCheckFn = origPC }()

	var containerCalls int
	origCC := proxyConnectedContainersFn
	proxyConnectedContainersFn = func(config.Config) ([]string, error) {
		containerCalls++
		return nil, nil // empty: both consumers short-circuit before any per-container call
	}
	defer func() { proxyConnectedContainersFn = origCC }()

	// Probe error: checkEgress returns before ProbeDNS, checkForwardingEgress
	// returns before the forwarding sidecar — no real I/O, probe still counted.
	eng := &countingEngine{probeErr: errors.New("probe unavailable in test")}

	checks := proxyDoctorChecks(config.Config{}, eng)

	runCheckByName(t, checks, "docker reachable")
	runCheckByName(t, checks, "proxy image present")
	if proxyCheckCalls != 1 {
		t.Errorf("docker prerequisite scan ran %d time(s) across its two checks, want 1", proxyCheckCalls)
	}

	runCheckByName(t, checks, "self-egress (proxy tunnel exit-IP)")
	runCheckByName(t, checks, "forwarding datapath (dev-container exit-IP)")
	if eng.probeCalls != 1 {
		t.Errorf("live egress probe ran %d time(s) across its two checks, want 1", eng.probeCalls)
	}

	runCheckByName(t, checks, "dev-container default route via proxy")
	runCheckByName(t, checks, "workspace IPv6 fail-closed")
	if containerCalls != 1 {
		t.Errorf("connected-container enumeration ran %d time(s) across its two checks, want 1", containerCalls)
	}
}

// TestDoctorMemo_LazyNotComputedAfterEarlyHardFail proves the memos are LAZY, not
// eager: the docker-reachable check (index 0) HARD-fails, so runChecks returns
// before any later check and neither the live egress probe nor the container
// enumeration is ever computed. An eager memo (computing at construction) would
// fail this.
func TestDoctorMemo_LazyNotComputedAfterEarlyHardFail(t *testing.T) {
	origPC := doctorProxyCheckFn
	doctorProxyCheckFn = func(config.Config) []docker.CheckResult {
		return []docker.CheckResult{{Name: "Docker running", Passed: false}}
	}
	defer func() { doctorProxyCheckFn = origPC }()

	var containerCalls int
	origCC := proxyConnectedContainersFn
	proxyConnectedContainersFn = func(config.Config) ([]string, error) {
		containerCalls++
		return nil, nil
	}
	defer func() { proxyConnectedContainersFn = origCC }()

	eng := &countingEngine{}

	res := runChecks(proxyDoctorChecks(config.Config{}, eng))

	if res.OK || res.FailedAt != 0 {
		t.Fatalf("expected HARD fail at the docker-reachable check (index 0), got %+v", res)
	}
	if eng.probeCalls != 0 {
		t.Errorf("live egress probe must NOT run after an earlier HARD check fails; ran %d time(s)", eng.probeCalls)
	}
	if containerCalls != 0 {
		t.Errorf("container enumeration must NOT run after an earlier HARD check fails; ran %d time(s)", containerCalls)
	}
}

// TestDoctorFailsClosedOnEnumerationError proves that when proxy-network
// enumeration returns a genuine error, both workspace-facing HARD checks refuse
// to report a green "nothing connected" and instead fail closed (OK=false) with
// an enumerate-scoped Detail and a remediation. Guards against a silent
// regression to the swallowing (nil,nil) behavior. No live daemon: the reused
// proxyConnectedContainersFn seam is overridden to return the error the callee
// now propagates, and its (names,err) is folded into a containerList exactly as
// the doctor's run-once memo does before threading it into the checks.
func TestDoctorFailsClosedOnEnumerationError(t *testing.T) {
	orig := proxyConnectedContainersFn
	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) {
		return nil, errors.New("inspect proxy network: daemon unreachable")
	}
	defer func() { proxyConnectedContainersFn = orig }()

	cfg := config.Config{ProxyContainer: "ws-proxy", ProxyNetwork: "ws-proxy", ProxyIP: "172.30.0.2"}

	names, err := proxyConnectedContainersFn(cfg)
	cl := containerList{names: names, err: err}

	for _, tc := range []struct {
		name string
		run  func() CheckOutcome
	}{
		{"checkDefaultRoute", func() CheckOutcome { return checkDefaultRoute(cfg, cl) }},
		{"checkWorkspaceV6FailClosed", func() CheckOutcome { return checkWorkspaceV6FailClosed(cl) }},
	} {
		out := tc.run()
		if out.OK {
			t.Errorf("%s: expected fail-closed (OK=false) on enumeration error, got OK=true (%+v)", tc.name, out)
		}
		if !strings.Contains(out.Detail, "could not enumerate connected workspaces") {
			t.Errorf("%s: expected enumerate-scoped Detail, got %q", tc.name, out.Detail)
		}
		if strings.Contains(out.Detail, "no workspace containers connected") {
			t.Errorf("%s: must not fall through to the green 'nothing connected' path on error", tc.name)
		}
		if out.Fix == "" {
			t.Errorf("%s: expected a remediation Fix, got empty", tc.name)
		}
	}
}

// TestActiveProfileReadFold locks the behavior of the folded active-profile read:
// datapathModeFrom (HARD-error policy, consumed by the datapath-contract check)
// and inboundTproxyOutcome (advisory-skip policy) derive their original results
// from a single profileTproxyProbe, per stage.
func TestActiveProfileReadFold(t *testing.T) {
	readErr := errors.New("boom-read")
	parseErr := errors.New("boom-parse")
	nameErr := errors.New("boom-name")

	cases := []struct {
		name              string
		probe             profileTproxyProbe
		wantMode          string
		wantModeErrMsg    string // "" = expect nil error
		wantInboundOK     bool
		wantInboundDetail string
	}{
		{
			name:              "no active profile name",
			probe:             profileTproxyProbe{nameErr: nameErr},
			wantModeErrMsg:    "no active profile",
			wantInboundOK:     true,
			wantInboundDetail: "no active profile (skipped)",
		},
		{
			name:              "empty active profile name",
			probe:             profileTproxyProbe{name: ""},
			wantModeErrMsg:    "no active profile",
			wantInboundOK:     true,
			wantInboundDetail: "no active profile (skipped)",
		},
		{
			name:              "read error",
			probe:             profileTproxyProbe{name: "p", readErr: readErr},
			wantModeErrMsg:    `read active profile "p": boom-read`,
			wantInboundOK:     true,
			wantInboundDetail: "could not read active profile (skipped)",
		},
		{
			name:              "parse error",
			probe:             profileTproxyProbe{name: "p", parseErr: parseErr},
			wantModeErrMsg:    `parse active profile "p": boom-parse`,
			wantInboundOK:     true,
			wantInboundDetail: "could not parse active profile (skipped)",
		},
		{
			name:              "tproxy present",
			probe:             profileTproxyProbe{name: "p", tproxy: true},
			wantMode:          "tproxy",
			wantInboundOK:     true,
			wantInboundDetail: "sockopt.tproxy=tproxy present",
		},
		{
			name:              "redirect (no tproxy)",
			probe:             profileTproxyProbe{name: "p", tproxy: false},
			wantMode:          "redirect",
			wantInboundOK:     true,
			wantInboundDetail: "ADVISORY: active profile inbound missing sockopt.tproxy (TPROXY mode may not work)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, err := datapathModeFrom(c.probe)
			if c.wantModeErrMsg == "" {
				if err != nil {
					t.Errorf("datapathModeFrom unexpected err: %v", err)
				}
			} else if err == nil || err.Error() != c.wantModeErrMsg {
				t.Errorf("datapathModeFrom err = %v, want %q", err, c.wantModeErrMsg)
			}
			if mode != c.wantMode {
				t.Errorf("datapathModeFrom mode = %q, want %q", mode, c.wantMode)
			}

			out := inboundTproxyOutcome(c.probe)
			if out.OK != c.wantInboundOK {
				t.Errorf("inboundTproxyOutcome OK = %v, want %v", out.OK, c.wantInboundOK)
			}
			if out.Detail != c.wantInboundDetail {
				t.Errorf("inboundTproxyOutcome Detail = %q, want %q", out.Detail, c.wantInboundDetail)
			}
		})
	}
}

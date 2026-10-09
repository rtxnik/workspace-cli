package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
	"github.com/rtxnik/workspace-cli/internal/xray"
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

// TestDoctorJSONReportsAFailedWrite: a --json report that cannot be written
// is the error the root prints, where it used to be discarded and the verdict
// returned as if the report had been read. Docker is unreachable, so the run
// stops at its first check.
func TestDoctorJSONReportsAFailedWrite(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/ws-test/docker.sock")
	rootCmd.SetOut(failingWriter{})
	rootCmd.SetArgs([]string{"proxy", "doctor", "--json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
		_ = proxyDoctorCmd.Flags().Set("json", "false")
	})
	if _, err := execute(rootCmd); err == nil || !strings.Contains(err.Error(), "write refused") {
		t.Errorf("ws proxy doctor --json into a failing writer returned %v; want the write error", err)
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
		{Name: "soft-warn", Run: func() CheckOutcome {
			return CheckOutcome{OK: true, Detail: "UDP best-effort: SKIP", Soft: softDegraded}
		}},
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

// TestSoftTierOfEachSoftOutcome pins the doctor's soft tier (phase-5 §3.5),
// one row per soft branch of the spec's table, through the outcome builders
// themselves: a finding that does not stop the run but is not a pass renders
// degraded or unknown, never ok. hy2's two rows dial a real listener: a
// closed port for the inconclusive probe, a TLS server whose leaf differs
// from the pin for the mismatch.
func TestSoftTierOfEachSoftOutcome(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	tlsSrv := httptest.NewUnstartedServer(http.NotFoundHandler())
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0) // the probe hangs up mid-handshake, by design
	tlsSrv.StartTLS()
	t.Cleanup(tlsSrv.Close)
	tlsAddr := tlsSrv.Listener.Addr().(*net.TCPAddr)
	sum := sha256.Sum256(tlsSrv.Certificate().Raw)
	leaf := hex.EncodeToString(sum[:])

	probe := proxyengine.ProbeResult{DirectIP: "198.51.100.1", ProxiedIP: "203.0.113.9", Tunneled: true}
	for _, c := range []struct {
		name string
		got  CheckOutcome
		ok   bool
		want softTier
	}{
		{"inbound: no active profile", inboundTproxyOutcome(profileTproxyProbe{nameErr: errors.New("no link")}), true, softUnknown},
		{"inbound: profile unreadable", inboundTproxyOutcome(profileTproxyProbe{name: "p", readErr: errors.New("eacces")}), true, softUnknown},
		{"inbound: profile unparseable", inboundTproxyOutcome(profileTproxyProbe{name: "p", parseErr: errors.New("eof")}), true, softUnknown},
		{"inbound: sockopt missing", inboundTproxyOutcome(profileTproxyProbe{name: "p"}), true, softDegraded},
		{"inbound: sockopt present", inboundTproxyOutcome(profileTproxyProbe{name: "p", tproxy: true}), true, softNone},
		{"egress: UDP/DNS inconclusive", dnsEgressOutcome(probe, ""), true, softDegraded},
		{"egress: UDP/DNS tunnelled", dnsEgressOutcome(probe, "203.0.113.9"), true, softNone},
		{"egress: UDP/DNS leak", dnsEgressOutcome(probe, "198.51.100.1"), false, softNone},
		{"IPv6: posture unknown", v6FailClosedOutcome([]string{"api"}, []docker.WorkspaceV6Verdict{docker.V6Unknown}), true, softUnknown},
		{"IPv6: fail-closed", v6FailClosedOutcome([]string{"api"}, []docker.WorkspaceV6Verdict{docker.V6FailClosed}), true, softNone},
		{"IPv6: none connected", v6FailClosedOutcome(nil, nil), true, softNone},
		{"hy2: probe inconclusive", hy2ProtocolSanity(xray.DetailedProfile{Address: "127.0.0.1", Port: closedPort}), true, softUnknown},
		{"hy2: leaf differs from the pin", hy2ProtocolSanity(xray.DetailedProfile{Address: "127.0.0.1", Port: tlsAddr.Port, PinSHA256: strings.Repeat("0", 64)}), true, softDegraded},
		{"hy2: leaf matches the pin", hy2ProtocolSanity(xray.DetailedProfile{Address: "127.0.0.1", Port: tlsAddr.Port, PinSHA256: leaf}), true, softNone},
		{"hy2: no pin", hy2ProtocolSanity(xray.DetailedProfile{Address: "127.0.0.1", Port: tlsAddr.Port}), true, softNone},
	} {
		if c.got.OK != c.ok || c.got.Soft != c.want {
			t.Errorf("%s: OK %v, Soft %v; want OK %v, Soft %v (detail %q)", c.name, c.got.OK, c.got.Soft, c.ok, c.want, c.got.Detail)
		}
	}
}

// softWord matches the words a doctor outcome writes into its Detail when it
// is a soft finding (phase-5 §3.5) — ADVISORY, inconclusive, UNKNOWN, NOTE and
// skipped — in any case, and SKIP, which CheckOutcome's earlier convention
// wrote for an advisory leg.
var softWord = regexp.MustCompile(`(?i)\b(advisory|inconclusive|unknown|note|skip(ped)?)\b`)

// untieredSoftOutcomes reads Go source, files by name, and returns every
// CheckOutcome literal that could be an untiered soft finding, as
// "file:line", with the number of literals that do carry a tier. It fails
// closed where it cannot read a literal:
//   - a literal is found by its type, or by the element type of the slice,
//     array or map literal it sits in when its own type is elided;
//   - an unkeyed literal is reported as it stands, its fields unread;
//   - an OK set to anything but the constant false counts as possibly
//     true (an absent OK is false);
//   - Soft carries a tier only when it is set to something other than
//     softNone or 0;
//   - a Detail's words are read from every string literal in its
//     expression — a fmt.Sprintf format among them — and, for a name in it,
//     from the package's string constants of that name and every string
//     literal assigned to the name in the enclosing function.
func untieredSoftOutcomes(t *testing.T, files map[string][]byte) (found []string, tiered int) {
	t.Helper()
	fset := token.NewFileSet()
	var parsed []*ast.File
	consts := map[string][]string{}
	for _, name := range sortedKeys(files) {
		file, err := parser.ParseFile(fset, name, files[name], 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, file)
		ast.Inspect(file, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok {
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						consts[id.Name] = append(consts[id.Name], stringLits(vs.Values[i])...)
					}
				}
			}
			return true
		})
	}
	isOutcome := func(e ast.Expr) bool {
		if s, ok := e.(*ast.StarExpr); ok {
			e = s.X
		}
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "CheckOutcome"
	}
	for _, file := range parsed {
		for _, decl := range file.Decls {
			// Names assigned in this declaration — a function, or a var
			// block — and the string literals assigned to them.
			assigned := map[string][]string{}
			ast.Inspect(decl, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok {
					for i, lhs := range as.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && i < len(as.Rhs) {
							assigned[id.Name] = append(assigned[id.Name], stringLits(as.Rhs[i])...)
						}
					}
				}
				return true
			})
			elided := map[*ast.CompositeLit]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				var elem ast.Expr
				switch ty := lit.Type.(type) {
				case *ast.ArrayType:
					elem = ty.Elt
				case *ast.MapType:
					elem = ty.Value
				}
				if elem != nil && isOutcome(elem) {
					for _, e := range lit.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							e = kv.Value
						}
						if inner, ok := e.(*ast.CompositeLit); ok && inner.Type == nil {
							elided[inner] = true
						}
					}
				}
				if lit.Type != nil && !isOutcome(lit.Type) || lit.Type == nil && !elided[lit] {
					return true
				}
				at := fset.Position(lit.Pos())
				where := fmt.Sprintf("%s:%d", filepath.Base(at.Filename), at.Line)
				okFalse, soft, words := true, false, "" // an absent OK is false
				for _, e := range lit.Elts {
					kv, ok := e.(*ast.KeyValueExpr)
					if !ok {
						found = append(found, where+" (unkeyed)")
						return true
					}
					switch kv.Key.(*ast.Ident).Name {
					case "OK":
						v, ok := kv.Value.(*ast.Ident)
						okFalse = ok && v.Name == "false"
					case "Soft":
						switch v := kv.Value.(type) {
						case *ast.Ident:
							soft = v.Name != "softNone"
						case *ast.BasicLit:
							soft = v.Value != "0"
						default:
							soft = true
						}
					case "Detail":
						words += strings.Join(stringLits(kv.Value), " ")
						ast.Inspect(kv.Value, func(m ast.Node) bool {
							if id, ok := m.(*ast.Ident); ok {
								words += " " + strings.Join(consts[id.Name], " ") + " " + strings.Join(assigned[id.Name], " ")
							}
							return true
						})
					}
				}
				switch {
				case okFalse:
				case soft:
					tiered++
				case softWord.MatchString(words):
					found = append(found, where)
				}
				return true
			})
		}
	}
	return found, tiered
}

// stringLits returns the string literals in an expression, unquoted.
func stringLits(e ast.Expr) []string {
	var out []string
	ast.Inspect(e, func(n ast.Node) bool {
		if b, ok := n.(*ast.BasicLit); ok && b.Kind == token.STRING {
			if s, err := strconv.Unquote(b.Value); err == nil {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// doctorTieredOutcomes is how many tiered outcomes §3.5's table names today:
// the inbound check's three skipped branches and its missing sockopt, the
// egress probe's inconclusive UDP/DNS leg, the IPv6 posture it could not read,
// and the hy2 probe's inconclusive dial and its leaf that differs from the pin.
const doctorTieredOutcomes = 8

// TestDoctorSoftOutcomesAllCarryATier is the guard that keeps §3.5's table
// whole: a soft outcome added later without a tier would render ok, which is
// the defect the table exists to end. It reads every source file of the
// package, so an outcome builder that moves to a file of its own stays under
// it, and it must see at least the tiered outcomes the table names — a walk
// that saw none of them would pass over anything.
func TestDoctorSoftOutcomesAllCarryATier(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		if files[name], err = os.ReadFile(name); err != nil {
			t.Fatal(err)
		}
	}
	found, tiered := untieredSoftOutcomes(t, files)
	if len(found) > 0 {
		t.Errorf("a soft CheckOutcome with no Soft tier, or one the guard cannot read, at %s", strings.Join(found, ", "))
	}
	if tiered < doctorTieredOutcomes {
		t.Errorf("the guard saw %d tiered outcomes; §3.5's table names %d — it is not reading the doctor's checks", tiered, doctorTieredOutcomes)
	}
}

// TestDoctorSoftTierGuardCanFail is the guard's control, over planted source.
// Found: a soft word in a plain string, in a Sprintf format, in a constant,
// in a local assigned a string, in any case; a literal with its type elided in
// a slice or a map of pointers; an unkeyed literal; an OK computed at run
// time; a Soft set to softNone or 0. Not found: a real tier (counted), OK
// false, and a detail with no soft word.
func TestDoctorSoftTierGuardCanFail(t *testing.T) {
	src := []byte(`package cmd
const adv = "ADVISORY: x"
func a() CheckOutcome { return CheckOutcome{OK: true, Detail: "ADVISORY: x"} }
func b() CheckOutcome { return CheckOutcome{OK: true, Detail: fmt.Sprintf("probe inconclusive (%v)", 1)} }
func c() CheckOutcome { return CheckOutcome{OK: true, Detail: "ADVISORY: x", Soft: softDegraded} }
func d() CheckOutcome { return CheckOutcome{OK: false, Detail: "UNKNOWN"} }
func e() CheckOutcome { return CheckOutcome{OK: true, Detail: "running, healthy"} }
func f() []CheckOutcome { return []CheckOutcome{{OK: true, Detail: "posture UNKNOWN"}} }
func g() CheckOutcome { return CheckOutcome{true, "ADVISORY", "", softNone} }
func h() CheckOutcome { return CheckOutcome{OK: true, Detail: adv} }
func i() CheckOutcome { detail := "no profile (skipped)"; return CheckOutcome{OK: true, Detail: detail} }
func j() CheckOutcome { return CheckOutcome{OK: true, Detail: "ADVISORY: x", Soft: softNone} }
func k() CheckOutcome { return CheckOutcome{OK: true, Detail: "UDP best-effort: SKIP"} }
func l(n int) CheckOutcome { return CheckOutcome{OK: n > 0, Detail: "Advisory: x"} }
func m() map[string]*CheckOutcome { return map[string]*CheckOutcome{"a": {OK: true, Detail: "Note: x"}} }
func n() CheckOutcome { return CheckOutcome{OK: true, Detail: "probe Inconclusive", Soft: 0} }
`)
	found, tiered := untieredSoftOutcomes(t, map[string][]byte{"doctor.go": src})
	want := "doctor.go:3,doctor.go:4,doctor.go:8,doctor.go:9 (unkeyed),doctor.go:10,doctor.go:11,doctor.go:12,doctor.go:13,doctor.go:14,doctor.go:15,doctor.go:16"
	if got := strings.Join(found, ","); got != want {
		t.Errorf("found %q,\nwant %q", got, want)
	}
	if tiered != 1 {
		t.Errorf("counted %d tiered outcomes, want 1", tiered)
	}
}

// TestDoctorReport pins ws proxy doctor's report (phase-5 §3.5): a line for
// every check in the list, in order — its outcome when it ran, unknown with no
// note when the run stopped before it; a check's Detail as its note, and the
// failed check's Fix as a second paragraph; soft findings degraded or unknown;
// and the caption, which names the failed check out of the whole list, or
// counts the rendered states when nothing failed.
func TestDoctorReport(t *testing.T) {
	checks := []Check{{Name: "first"}, {Name: "second"}, {Name: "third"}, {Name: "fourth"}}
	ran := func(outs ...CheckOutcome) []checkResult {
		r := make([]checkResult, len(outs))
		for i, o := range outs {
			r[i] = checkResult{Name: checks[i].Name, CheckOutcome: o}
		}
		return r
	}
	ok, adv, fail, unknown := output.StateOK, output.StateAdvisory, output.StateFail, output.StateUnknown
	type line struct {
		state output.State
		note  string
	}
	for _, c := range []struct {
		name    string
		res     Result
		want    []line
		caption string
	}{
		{"every check passes", Result{OK: true, FailedAt: -1, Outcomes: ran(
			CheckOutcome{OK: true}, CheckOutcome{OK: true, Detail: "devpod-proxy"}, CheckOutcome{OK: true}, CheckOutcome{OK: true})},
			[]line{{ok, ""}, {ok, "devpod-proxy"}, {ok, ""}, {ok, ""}}, "4 of 4 checks passed"},
		{"soft findings", Result{OK: true, FailedAt: -1, Outcomes: ran(
			CheckOutcome{OK: true}, CheckOutcome{OK: true, Detail: "ADVISORY: a", Soft: softDegraded},
			CheckOutcome{OK: true, Detail: "posture UNKNOWN", Soft: softUnknown}, CheckOutcome{OK: true})},
			[]line{{ok, ""}, {adv, "ADVISORY: a"}, {unknown, "posture UNKNOWN"}, {ok, ""}},
			"2 of 4 checks passed, 1 degraded, 1 unknown"},
		{"the second check fails", Result{OK: false, FailedAt: 1, Outcomes: ran(
			CheckOutcome{OK: true}, CheckOutcome{OK: false, Detail: "image datapath differs", Fix: "ws proxy rebuild"})},
			[]line{{ok, ""}, {fail, "image datapath differs\nFix: ws proxy rebuild"}, {unknown, ""}, {unknown, ""}},
			"Failed at check 2 of 4: second"},
		{"the first fails with no detail", Result{OK: false, FailedAt: 0, Outcomes: ran(
			CheckOutcome{OK: false, Fix: "Start Docker and retry."})},
			[]line{{fail, "Fix: Start Docker and retry."}, {unknown, ""}, {unknown, ""}, {unknown, ""}},
			"Failed at check 1 of 4: first"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := doctorReport(checks, c.res)
			if got.Title != "Proxy doctor" {
				t.Errorf("title %q, want %q", got.Title, "Proxy doctor")
			}
			if len(got.Items) != len(checks) {
				t.Fatalf("%d lines, want one per check in the list, %d: %+v", len(got.Items), len(checks), got.Items)
			}
			for i, it := range got.Items {
				if it.Name != checks[i].Name || it.State != c.want[i].state || it.Note != c.want[i].note {
					t.Errorf("line %d = %+v, want %q in state %v with note %q", i+1, it, checks[i].Name, c.want[i].state, c.want[i].note)
				}
			}
			if got.Caption != c.caption {
				t.Errorf("caption %q, want %q", got.Caption, c.caption)
			}
		})
	}
}

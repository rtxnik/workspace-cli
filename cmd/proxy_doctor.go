package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
	"github.com/rtxnik/workspace-cli/internal/xray"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
	"github.com/spf13/cobra"
)

// Check is one named diagnostic step. Run is a closure so the check list can be
// built with injected fakes in unit tests (no docker) and with real
// docker/network probes at runtime.
type Check struct {
	Name string
	Run  func() CheckOutcome
}

// CheckOutcome is the result of running one Check. OK=false marks a HARD failure
// that stops the runner. A SOFT/advisory finding is encoded as OK=true with a
// human-readable Detail (e.g. "UDP best-effort: SKIP") so the run continues.
// Fix is a remediation hint shown only on failure. Detail/Fix never contain
// secrets — only a non-secret cert sha256 may be printed.
type CheckOutcome struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// checkResult pairs a Check's name with its outcome, for JSON output and
// rendering.
type checkResult struct {
	Name string `json:"name"`
	CheckOutcome
}

// Result is the aggregate of a doctor run. OK is true only when every check
// passed. FailedAt is the index of the first failing HARD check, or -1 when all
// passed. Outcomes holds every check that actually ran, in order.
type Result struct {
	OK       bool          `json:"ok"`
	FailedAt int           `json:"failedAt"`
	Outcomes []checkResult `json:"checks"`
}

// runChecks executes checks in order and STOPS at the first HARD failure
// (OK=false), recording its index in FailedAt. On a clean run FailedAt is -1 and
// OK is true. Soft findings (OK=true with a Detail) never stop the run.
func runChecks(checks []Check) Result {
	res := Result{OK: true, FailedAt: -1}
	for i, c := range checks {
		out := c.Run()
		res.Outcomes = append(res.Outcomes, checkResult{Name: c.Name, CheckOutcome: out})
		if !out.OK {
			res.OK = false
			res.FailedAt = i
			return res
		}
	}
	return res
}

// doctorExitCode maps a Result to the process exit code: 0 on all-pass, else
// FailedAt+1 (1-based, so the first check failing exits 1).
func doctorExitCode(res Result) int {
	if res.OK {
		return 0
	}
	return res.FailedAt + 1
}

var proxyDoctorCmd = &cobra.Command{
	Use:         "doctor",
	Annotations: proxyAnnotation,
	Short:       "Run an ordered, fail-fast proxy diagnostic with remediation hints",
	Long: "Runs a turnkey, ordered diagnostic of the proxy stack (docker → image → " +
		"config validity → container health → network → routing → live egress → " +
		"protocol sanity). Stops at the first hard failure with a remediation hint " +
		"and a non-zero exit code. Use --json for a machine-readable report.",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		jsonFlag, _ := cmd.Flags().GetBool("json")

		res := runChecks(proxyDoctorChecks(cfg, proxyengine.Default()))

		if jsonFlag {
			output.JSON(res)
			os.Exit(doctorExitCode(res))
		}

		renderDoctor(res)
		os.Exit(doctorExitCode(res))
	},
}

// renderDoctor prints a ✓/✗ line per check that ran, plus the failing check's
// Detail and Fix hint, then a summary.
func renderDoctor(res Result) {
	for _, r := range res.Outcomes {
		mark := output.StyleSuccess.Render("✓")
		if !r.OK {
			mark = output.StyleError.Render("✗")
		}
		line := fmt.Sprintf("  %s %s", mark, r.Name)
		if r.Detail != "" {
			line += output.StyleDim.Render(" — " + r.Detail)
		}
		fmt.Println(line)
	}
	fmt.Println()
	if res.OK {
		output.Success("All proxy checks passed")
		return
	}
	failed := res.Outcomes[res.FailedAt]
	output.Warn(fmt.Sprintf("Failed at check %d/%d: %s", res.FailedAt+1, len(res.Outcomes), failed.Name))
	if failed.Fix != "" {
		output.Detail("Fix: " + failed.Fix)
	}
}

// doctorProxyCheckFn and proxyConnectedContainersFn are the injection seam for
// the read-only docker prerequisite scans the doctor memoizes. They are package
// vars (not direct calls) so tests can count invocations and prove each scan runs
// once per run. Both underlying functions are read-only (docker inspect only), so
// sharing one result across checks is behavior-preserving. proxyConnectedContainersFn
// is declared here, once, for the whole package: the connected-workspace warn gate
// on the proxy mutators reuses the same seam.
var (
	doctorProxyCheckFn         = docker.ProxyCheck
	proxyConnectedContainersFn = docker.ProxyConnectedContainers
)

// memoize wraps compute in a lazy, run-once cache: compute does not run until the
// returned getter is first called, and runs at most once no matter how many
// checks call the getter. Laziness is load-bearing — a getter threaded into a
// late check is never computed when an earlier HARD check fails the run before
// that check executes (crucial for eng.Probe, a LIVE egress probe).
func memoize[T any](compute func() T) func() T {
	var (
		once   sync.Once
		cached T
	)
	return func() T {
		once.Do(func() { cached = compute() })
		return cached
	}
}

// probeResult caches eng.Probe's (result, error) so both egress checks share a
// single live probe.
type probeResult struct {
	res proxyengine.ProbeResult
	err error
}

// containerList caches ProxyConnectedContainers' (names, error) so the default-
// route and IPv6 fail-closed checks enumerate the proxy network once.
type containerList struct {
	names []string
	err   error
}

// profileTproxyProbe is the single read of the active profile shared by the
// datapath-contract check (via datapathModeFrom) and the advisory inbound-sockopt
// check (via inboundTproxyOutcome). It records which stage produced a result so
// each consumer keeps its own error/skip policy verbatim. The doctor performs no
// writes between checks, so caching this read across the run is safe.
type profileTproxyProbe struct {
	name     string
	nameErr  error // from xray.ReadActiveProfileName
	readErr  error // from os.ReadFile
	parseErr error // from json.Unmarshal
	tproxy   bool  // inbound[0].streamSettings.sockopt.tproxy == "tproxy"
}

// readProfileTproxy reads the active profile once and records the outcome of each
// stage so the two consumers can apply their divergent policies.
func readProfileTproxy(cfg config.Config) profileTproxyProbe {
	name, err := xray.ReadActiveProfileName(cfg)
	if err != nil || name == "" {
		return profileTproxyProbe{name: name, nameErr: err}
	}
	data, rerr := os.ReadFile(filepath.Join(cfg.XrayProfilesDir, name+".json"))
	if rerr != nil {
		return profileTproxyProbe{name: name, readErr: rerr}
	}
	var xc xrayconf.XrayConfig
	if perr := json.Unmarshal(data, &xc); perr != nil {
		return profileTproxyProbe{name: name, parseErr: perr}
	}
	tproxy := len(xc.Inbounds) > 0 &&
		xc.Inbounds[0].StreamSettings != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt.Tproxy == "tproxy"
	return profileTproxyProbe{name: name, tproxy: tproxy}
}

// datapathModeFrom derives the active profile's datapath mode from a single
// profile read: "tproxy" when the inbound carries sockopt.tproxy, else
// "redirect". Read/parse failures are HARD errors (surfaced by the datapath
// contract check), matching the original per-stage messages.
func datapathModeFrom(p profileTproxyProbe) (string, error) {
	if p.nameErr != nil || p.name == "" {
		return "", fmt.Errorf("no active profile")
	}
	if p.readErr != nil {
		return "", fmt.Errorf("read active profile %q: %w", p.name, p.readErr)
	}
	if p.parseErr != nil {
		return "", fmt.Errorf("parse active profile %q: %w", p.name, p.parseErr)
	}
	if p.tproxy {
		return "tproxy", nil
	}
	return "redirect", nil
}

// inboundTproxyOutcome is the advisory inbound-sockopt verdict derived from the
// same single profile read. Every stage stays OK=true (advisory) exactly as
// before: missing profile / unreadable / unparseable are skips, present is a
// pass, absent is the upgrade advisory. OK is always true so the doctor run does
// not abort here — existing operators may not have migrated yet.
func inboundTproxyOutcome(p profileTproxyProbe) CheckOutcome {
	if p.nameErr != nil || p.name == "" {
		return CheckOutcome{OK: true, Detail: "no active profile (skipped)"}
	}
	if p.readErr != nil {
		return CheckOutcome{OK: true, Detail: "could not read active profile (skipped)"}
	}
	if p.parseErr != nil {
		return CheckOutcome{OK: true, Detail: "could not parse active profile (skipped)"}
	}
	if p.tproxy {
		return CheckOutcome{OK: true, Detail: "sockopt.tproxy=tproxy present"}
	}
	return CheckOutcome{
		OK:     true,
		Detail: "ADVISORY: active profile inbound missing sockopt.tproxy (TPROXY mode may not work)",
		Fix:    "ws proxy upgrade-config",
	}
}

// proxyDoctorChecks builds the ordered, fail-fast check list. The ordering is
// load-bearing: each check assumes the previous ones passed (e.g. the egress
// probe runs only after the container is proven healthy). eng is injected so the
// real engine can be swapped in tests if ever needed; the docker-touching checks
// are exercised live only.
//
// Soft/hard split: every check here is HARD (OK=false stops the run) EXCEPT the
// UDP leg of the egress probe and the hy2 cert-pin observation, which are
// advisory — they report SKIP / a note via Detail with OK=true so a QUIC-only
// endpoint or a UDP-blocked sandbox does not block the operator.
func proxyDoctorChecks(cfg config.Config, eng proxyengine.Engine) []Check {
	// Run-once memos shared across the checks that need them. Lazy: nothing is
	// computed here; each getter computes on first use, so an early HARD failure
	// short-circuits the run before the later (docker/live) ops execute.
	proxyCheck := memoize(func() []docker.CheckResult { return doctorProxyCheckFn(cfg) })
	probe := memoize(func() probeResult {
		res, err := eng.Probe(cfg)
		return probeResult{res: res, err: err}
	})
	containers := memoize(func() containerList {
		names, err := proxyConnectedContainersFn(cfg)
		return containerList{names: names, err: err}
	})
	profileTproxy := memoize(func() profileTproxyProbe { return readProfileTproxy(cfg) })

	return []Check{
		{Name: "docker reachable", Run: func() CheckOutcome { return checkDockerReachable(proxyCheck()) }},
		{Name: "proxy image present", Run: func() CheckOutcome { return checkImagePresent(cfg, proxyCheck()) }},
		{Name: "active profile valid (xray -test)", Run: func() CheckOutcome { return checkActiveProfileValid(cfg, eng) }},
		{Name: "datapath contract (image ↔ profile)", Run: func() CheckOutcome { return checkDatapathContract(cfg, profileTproxy()) }},
		{Name: "proxy container running and healthy", Run: func() CheckOutcome { return checkContainerHealthy(cfg) }},
		{Name: "tproxy preconditions", Run: func() CheckOutcome { return checkTproxyPreconditions(cfg) }},
		{Name: "ws-proxy network + subnet", Run: func() CheckOutcome { return checkNetworkSubnet(cfg) }},
		{Name: "dev-container default route via proxy", Run: func() CheckOutcome { return checkDefaultRoute(cfg, containers()) }},
		{Name: "self-egress (proxy tunnel exit-IP)", Run: func() CheckOutcome { return checkEgress(cfg, probe()) }},
		{Name: "forwarding datapath (dev-container exit-IP)", Run: func() CheckOutcome { return checkForwardingEgress(cfg, probe()) }},
		{Name: "workspace IPv6 fail-closed", Run: func() CheckOutcome { return checkWorkspaceV6FailClosed(containers()) }},
		{Name: "protocol sanity", Run: func() CheckOutcome { return checkProtocolSanity(cfg) }},
		{Name: "inbound sockopt.tproxy (advisory)", Run: func() CheckOutcome { return inboundTproxyOutcome(profileTproxy()) }},
	}
}

// checkDockerReachable: HARD. Reuses the shared ProxyCheck scan's first result
// (docker ping).
func checkDockerReachable(results []docker.CheckResult) CheckOutcome {
	if len(results) > 0 && results[0].Passed {
		return CheckOutcome{OK: true}
	}
	return CheckOutcome{
		OK:  false,
		Fix: "Start Docker (Docker Desktop or the daemon) and retry.",
	}
}

// checkImagePresent: HARD. Looks up the "Proxy image built" entry by name in the
// shared ProxyCheck scan so it is robust to reordering in that result list.
func checkImagePresent(cfg config.Config, results []docker.CheckResult) CheckOutcome {
	for _, r := range results {
		if r.Name == "Proxy image built" {
			if r.Passed {
				return CheckOutcome{OK: true, Detail: cfg.ProxyImage}
			}
			return CheckOutcome{
				OK:  false,
				Fix: "Build the proxy image: ws proxy rebuild",
			}
		}
	}
	// Named entry not found — treat as failed check.
	return CheckOutcome{
		OK:     false,
		Detail: `"Proxy image built" check not found in ProxyCheck results`,
		Fix:    "Build the proxy image: ws proxy rebuild",
	}
}

// checkActiveProfileValid: HARD. Reads the active profile name from the
// config symlink and validates it via the engine (xray run -test in-container).
func checkActiveProfileValid(cfg config.Config, eng proxyengine.Engine) CheckOutcome {
	name, err := xray.ReadActiveProfileName(cfg)
	if err != nil || name == "" {
		return CheckOutcome{
			OK:  false,
			Fix: "No active profile. Create one: ws proxy init <uri>  (or: ws proxy profile add <name> <uri>)",
		}
	}
	if err := eng.Validate(cfg, name); err != nil {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("profile %q", name),
			Fix:    "xray -test rejected the active profile. Inspect it: ws proxy profile show " + name,
		}
	}
	return CheckOutcome{OK: true, Detail: fmt.Sprintf("profile %q", name)}
}

// checkContainerHealthy: HARD. Container must be running AND report
// State.Health.Status == "healthy".
func checkContainerHealthy(cfg config.Config) CheckOutcome {
	st, err := docker.ProxyStatus(cfg)
	if err != nil {
		return CheckOutcome{OK: false, Fix: "Inspect failed: " + err.Error() + ". Try: ws proxy up"}
	}
	if !st.Running {
		return CheckOutcome{OK: false, Fix: "Proxy container is not running. Start it: ws proxy up"}
	}
	if st.Health != "healthy" {
		detail := "health=" + st.Health
		if st.Health == "" {
			detail = "no healthcheck reported"
		}
		return CheckOutcome{
			OK:     false,
			Detail: detail,
			Fix:    "Container is up but not healthy. Check logs: ws proxy logs",
		}
	}
	return CheckOutcome{OK: true, Detail: "running, healthy"}
}

// checkNetworkSubnet: HARD. The ws-proxy network must exist and carry the
// expected subnet (cfg.ProxySubnet, default 172.28.0.0/16).
func checkNetworkSubnet(cfg config.Config) CheckOutcome {
	subnet, err := docker.NetworkSubnet(cfg)
	if err != nil {
		return CheckOutcome{
			OK:  false,
			Fix: "ws-proxy network missing or malformed. Recreate it: ws proxy down && ws proxy up",
		}
	}
	if subnet != cfg.ProxySubnet {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("got %s, want %s", subnet, cfg.ProxySubnet),
			Fix:    "Network subnet differs from config. Recreate: ws proxy down && ws proxy up",
		}
	}
	return CheckOutcome{OK: true, Detail: subnet}
}

// checkDefaultRoute: HARD. At least one dev-container on the proxy network must
// route its default via the proxy IP (cfg.ProxyIP). When no workspace is
// connected this is informational (nothing to route yet) rather than a failure.
// The connected-container enumeration is shared with checkWorkspaceV6FailClosed.
func checkDefaultRoute(cfg config.Config, cl containerList) CheckOutcome {
	if cl.err != nil {
		return CheckOutcome{OK: false, Fix: "Could not list connected workspaces: " + cl.err.Error()}
	}
	containers := cl.names
	if len(containers) == 0 {
		// Soft: nothing connected, so there is no route to verify yet.
		return CheckOutcome{OK: true, Detail: "no workspace containers connected (nothing to route)"}
	}
	for _, name := range containers {
		via, err := docker.DefaultRouteOf(name)
		if err != nil {
			return CheckOutcome{
				OK:     false,
				Detail: fmt.Sprintf("%s: %v", name, err),
				Fix:    "Restore routes: ws proxy fix-routes",
			}
		}
		if via != cfg.ProxyIP {
			return CheckOutcome{
				OK:     false,
				Detail: fmt.Sprintf("%s default via %s, want %s", name, via, cfg.ProxyIP),
				Fix:    "Default route does not point at the proxy. Fix: ws proxy fix-routes",
			}
		}
	}
	return CheckOutcome{OK: true, Detail: fmt.Sprintf("%d container(s) route via %s", len(containers), cfg.ProxyIP)}
}

// checkEgress: HARD on both the TCP exit-IP comparison (proves the tunnel carries
// traffic) and a proven UDP/DNS leak (DNS exit IP == direct IP). An inconclusive
// UDP/DNS probe (no UDP egress observed) is advisory (OK=true) so a UDP-blocked
// sandbox does not block the operator. The live egress probe is shared with
// checkForwardingEgress (one probe per doctor run).
func checkEgress(cfg config.Config, pm probeResult) CheckOutcome {
	if pm.err != nil {
		return CheckOutcome{
			OK:  false,
			Fix: "Live egress probe failed: " + pm.err.Error() + ". Verify the tunnel: ws proxy test",
		}
	}
	probe := pm.res
	if !probe.Tunneled {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("direct=%s proxied=%s (identical)", probe.DirectIP, probe.ProxiedIP),
			Fix:    "Exit IPs are identical — traffic is NOT tunnelling. Check the profile and logs: ws proxy logs",
		}
	}
	// TCP tunnelling proven. Now the UDP/DNS leg (H10): a DNS exit IP equal to
	// the direct (untunnelled) IP is a PROVEN leak (HARD); inconclusive is advisory.
	dnsRes, _ := proxyengine.ProbeDNS(cfg)
	return dnsEgressOutcome(probe, dnsRes.ExitIP)
}

// dnsEgressOutcome maps the UDP/DNS verdict to a CheckOutcome (H10 severity-split):
// a proven leak is HARD (OK=false); an inconclusive probe is advisory (OK=true).
func dnsEgressOutcome(probe proxyengine.ProbeResult, dnsExit string) CheckOutcome {
	switch proxyengine.ClassifyDNS(probe.DirectIP, probe.ProxiedIP, dnsExit) {
	case proxyengine.DNSLeak:
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("TCP exit-IP %s (direct %s); UDP/DNS exit %s == direct (untunnelled)", probe.ProxiedIP, probe.DirectIP, dnsExit),
			Fix:    "DNS/UDP is leaking around the tunnel (resolver saw your real IP). Check the UDP capture: ws proxy doctor; logs: ws proxy logs",
		}
	case proxyengine.DNSInconclusive:
		return CheckOutcome{
			OK:     true,
			Detail: fmt.Sprintf("TCP exit-IP %s (direct %s); UDP/DNS: inconclusive (no UDP/DNS egress observed)", probe.ProxiedIP, probe.DirectIP),
		}
	default: // DNSTunneled
		return CheckOutcome{
			OK:     true,
			Detail: fmt.Sprintf("TCP exit-IP %s (direct %s); UDP/DNS exit %s (tunnelled)", probe.ProxiedIP, probe.DirectIP, dnsExit),
		}
	}
}

// checkTproxyPreconditions HARD-fails on the first failing runtime prerequisite
// of the TPROXY datapath, naming which one broke so the operator can localize.
func checkTproxyPreconditions(cfg config.Config) CheckOutcome {
	for _, p := range docker.TproxyPreconditions(cfg) {
		if !p.OK {
			return CheckOutcome{
				OK:     false,
				Detail: p.Name + ": FAIL — " + p.Detail,
				Fix:    "Rebuild the proxy image and restart: ws proxy rebuild && ws proxy recreate",
			}
		}
	}
	return CheckOutcome{OK: true, Detail: "cap/rp_filter/listen/mangle/fwmark/self-egress-contour all present"}
}

// checkForwardingEgress proves dev-container traffic traverses the TPROXY
// forwarding leg (not just the proxy's own egress) via an ephemeral sidecar. It
// reuses the shared self-egress probe (no second live probe).
func checkForwardingEgress(cfg config.Config, pm probeResult) CheckOutcome {
	if pm.err != nil {
		return CheckOutcome{OK: false, Fix: "self-egress probe failed: " + pm.err.Error() + ". Run: ws proxy test"}
	}
	self := pm.res
	fwd, err := docker.ForwardingEgressProbe(cfg)
	if err != nil {
		// Infrastructure error: could not test. Surface as HARD — never a silent pass.
		return CheckOutcome{OK: false, Detail: "could not run forwarding probe", Fix: "Forwarding sidecar failed: " + err.Error()}
	}
	ok, reason := docker.ForwardingVerdict(self.ProxiedIP, fwd.ForwardedIP, fwd.DirectIP)
	if !ok {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("self=%s forwarded=%s direct=%s (%s)", self.ProxiedIP, fwd.ForwardedIP, fwd.DirectIP, reason),
			Fix:    "Dev-container traffic is NOT tunnelling. Check xray cap_net_admin and rp_filter via the preconditions above.",
		}
	}
	return CheckOutcome{OK: true, Detail: fmt.Sprintf("forwarded exit-IP %s via tunnel", fwd.ForwardedIP)}
}

// v6FailClosedOutcome is the pure aggregation of per-workspace IPv6 verdicts
// into a doctor CheckOutcome (SEC2-04, DR-SH1-6): a proven v6 egress path is a
// HARD failure naming the workspaces; an unreadable posture is advisory
// (OK=true, detail says UNKNOWN -- never a PROTECTED claim); all fail-closed
// passes; no workspaces is informational.
func v6FailClosedOutcome(names []string, verdicts []docker.WorkspaceV6Verdict) CheckOutcome {
	if len(names) == 0 {
		return CheckOutcome{OK: true, Detail: "no workspace containers connected (nothing to assert)"}
	}
	var leaks, unknown []string
	for i, name := range names {
		switch verdicts[i] {
		case docker.V6Leak:
			leaks = append(leaks, name)
		case docker.V6Unknown:
			unknown = append(unknown, name)
		}
	}
	if len(leaks) > 0 {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("global IPv6 default route present in: %s (can egress v6 around the v4 capture)", strings.Join(leaks, ", ")),
			Fix:    "The proxy is IPv4-only. Disable IPv6 (or drop v6 egress) in these workspaces — see docs/proxy-profiles.md (IPv6).",
		}
	}
	if len(unknown) > 0 {
		return CheckOutcome{
			OK:     true,
			Detail: fmt.Sprintf("IPv6 posture UNKNOWN for: %s (v6 route table unreadable)", strings.Join(unknown, ", ")),
		}
	}
	return CheckOutcome{OK: true, Detail: fmt.Sprintf("%d workspace(s) IPv6 fail-closed", len(names))}
}

// checkWorkspaceV6FailClosed asserts every workspace container on the proxy
// network is IPv6 fail-closed (SEC2-04): the v4 TPROXY capture does not cover
// IPv6, so a workspace with a global v6 default route can egress v6 directly
// around the proxy. The connected-container enumeration is shared with
// checkDefaultRoute; the per-container v6 probe stays separate. The aggregation
// is pure (v6FailClosedOutcome).
func checkWorkspaceV6FailClosed(cl containerList) CheckOutcome {
	if cl.err != nil {
		return CheckOutcome{OK: false, Fix: "Could not list connected workspaces: " + cl.err.Error()}
	}
	names := cl.names
	verdicts := make([]docker.WorkspaceV6Verdict, len(names))
	for i, name := range names {
		verdicts[i] = docker.WorkspaceV6FailClosed(name)
	}
	return v6FailClosedOutcome(names, verdicts)
}

// checkProtocolSanity: HARD that the profile loads; advisory thereafter.
//   - hy2: dials the endpoint via TCP-TLS and prints the observed leaf sha256,
//     noting whether it matches the pinned value. A QUIC-only endpoint that
//     refuses TCP is reported as a NOTE (OK stays true) — see the QUIC caveat on
//     proxyengine.LeafCertSHA256.
//   - vless/reality: asserts serverName (SNI) and shortId are present.
//
// Never prints secrets — only the non-secret sha256 fingerprint and SNI.
func checkProtocolSanity(cfg config.Config) CheckOutcome {
	name, err := xray.ReadActiveProfileName(cfg)
	if err != nil || name == "" {
		return CheckOutcome{OK: false, Fix: "No active profile to inspect."}
	}
	dp, err := xray.LoadProfile(cfg, name)
	if err != nil {
		return CheckOutcome{OK: false, Detail: err.Error(), Fix: "Could not parse the active profile."}
	}

	if dp.Protocol == "hysteria2" {
		return hy2ProtocolSanity(dp)
	}
	return vlessProtocolSanity(dp)
}

// hy2ProtocolSanity dials the hy2 endpoint over TCP-TLS, prints the observed
// leaf sha256, and notes pin match. QUIC-only endpoints fail the TCP dial — that
// is reported as an advisory NOTE (OK stays true) per the documented caveat.
func hy2ProtocolSanity(dp xray.DetailedProfile) CheckOutcome {
	observed, err := proxyengine.LeafCertSHA256(dp.Address, strconv.Itoa(dp.Port))
	if err != nil {
		return CheckOutcome{
			OK: true,
			Detail: fmt.Sprintf("hy2 %s:%d — TCP-TLS probe inconclusive (%v); hysteria2 is QUIC/UDP so a TCP refusal is expected",
				dp.Address, dp.Port, err),
		}
	}
	switch {
	case dp.PinSHA256 == "":
		return CheckOutcome{OK: true, Detail: fmt.Sprintf("hy2 observed leaf sha256=%s (no pin configured)", observed)}
	case observed == dp.PinSHA256:
		return CheckOutcome{OK: true, Detail: fmt.Sprintf("hy2 leaf sha256=%s matches pin", observed)}
	default:
		return CheckOutcome{
			OK: true,
			Detail: fmt.Sprintf("hy2 observed leaf sha256=%s != pin %s — NOTE: TCP-TLS leaf may differ from the QUIC leaf; verify against the endpoint",
				observed, dp.PinSHA256),
		}
	}
}

// vlessProtocolSanity asserts the reality/tls handshake fields the server needs
// (serverName/SNI, and shortId for reality) are present.
func vlessProtocolSanity(dp xray.DetailedProfile) CheckOutcome {
	var missing []string
	if dp.SNI == "" {
		missing = append(missing, "serverName/SNI")
	}
	if dp.Security == "reality" && dp.ShortID == "" {
		missing = append(missing, "shortId")
	}
	if len(missing) > 0 {
		return CheckOutcome{
			OK:     false,
			Detail: "missing: " + strings.Join(missing, ", "),
			Fix:    "Active profile is missing required handshake fields. Re-import the URI: ws proxy init <uri>",
		}
	}
	detail := fmt.Sprintf("%s SNI=%s", dp.Security, dp.SNI)
	if dp.Security == "reality" {
		detail += " shortId set"
	}
	return CheckOutcome{OK: true, Detail: detail}
}

// datapathContractVerdict HARD-fails when the proxy image's datapath LABEL is
// absent (built by an old/unverified ws) or disagrees with the active profile's
// mode (the C5 / audit-T12 black-hole: e.g. a redirect image under a tproxy
// profile). Pure so it is unit-testable without docker.
func datapathContractVerdict(labels map[string]string, profileMode string) CheckOutcome {
	imgMode := labels[docker.LabelDatapath]
	if imgMode == "" {
		return CheckOutcome{
			OK:     false,
			Detail: "proxy image has no " + docker.LabelDatapath + " label (built by an old or unverified ws)",
			Fix:    "Rebuild with a current ws: ws proxy rebuild",
		}
	}
	if imgMode != profileMode {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("image datapath=%q but active profile mode=%q (black-hole risk)", imgMode, profileMode),
			Fix:    "Realign image and profile: ws proxy rebuild  (and, if the profile is stale, ws proxy upgrade-config)",
		}
	}
	return CheckOutcome{OK: true, Detail: fmt.Sprintf("image datapath=%q matches active profile", imgMode)}
}

// checkDatapathContract: HARD. Compares the running proxy image's datapath LABEL
// against the active profile's derived mode. Closes the silent half-desync (C5).
// The active-profile read is shared with the advisory inbound-sockopt check.
func checkDatapathContract(cfg config.Config, p profileTproxyProbe) CheckOutcome {
	labels, err := docker.ImageLabels(cfg)
	if err != nil {
		return CheckOutcome{OK: false, Fix: "Could not inspect proxy image labels: " + err.Error() + ". Rebuild: ws proxy rebuild"}
	}
	mode, err := datapathModeFrom(p)
	if err != nil {
		return CheckOutcome{OK: false, Detail: err.Error(), Fix: "Could not determine the active profile's datapath mode."}
	}
	return datapathContractVerdict(labels, mode)
}

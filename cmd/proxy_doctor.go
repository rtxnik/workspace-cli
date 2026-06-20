package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	return []Check{
		{Name: "docker reachable", Run: func() CheckOutcome { return checkDockerReachable(cfg) }},
		{Name: "proxy image present", Run: func() CheckOutcome { return checkImagePresent(cfg) }},
		{Name: "active profile valid (xray -test)", Run: func() CheckOutcome { return checkActiveProfileValid(cfg, eng) }},
		{Name: "proxy container running and healthy", Run: func() CheckOutcome { return checkContainerHealthy(cfg) }},
		{Name: "ws-proxy network + subnet", Run: func() CheckOutcome { return checkNetworkSubnet(cfg) }},
		{Name: "dev-container default route via proxy", Run: func() CheckOutcome { return checkDefaultRoute(cfg) }},
		{Name: "real egress (tunnel exit-IP)", Run: func() CheckOutcome { return checkEgress(cfg, eng) }},
		{Name: "protocol sanity", Run: func() CheckOutcome { return checkProtocolSanity(cfg) }},
		{Name: "inbound sockopt.tproxy (advisory)", Run: func() CheckOutcome { return checkInboundTproxy(cfg) }},
	}
}

// checkDockerReachable: HARD. Reuses ProxyCheck's first result (docker ping).
func checkDockerReachable(cfg config.Config) CheckOutcome {
	results := docker.ProxyCheck(cfg)
	if len(results) > 0 && results[0].Passed {
		return CheckOutcome{OK: true}
	}
	return CheckOutcome{
		OK:  false,
		Fix: "Start Docker (Docker Desktop or the daemon) and retry.",
	}
}

// checkImagePresent: HARD. ProxyCheck index 2 is "Proxy image built".
func checkImagePresent(cfg config.Config) CheckOutcome {
	results := docker.ProxyCheck(cfg)
	if len(results) > 2 && results[2].Passed {
		return CheckOutcome{OK: true, Detail: cfg.ProxyImage}
	}
	return CheckOutcome{
		OK:  false,
		Fix: "Build the proxy image: ws proxy rebuild",
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
func checkDefaultRoute(cfg config.Config) CheckOutcome {
	containers, err := docker.ProxyConnectedContainers(cfg)
	if err != nil {
		return CheckOutcome{OK: false, Fix: "Could not list connected workspaces: " + err.Error()}
	}
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

// checkEgress: HARD on the TCP exit-IP comparison (proves the tunnel carries
// traffic). The UDP leg is best-effort and surfaced as a note in Detail — its
// failure does not flip OK to false here (the probe today is TCP/HTTP-based; UDP
// is reported as SKIP until a UDP datapath probe exists).
func checkEgress(cfg config.Config, eng proxyengine.Engine) CheckOutcome {
	probe, err := eng.Probe(cfg)
	if err != nil {
		return CheckOutcome{
			OK:  false,
			Fix: "Live egress probe failed: " + err.Error() + ". Verify the tunnel: ws proxy test",
		}
	}
	if !probe.Tunneled {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("direct=%s proxied=%s (identical)", probe.DirectIP, probe.ProxiedIP),
			Fix:    "Exit IPs are identical — traffic is NOT tunnelling. Check the profile and logs: ws proxy logs",
		}
	}
	return CheckOutcome{
		OK: true,
		Detail: fmt.Sprintf("TCP exit-IP %s (direct %s); UDP: SKIP (best-effort, no UDP datapath probe)",
			probe.ProxiedIP, probe.DirectIP),
	}
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

// checkInboundTproxy is a SOFT advisory check: it warns when the active profile's
// dokodemo-door inbound is missing streamSettings.sockopt.tproxy="tproxy" (needed
// for TPROXY-mode transparent proxying). OK is always true so the doctor run
// does not abort here — existing operators may not have migrated yet.
func checkInboundTproxy(cfg config.Config) CheckOutcome {
	name, err := xray.ReadActiveProfileName(cfg)
	if err != nil || name == "" {
		// No active profile — a more fundamental check already covers this.
		return CheckOutcome{OK: true, Detail: "no active profile (skipped)"}
	}
	profilePath := filepath.Join(cfg.XrayProfilesDir, name+".json")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return CheckOutcome{OK: true, Detail: "could not read active profile (skipped)"}
	}
	var xc xrayconf.XrayConfig
	if err := json.Unmarshal(data, &xc); err != nil {
		return CheckOutcome{OK: true, Detail: "could not parse active profile (skipped)"}
	}
	if len(xc.Inbounds) > 0 &&
		xc.Inbounds[0].StreamSettings != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt.Tproxy == "tproxy" {
		return CheckOutcome{OK: true, Detail: "sockopt.tproxy=tproxy present"}
	}
	return CheckOutcome{
		OK:     true,
		Detail: "ADVISORY: active profile inbound missing sockopt.tproxy (TPROXY mode may not work)",
		Fix:    "ws proxy upgrade-config",
	}
}

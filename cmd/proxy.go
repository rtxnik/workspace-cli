package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/hysteria2"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
	"github.com/rtxnik/workspace-cli/internal/vless"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
	"github.com/spf13/cobra"
)

var proxyAnnotation = map[string]string{"group": "proxy"}

var proxyCmd = &cobra.Command{
	Use:         "proxy",
	Short:       "Proxy management commands",
	Annotations: proxyAnnotation,
}

var proxyUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the proxy container",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		noWait, _ := cmd.Flags().GetBool("no-wait")

		steps := []output.Step{
			{Name: "Starting proxy", Fn: func() error {
				return docker.ProxyUp(cfg)
			}},
		}
		if !noWait {
			steps = append(steps, output.Step{
				Name: "Waiting for health check",
				Fn: func() error {
					return docker.WaitForHealth(cfg, 60*time.Second)
				},
			})
		}
		steps = append(steps, output.Step{
			Name: "Fixing workspace routes",
			Fn: func() error {
				rep, err := docker.ProxyFixRoutes(cfg)
				if err != nil {
					return err
				}
				// Partial failure = degraded, non-zero outcome (a workspace
				// egressing DIRECT must not render a green screen).
				return rep.Err()
			},
		})

		if err := output.NewStepRunner(steps...).Run(); err != nil {
			fmt.Fprintln(os.Stderr, output.RenderError(upFailureDetail(err)))
			return &cliErrorWithExit{code: 1, msg: ""}
		}
		return nil
	},
}

var proxyDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop the proxy container",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if proceed, err := confirmProxyMutation(cfg); !proceed {
				return err
			}
		}

		if err := output.RunWithSpinner("Stopping proxy", func() error {
			return docker.ProxyDown(cfg)
		}); err != nil {
			return err
		}
		return nil
	},
}

var proxyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show proxy container status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		st, err := docker.ProxyStatus(cfg)
		if err != nil {
			return err
		}

		jsonFlag, _ := cmd.Flags().GetBool("json")
		if jsonFlag {
			prot, perr := docker.WorkspaceRouteProtection(cfg)
			scanErr := ""
			if perr != nil {
				scanErr = perr.Error()
			}
			output.JSON(struct {
				Running             bool                      `json:"running"`
				Health              string                    `json:"health"`
				Uptime              string                    `json:"uptime"`
				Image               string                    `json:"image"`
				Network             string                    `json:"network"`
				ConnectedWorkspaces []string                  `json:"connectedWorkspaces"`
				WorkspaceProtection []workspaceProtectionJSON `json:"workspaceProtection"`
				ProtectionScanError string                    `json:"protectionScanError,omitempty"`
			}{
				Running:             st.Running,
				Health:              st.Health,
				Uptime:              st.Uptime,
				Image:               st.Image,
				Network:             cfg.ProxyNetwork,
				ConnectedWorkspaces: protectionNames(prot),
				WorkspaceProtection: protectionJSON(prot),
				ProtectionScanError: scanErr,
			})
			return nil
		}

		stateStatus := "stopped"
		if st.Running {
			stateStatus = "running"
		}

		label := output.StyleDim.Render
		var lines []string
		lines = append(lines, fmt.Sprintf("%s  %s", label("State"), output.StatusText(stateStatus)))
		if st.Health != "" {
			lines = append(lines, fmt.Sprintf("%s %s", label("Health"), output.StatusText(st.Health)))
		}
		if st.Uptime != "" {
			lines = append(lines, fmt.Sprintf("%s %s", label("Uptime"), st.Uptime))
		}
		if st.Image != "" {
			lines = append(lines, fmt.Sprintf("%s  %s", label("Image"), st.Image))
		}
		lines = append(lines, fmt.Sprintf("%s  %s (%s)",
			label("Network"), cfg.ProxyNetwork, cfg.ProxyIP))

		// Connected workspaces + route-protection summary (single read-only scan).
		prot, perr := docker.WorkspaceRouteProtection(cfg)
		if names := protectionNames(prot); len(names) > 0 {
			lines = append(lines, "")
			lines = append(lines, output.StyleHeader.Render("Connected Workspaces"))
			for _, name := range names {
				lines = append(lines, "  "+name)
			}
		}
		if perr != nil {
			lines = append(lines, "")
			lines = append(lines, output.StyleHeader.Render("Protection"))
			lines = append(lines, "  "+output.StyleError.Render("✗ ")+"protection scan failed: "+perr.Error()+" (workspace protection UNKNOWN)")
		} else if summary, anyUnprot := protectionSummary(prot); summary != "" {
			lines = append(lines, "")
			lines = append(lines, output.StyleHeader.Render("Protection"))
			marked := summary
			if anyUnprot {
				marked = output.StyleError.Render("✗ ") + summary
			}
			lines = append(lines, "  "+marked)
		}

		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(output.Blue).
			BorderTop(true).
			Padding(0, 2).
			Render(output.StyleHeader.Render("Proxy") + "\n\n" + strings.Join(lines, "\n"))

		fmt.Println(box)
		return nil
	},
}

var proxyCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify proxy prerequisites",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		results := docker.ProxyCheck(cfg)

		passed := 0
		for _, r := range results {
			if r.Passed {
				fmt.Printf("  %s %s\n", output.StyleSuccess.Render("✓"), r.Name)
				passed++
			} else {
				fmt.Printf("  %s %s\n", output.StyleError.Render("✗"), r.Name)
			}
		}

		fmt.Println()
		total := len(results)
		if passed == total {
			output.Success(fmt.Sprintf("%d/%d checks passed", passed, total))
		} else {
			output.Warn(fmt.Sprintf("%d/%d checks passed", passed, total))
		}
		return nil
	},
}

var proxyLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show proxy container logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		logs, err := docker.ProxyLogs(cfg, 50)
		if err != nil {
			return err
		}
		fmt.Print(logs)
		return nil
	},
}

var proxyRebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Rebuild proxy image from scratch",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if proceed, err := confirmProxyMutation(cfg); !proceed {
				return err
			}
		}
		allowDrift, _ := cmd.Flags().GetBool("allow-drift")

		runner := output.NewStepRunner(
			output.Step{Name: "Building proxy image", Fn: func() error {
				return docker.BuildProxyImage(cfg, "", allowDrift)
			}},
			output.Step{Name: "Recreating container", Fn: func() error {
				st, _ := docker.ProxyStatus(cfg)
				if st.Running {
					return docker.ProxyRecreate(cfg)
				}
				return nil
			}},
			output.Step{Name: "Waiting for health check", Fn: func() error {
				// Redundant after the transactional ProxyRecreate (which verifies
				// health internally) but benign; kept for the non-recreate cold
				// path. Removing it is an optional follow-up (spec §10).
				return docker.WaitForHealth(cfg, 60*time.Second)
			}},
			output.Step{Name: "Cleaning old images", Fn: func() error {
				return docker.PruneImages()
			}},
		)
		if err := runner.Run(); err != nil {
			return err
		}
		return nil
	},
}

var proxyTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Prove tunnel is active by comparing direct vs proxied exit IP",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		st, err := docker.ProxyStatus(cfg)
		if err != nil || !st.Running {
			return errors.New("proxy is not running — start it first: ws proxy up")
		}

		output.Info("Probing tunnel (comparing direct vs proxied exit IP)...")

		result, err := proxyengine.Default().Probe(cfg)
		if err != nil {
			return fmt.Errorf("probe failed: %s", err)
		}

		jsonFlag, _ := cmd.Flags().GetBool("json")
		if jsonFlag {
			// Run the same UDP/DNS-leak leg the human path runs, so automation
			// keying on the JSON sees a leak the operator screen would catch
			// (SEC2-02). DNS is probed only when the TCP tunnel holds, mirroring
			// the human path.
			var dnsExit string
			if result.Tunneled {
				dnsRes, _ := proxyengine.ProbeDNS(cfg)
				dnsExit = dnsRes.ExitIP
			}
			verdict, exitNonZero := testDNSVerdict(result, dnsExit)
			output.JSON(testJSONResult{
				DirectIP:  result.DirectIP,
				ProxiedIP: result.ProxiedIP,
				Tunneled:  result.Tunneled,
				LatencyMs: result.Latency.Milliseconds(),
				DNS:       verdict,
				DNSExitIP: dnsExit,
			})
			if exitNonZero {
				return &cliErrorWithExit{code: 1, msg: ""}
			}
			return nil
		}

		tunnelMark := "✗"
		if result.Tunneled {
			tunnelMark = "✓"
		}
		label := output.StyleDim.Render
		fmt.Printf("%s  %s\n", label("Direct IP "), result.DirectIP)
		fmt.Printf("%s %s\n", label("Proxied IP"), result.ProxiedIP)
		fmt.Printf("%s   %s\n", label("Tunneled "), tunnelMark)
		fmt.Printf("%s  %s\n", label("Latency  "), result.Latency.Truncate(time.Millisecond).String())

		if result.Tunneled {
			output.Success("Tunnel active — exit IPs differ")
			// UDP/DNS leg (H10): prove the non-TCP path is tunnelled too.
			dnsRes, _ := proxyengine.ProbeDNS(cfg)
			switch proxyengine.ClassifyDNS(result.DirectIP, result.ProxiedIP, dnsRes.ExitIP) {
			case proxyengine.DNSLeak:
				output.Warn(fmt.Sprintf("UDP/DNS LEAK -- resolver saw your real IP %s (untunnelled)", dnsRes.ExitIP))
				return &cliErrorWithExit{code: 1, msg: ""}
			case proxyengine.DNSInconclusive:
				output.Info("UDP/DNS: inconclusive (no UDP/DNS egress observed)")
			default:
				output.Success(fmt.Sprintf("UDP/DNS tunnelled -- exit %s", dnsRes.ExitIP))
			}
		} else {
			output.Warn("Tunnel NOT active — direct and proxied exit IPs are the same")
			return &cliErrorWithExit{code: 1, msg: ""}
		}
		return nil
	},
}

var proxyDebugCmd = &cobra.Command{
	Use:   "debug <on|off>",
	Short: "Toggle debug logging",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if proceed, err := confirmProxyMutation(cfg); !proceed {
				return err
			}
		}
		mode := args[0]

		var level string
		switch mode {
		case "on":
			level = "debug"
		case "off":
			level = "warning"
		default:
			return errors.New("usage: ws proxy debug <on|off>")
		}

		if err := setXrayLogLevel(cfg, level); err != nil {
			return err
		}
		output.Success(fmt.Sprintf("Log level set to %q", level))

		// Restart proxy if running.
		st, _ := docker.ProxyStatus(cfg)
		if st.Running {
			output.Info("Restarting proxy...")
			if err := docker.ProxyRestart(cfg); err != nil {
				return err
			}
			output.Success("Proxy restarted")
		}
		return nil
	},
}

var proxyUpdateCmd = &cobra.Command{
	Use:   "update [version]",
	Short: "Update xray-core version",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if proceed, err := confirmProxyMutation(cfg); !proceed {
				return err
			}
		}

		version := ""
		if len(args) > 0 {
			version = args[0]
		} else {
			output.Info("Fetching latest xray-core version...")
			v, err := fetchLatestXrayVersion()
			if err != nil {
				return err
			}
			version = v
			output.Detail(fmt.Sprintf("Latest: %s", version))
		}

		if err := output.RunWithSpinner(fmt.Sprintf("Building proxy image with xray-core %s", version), func() error {
			return docker.BuildProxyImage(cfg, version, false)
		}); err != nil {
			return err
		}

		// Recreate proxy container to use the new image. A failed recreate now
		// rolls back to the previous proxy (transactional), so surface that the
		// previous version is still serving rather than a bare warning.
		output.Info("Recreating proxy with the new image...")
		if m, warn := recreateUpdateOutcome(docker.ProxyRecreate(cfg)); warn {
			output.Warn(m)
		} else {
			output.Success(m)
		}
		return nil
	},
}

var proxyFixRoutesCmd = &cobra.Command{
	Use:   "fix-routes",
	Short: "Fix default routes in workspace containers after reboot",
	Long:  "Restores the default route via proxy in all workspace containers on the proxy network. Useful after a system reboot when Docker restarts containers without running devcontainer lifecycle hooks.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		st, err := docker.ProxyStatus(cfg)
		if err != nil || !st.Running {
			return errors.New("Proxy is not running. Start it first: ws proxy up") //nolint:staticcheck // ST1005: operator-facing text, printed verbatim by the root
		}

		rep, err := docker.ProxyFixRoutes(cfg)
		if err != nil {
			return err
		}
		switch {
		case rep.Attempted == 0:
			output.Info("No workspace containers found on proxy network")
		case rep.Fixed == rep.Attempted:
			output.Success(fmt.Sprintf("Fixed routes in %d container(s)", rep.Fixed))
		default:
			for _, f := range rep.Failures {
				output.Warn(f)
			}
			return fmt.Errorf("Fixed routes in %d of %d container(s)", rep.Fixed, rep.Attempted) //nolint:staticcheck // ST1005: operator-facing text, printed verbatim by the root
		}
		return nil
	},
}

var proxyInitCmd = &cobra.Command{
	Use:   "init <proxy-uri>",
	Short: "Generate xray config from a VLESS or Hysteria2 URI",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		uri := args[0]

		scheme, _, ok := strings.Cut(uri, "://")
		if !ok {
			return errors.New("unsupported URI scheme (want vless://, hysteria2://, or hy2://)")
		}
		switch strings.ToLower(scheme) {
		case "vless":
			parsed, err := vless.Parse(uri)
			if err != nil {
				return err
			}
			target, _, rerr := xrayconf.ResolveConfigTarget(cfg.XrayConfig, xrayConfigRoots(cfg))
			if rerr != nil {
				return rerr
			}
			if err := vless.WriteNewConfig(target, parsed); err != nil {
				return err
			}
			output.Success(fmt.Sprintf("Config written to %s", cfg.XrayConfig))
			output.Detail(fmt.Sprintf("Transport: %s, Security: %s", parsed.Network, parsed.Security))
		case "hysteria2", "hy2":
			parsed, err := hysteria2.Parse(uri)
			if err != nil {
				return err
			}
			if parsed.AllowInsecure && parsed.PinSHA256 == "" {
				output.Warn("hysteria2 'insecure' is unsupported on xray-core v26.2.6; ignoring. For a self-signed endpoint, pin the cert: add ?pinSHA256=<sha256> (run 'ws proxy doctor' to print it).")
			}
			target, _, rerr := xrayconf.ResolveConfigTarget(cfg.XrayConfig, xrayConfigRoots(cfg))
			if rerr != nil {
				return rerr
			}
			if err := hysteria2.WriteNewConfig(target, parsed); err != nil {
				return err
			}
			output.Success(fmt.Sprintf("Config written to %s", cfg.XrayConfig))
			output.Detail("Transport: hysteria, Security: tls")
		default:
			return errors.New("unsupported URI scheme (want vless://, hysteria2://, or hy2://)")
		}
		return nil
	},
}

// warnConfirmFn is the confirmation seam: production wires to output.Confirm;
// tests override it to decide the operator's answer without a TTY. The
// connected-workspace enumeration reuses the package's existing
// proxyConnectedContainersFn seam (declared in proxy_doctor.go) — it is NOT
// redeclared here.
var warnConfirmFn = output.Confirm

// errAborted signals the operator declined a mutation at the connected-workspace
// prompt. Every mutator turns it into the same clean no-op — "Aborted" on stderr
// and exit 0: proxy restart and recreate inline, the others through
// confirmProxyMutation.
var errAborted = errors.New("aborted by user")

// warnProxyConnected gates a mutating proxy operation on operator confirmation
// when workspaces still share the proxy network.
//
//   - genuine enumeration error -> fail-closed: return the error so the mutator
//     aborts rather than silently proceeding past an unknown blast radius.
//   - no connected workspaces (nil/empty) -> nil: proceed, no prompt.
//   - connected workspaces + operator declines -> errAborted.
//   - connected workspaces + operator confirms -> nil: proceed.
func warnProxyConnected(cfg config.Config) error {
	names, err := proxyConnectedContainersFn(cfg)
	if err != nil {
		return fmt.Errorf("cannot determine connected workspaces: %w", err)
	}
	if len(names) == 0 {
		return nil
	}

	desc := fmt.Sprintf("Active workspaces: %s\nThis will interrupt network for these workspaces.", strings.Join(names, ", "))
	if !warnConfirmFn("Continue?", desc) {
		return errAborted
	}
	return nil
}

// confirmProxyMutation gates a mutating proxy command on the connected-
// workspace prompt. proceed is false when the command must stop: either the
// operator declined — "Aborted" has been printed and err is nil, so the
// command returns nil and exits 0 — or the connected workspaces could not be
// enumerated, and err says why (fail-closed). Every caller is therefore
//
//	if proceed, err := confirmProxyMutation(cfg); !proceed {
//		return err
//	}
func confirmProxyMutation(cfg config.Config) (proceed bool, err error) {
	if err := warnProxyConnected(cfg); err != nil {
		if errors.Is(err, errAborted) {
			output.Info("Aborted")
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func init() {
	proxyUpCmd.Flags().Bool("no-wait", false, "Skip health check wait after starting")
	proxyDownCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")
	proxyRebuildCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")
	proxyRebuildCmd.Flags().Bool("allow-drift", false, "Build even if the proxy recipe differs from the pinned known-good recipe")
	proxyRestartCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")
	proxyRecreateCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")
	proxyUpdateCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")
	proxyDebugCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")
	proxyCmd.AddCommand(proxyUpCmd)
	proxyCmd.AddCommand(proxyDownCmd)
	proxyCmd.AddCommand(proxyStatusCmd)
	proxyCmd.AddCommand(proxyCheckCmd)
	proxyCmd.AddCommand(proxyLogsCmd)
	proxyCmd.AddCommand(proxyRebuildCmd)
	proxyCmd.AddCommand(proxyTestCmd)
	proxyCmd.AddCommand(proxyDoctorCmd)
	proxyCmd.AddCommand(proxyDebugCmd)
	proxyCmd.AddCommand(proxyUpdateCmd)
	proxyCmd.AddCommand(proxyInitCmd)
	proxyCmd.AddCommand(proxyFixRoutesCmd)
	proxyCmd.AddCommand(proxyRestartCmd)
	proxyCmd.AddCommand(proxyRecreateCmd)
	proxyCmd.AddCommand(proxyUpgradeConfigCmd)
	proxyCmd.AddCommand(profileCmd)
	rootCmd.AddCommand(proxyCmd)
}

// recreateUpdateOutcome renders the operator-facing line after a transactional
// update recreate. A failed recreate now rolls back to the previous proxy, so a
// non-nil err means the previous version is still serving -- a warning, not a
// hard failure.
func recreateUpdateOutcome(err error) (msg string, isWarn bool) {
	if err != nil {
		return fmt.Sprintf("update rolled back -- still running previous version: %s", err), true
	}
	return "Proxy restarted with new version", false
}

// upFailureDetail maps a proxy-up failure to its operator-facing rendering.
// A partial route-fix failure means the proxy IS running but one or more
// workspace containers kept a direct default route -- rendered as a degraded
// outcome naming the failures, not as a start failure.
func upFailureDetail(err error) output.ErrorDetail {
	var rf *docker.RouteFixError
	if errors.As(err, &rf) {
		return output.ErrorDetail{
			Title: fmt.Sprintf("Proxy is up, but workspace routes are DEGRADED (%d of %d failed)",
				len(rf.Report.Failures), rf.Report.Attempted),
			Context: map[string]string{"Failures": strings.Join(rf.Report.Failures, "; ")},
			Suggestions: []string{
				"Retry: ws proxy fix-routes",
				"Diagnose: ws proxy doctor",
			},
		}
	}
	return output.ErrorDetail{
		Title:       "Failed to start proxy",
		Context:     map[string]string{"Error": err.Error()},
		Suggestions: []string{"Check config: ws proxy check", "Initialize config: ws proxy init <vless-uri>", "Rebuild image: ws proxy rebuild"},
	}
}

// workspaceProtectionJSON is the per-workspace route-protection entry in
// `ws proxy status --json`. Status is "protected" | "unprotected" | "unknown".
type workspaceProtectionJSON struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// protectionStatusString maps a route-protection verdict to its stable JSON
// token.
func protectionStatusString(v docker.RouteProtectionVerdict) string {
	switch v {
	case docker.RouteProtected:
		return "protected"
	case docker.RouteUnprotected:
		return "unprotected"
	default:
		return "unknown"
	}
}

// protectionNames extracts the workspace names in scan order.
func protectionNames(prot []docker.RouteProtection) []string {
	names := make([]string, 0, len(prot))
	for _, p := range prot {
		names = append(names, p.Name)
	}
	return names
}

// protectionJSON projects the read-only route-protection scan into the JSON
// wire entries.
func protectionJSON(prot []docker.RouteProtection) []workspaceProtectionJSON {
	out := make([]workspaceProtectionJSON, 0, len(prot))
	for _, p := range prot {
		out = append(out, workspaceProtectionJSON{
			Name:   p.Name,
			Status: protectionStatusString(p.Verdict),
			Detail: p.Detail,
		})
	}
	return out
}

// protectionSummary produces the human status line for workspace route
// protection and reports whether any workspace is UNPROTECTED. UNPROTECTED
// takes priority (it is the actionable leak), then UNKNOWN, then all-protected;
// an empty scan yields no line. Pure.
func protectionSummary(prot []docker.RouteProtection) (line string, anyUnprotected bool) {
	var unprot, unknown, protd int
	for _, p := range prot {
		switch p.Verdict {
		case docker.RouteUnprotected:
			unprot++
		case docker.RouteUnknown:
			unknown++
		case docker.RouteProtected:
			protd++
		}
	}
	total := len(prot)
	if total == 0 {
		return "", false
	}
	switch {
	case unprot > 0:
		return fmt.Sprintf("%d of %d workspace(s) UNPROTECTED — route not via proxy (run: ws proxy fix-routes)", unprot, total), true
	case unknown > 0:
		return fmt.Sprintf("%d of %d workspace(s) protection UNKNOWN — route unreadable", unknown, total), false
	default:
		return fmt.Sprintf("%d workspace(s) protected — route via proxy", protd), false
	}
}

// testJSONResult is the machine-readable shape of `ws proxy test --json`. It is
// a backward-compatible superset of ProbeResult's wire form
// (directIP/proxiedIP/tunneled/latencyMs preserved verbatim) plus the UDP/DNS
// leg the human path already reports: dns is one of "tunneled", "leak",
// "inconclusive", or "skipped" (TCP tunnel down, DNS not probed); dnsExitIP is
// the resolver-observed exit IP ("" when inconclusive/skipped).
type testJSONResult struct {
	DirectIP  string `json:"directIP"`
	ProxiedIP string `json:"proxiedIP"`
	Tunneled  bool   `json:"tunneled"`
	LatencyMs int64  `json:"latencyMs"`
	DNS       string `json:"dns"`
	DNSExitIP string `json:"dnsExitIP,omitempty"`
}

// testDNSVerdict is the pure exit/verdict decision for `ws proxy test --json`,
// mirroring the human path's severity split so the JSON consumer is never
// weaker than the operator screen (SEC2-02): a broken TCP tunnel or a proven
// DNS leak is a non-zero outcome; an inconclusive DNS probe is its own verdict
// (never "tunneled"/green) but not a failure -- matching the human path, which
// treats an inconclusive UDP leg as advisory. dnsExit is "" when the DNS probe
// was inconclusive or not run.
func testDNSVerdict(result proxyengine.ProbeResult, dnsExit string) (verdict string, exitNonZero bool) {
	if !result.Tunneled {
		// TCP tunnel is down: the DNS leg is moot and not probed (the human path
		// only probes DNS inside `if result.Tunneled`). Already a failure.
		return "skipped", true
	}
	switch proxyengine.ClassifyDNS(result.DirectIP, result.ProxiedIP, dnsExit) {
	case proxyengine.DNSLeak:
		return "leak", true
	case proxyengine.DNSTunneled:
		return "tunneled", false
	case proxyengine.DNSInconclusive:
		return "inconclusive", false
	default:
		// Fail safe: an unmodelled future verdict is advisory, never a green
		// "tunneled" claim (this is a never-false-green security verdict).
		return "inconclusive", false
	}
}

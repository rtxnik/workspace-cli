package docker

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/rtxnik/workspace-cli/internal/config"
)

// validateProbeIP returns the trimmed IP if s is a valid IP literal, else "".
// Guards against ProxyExec CombinedOutput mixing stderr / HTML error pages in.
func validateProbeIP(s string) string {
	s = strings.TrimSpace(s)
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}

// ForwardingVerdict decides whether dev-container traffic actually traverses
// the proxy tunnel: the forwarded exit-IP must be a valid IP, equal to the
// proxy's own self-egress IP, and different from the would-be direct IP.
func ForwardingVerdict(self, forwarded, direct string) (bool, string) {
	if validateProbeIP(forwarded) == "" {
		return false, "forwarded probe returned no valid IP (traffic dropped?)"
	}
	if forwarded != self {
		return false, "forwarded exit-IP != proxy self-egress (third path / not tunnelled)"
	}
	if forwarded == direct {
		return false, "forwarded exit-IP == direct (traffic leaks around the tunnel)"
	}
	return true, "forwarded traffic exits via the proxy tunnel"
}

// parseCapNetAdmin reports whether a /proc/<pid>/status dump shows CAP_NET_ADMIN
// (bit 12) set in CapEff.
func parseCapNetAdmin(status string) bool {
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "CapEff:" {
			v, err := strconv.ParseUint(f[1], 16, 64)
			if err != nil {
				return false
			}
			return v&(1<<12) != 0
		}
	}
	return false
}

// parseRpFilterAllZero reports whether every whitespace-separated rp_filter
// value in out is "0".
func parseRpFilterAllZero(out string) bool {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return false
	}
	for _, v := range fields {
		if v != "0" {
			return false
		}
	}
	return true
}

// parseListens reports whether `ss -lnt` output shows the given port listening.
func parseListens(ss string, port int) bool {
	return strings.Contains(ss, ":"+strconv.Itoa(port)+" ") ||
		strings.Contains(ss, ":"+strconv.Itoa(port)+"\n")
}

// parseMangleHasTproxy reports whether `iptables -t mangle -S XRAY` output
// contains a TPROXY jump to the given on-port.
func parseMangleHasTproxy(rules string, port int) bool {
	return strings.Contains(rules, "TPROXY") &&
		strings.Contains(rules, "--on-port "+strconv.Itoa(port))
}

// parseIPRuleHasFwmark reports whether `ip rule` output contains a fwmark rule
// for the given mark (matched in either decimal or hex form).
func parseIPRuleHasFwmark(rules string, mark int) bool {
	return strings.Contains(rules, fmt.Sprintf("fwmark 0x%x", mark)) ||
		strings.Contains(rules, fmt.Sprintf("fwmark %d", mark))
}

// parseXraySelfLoopGuardFirst reports whether the first appended rule of the
// mangle XRAY_SELF chain is the uid-owner loop guard that RETURNs xray's own
// traffic. Parses `iptables -t mangle -S XRAY_SELF`. The uid renders numerically
// in -S output (e.g. `--uid-owner 102`), so the match is structural: the first
// `-A XRAY_SELF` line must carry a uid-owner match and target RETURN. Returns
// false for an absent chain (iptables error text) without panicking.
func parseXraySelfLoopGuardFirst(rules string) bool {
	for _, line := range strings.Split(rules, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "-A XRAY_SELF") {
			continue // skip -N / blank / error lines until the first appended rule
		}
		return strings.Contains(line, "--uid-owner") && strings.Contains(line, "-j RETURN")
	}
	return false
}

// parseInputAcceptsMark reports whether `iptables -S INPUT` contains a rule that
// ACCEPTs packets carrying the given fwmark (hex `0x1` or decimal form), so the
// policy-routed self packets re-entering via lo are not silently dropped under a
// default-DROP INPUT policy.
func parseInputAcceptsMark(rules string, mark int) bool {
	for _, line := range strings.Split(rules, "\n") {
		if !strings.Contains(line, "-A INPUT") || !strings.Contains(line, "-j ACCEPT") {
			continue
		}
		if strings.Contains(line, fmt.Sprintf("--mark 0x%x", mark)) ||
			strings.Contains(line, fmt.Sprintf("--mark %d", mark)) {
			return true
		}
	}
	return false
}

// parseXraySelfMarksUDP reports whether the mangle XRAY_SELF chain MARKs UDP
// (not only TCP), so the proxy's own UDP/QUIC self-egress is captured into the
// tunnel rather than leaking around the old TCP-only REDIRECT path. Parses
// `iptables -t mangle -S XRAY_SELF`.
func parseXraySelfMarksUDP(rules string) bool {
	for _, line := range strings.Split(rules, "\n") {
		if strings.Contains(line, "-A XRAY_SELF") &&
			strings.Contains(line, "-p udp") &&
			strings.Contains(line, "-j MARK") {
			return true
		}
	}
	return false
}

// Precondition is one runtime kernel/runtime prerequisite of the TPROXY datapath.
type Precondition struct {
	Name   string
	OK     bool
	Detail string
}

const (
	tproxyPort = 12345
	tproxyMark = 1
)

// execInContainer runs `docker exec <name> <args...>` and returns CombinedOutput.
// Package var so tests can inject a fake (mirrors newClientFunc). The DockerClient
// SDK interface has no exec method, so in-container commands shell out to docker.
var execInContainer = func(name string, args ...string) ([]byte, error) {
	out, err := exec.Command("docker", append([]string{"exec", name}, args...)...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker exec %s %v: %w (output: %s)", name, args, err, out)
	}
	return out, nil
}

const sidecarName = "ws-proxy-fwdcheck"

// ForwardingProbe is the result of probing the TPROXY forwarding leg from an
// ephemeral sidecar attached to the ws-proxy network.
type ForwardingProbe struct {
	DirectIP    string // sidecar egress on its default bridge route (would-be direct)
	ForwardedIP string // sidecar egress after default route -> ProxyIP (through TPROXY)
}

// ForwardingEgressProbe launches a throwaway container on the ws-proxy network,
// measures its direct egress IP, re-routes its default gateway through the proxy
// (cfg.ProxyIP) to force traffic across the TPROXY forwarding leg, and measures
// the forwarded egress IP. The sidecar reuses the proxy image (it ships curl +
// iproute2) with the entrypoint overridden to a no-op sleep, and is removed
// unconditionally — even on error or panic.
func ForwardingEgressProbe(cfg config.Config) (ForwardingProbe, error) {
	cli, err := newClientFunc()
	if err != nil {
		return ForwardingProbe{}, fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutWrite)
	defer cancel()

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:      cfg.ProxyImage,
			Entrypoint: []string{"sleep", "300"}, // do NOT run the TPROXY entrypoint
		},
		&container.HostConfig{
			CapAdd:     []string{"NET_ADMIN"}, // needed for `ip route replace`
			AutoRemove: true,
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.ProxyNetwork: {}, // auto-assigned IP (must NOT be cfg.ProxyIP)
			},
		},
		nil, sidecarName,
	)
	if err != nil {
		return ForwardingProbe{}, fmt.Errorf("create sidecar: %w", err)
	}
	// Guaranteed cleanup, even on early return / panic. Force-remove tolerates
	// an already-gone container (AutoRemove may have fired).
	defer func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), timeoutStop)
		defer rmCancel()
		_ = cli.ContainerRemove(rmCtx, resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return ForwardingProbe{}, fmt.Errorf("start sidecar: %w", err)
	}

	directOut, err := execInContainer(sidecarName, "curl", "-s", "--max-time", "5", "https://ifconfig.me")
	if err != nil {
		return ForwardingProbe{}, fmt.Errorf("sidecar direct probe: %w", err)
	}
	if _, err := execInContainer(sidecarName, "ip", "route", "replace", "default", "via", cfg.ProxyIP); err != nil {
		return ForwardingProbe{}, fmt.Errorf("sidecar reroute: %w", err)
	}
	fwdOut, err := execInContainer(sidecarName, "curl", "-s", "--max-time", "5", "https://ifconfig.me")
	if err != nil {
		return ForwardingProbe{}, fmt.Errorf("sidecar forwarded probe: %w", err)
	}

	return ForwardingProbe{
		DirectIP:    validateProbeIP(string(directOut)),
		ForwardedIP: validateProbeIP(string(fwdOut)),
	}, nil
}

// TproxyPreconditions runs the HARD runtime preconditions inside the proxy
// container via ProxyExec and returns them in fail-fast order (cheap localizers
// first). Docker-bound; exercised live only (CI / owner host).
func TproxyPreconditions(cfg config.Config) []Precondition {
	var out []Precondition

	// P1: xray process holds CAP_NET_ADMIN (effective).
	status, _ := ProxyExec(cfg, "sh", "-c",
		`for p in /proc/[0-9]*; do [ "$(cat "$p/comm" 2>/dev/null)" = xray ] && cat "$p/status" && break; done`)
	out = append(out, Precondition{
		Name:   "xray CAP_NET_ADMIN",
		OK:     parseCapNetAdmin(string(status)),
		Detail: "xray must hold cap_net_admin for the IP_TRANSPARENT bind",
	})

	// P2: rp_filter disabled on every interface.
	rp, _ := ProxyExec(cfg, "sh", "-c", "cat /proc/sys/net/ipv4/conf/*/rp_filter")
	out = append(out, Precondition{
		Name:   "rp_filter disabled",
		OK:     parseRpFilterAllZero(string(rp)),
		Detail: "strict reverse-path filtering drops marked packets routed to lo",
	})

	// P3: inbound TPROXY socket is listening.
	listen, _ := ProxyExec(cfg, "ss", "-lnt")
	out = append(out, Precondition{
		Name:   "inbound TPROXY listening",
		OK:     parseListens(string(listen), tproxyPort),
		Detail: fmt.Sprintf("xray must LISTEN on :%d", tproxyPort),
	})

	// P4: mangle XRAY chain diverts to the TPROXY socket.
	mangle, _ := ProxyExec(cfg, "iptables", "-t", "mangle", "-S", "XRAY")
	out = append(out, Precondition{
		Name:   "mangle TPROXY divert rule",
		OK:     parseMangleHasTproxy(string(mangle), tproxyPort),
		Detail: "mangle XRAY chain must TPROXY --on-port the inbound socket",
	})

	// P5: policy routing rule for the fwmark.
	rule, _ := ProxyExec(cfg, "ip", "rule")
	out = append(out, Precondition{
		Name:   "fwmark policy routing",
		OK:     parseIPRuleHasFwmark(string(rule), tproxyMark),
		Detail: fmt.Sprintf("ip rule must route fwmark %d to the local table", tproxyMark),
	})

	// P6: self-egress loop guard is the FIRST rule of the mangle XRAY_SELF chain
	// (mis-order → infinite loop / bypass). `xs` is reused by P8.
	xs, _ := ProxyExec(cfg, "iptables", "-t", "mangle", "-S", "XRAY_SELF")
	out = append(out, Precondition{
		Name:   "self-egress loop guard first",
		OK:     parseXraySelfLoopGuardFirst(string(xs)),
		Detail: "mangle XRAY_SELF rule #1 must be -m owner --uid-owner xray -j RETURN",
	})

	// P7: INPUT accepts the looped-back self packets (else a default-DROP INPUT
	// silently eats them — XTLS discussion #4039).
	inp, _ := ProxyExec(cfg, "iptables", "-S", "INPUT")
	out = append(out, Precondition{
		Name:   "INPUT accepts self mark",
		OK:     parseInputAcceptsMark(string(inp), tproxyMark),
		Detail: fmt.Sprintf("INPUT must ACCEPT -m mark --mark %d (policy-routed self packets)", tproxyMark),
	})

	// P8: XRAY_SELF marks UDP too (the old REDIRECT was nat/TCP-only, so the
	// proxy's own UDP/QUIC self-egress leaked around the tunnel).
	out = append(out, Precondition{
		Name:   "self-egress captures UDP",
		OK:     parseXraySelfMarksUDP(string(xs)),
		Detail: "mangle XRAY_SELF must MARK -p udp so the proxy's own UDP/QUIC egress is tunnelled",
	})

	return out
}

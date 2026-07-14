package docker

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/procx"
)

// DefaultRouteOf runs `docker exec <container> ip route show default` and parses
// the gateway IP that follows the `via` token. Used by `ws proxy doctor` to
// prove a dev-container's default route points at the proxy (cfg.ProxyIP).
//
// Output of `ip route show default` looks like:
//
//	default via 172.28.0.2 dev eth0
//
// Returns an error if docker exec fails, no default route exists, or the line
// has no parseable `via <ip>`.
func DefaultRouteOf(container string) (string, error) {
	out, err := procx.RunCombined(context.Background(), timeoutRead, "docker", "exec", container, "ip", "route", "show", "default")
	if err != nil {
		return "", fmt.Errorf("docker exec %s ip route show default: %w (output: %s)", container, err, strings.TrimSpace(string(out)))
	}
	via, err := parseDefaultRouteVia(string(out))
	if err != nil {
		return "", fmt.Errorf("container %s: %w", container, err)
	}
	return via, nil
}

// parseDefaultRouteVia extracts the gateway IP after the `via` token from
// `ip route show default` output. Pure string parsing — unit-testable without
// docker.
func parseDefaultRouteVia(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "via" {
				ip := fields[i+1]
				if net.ParseIP(ip) == nil {
					return "", fmt.Errorf("default route 'via' value %q is not an IP", ip)
				}
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no default route with a 'via' gateway found")
}

// RouteProtectionVerdict classifies one workspace container's default-route
// posture relative to the proxy.
type RouteProtectionVerdict int

const (
	// RouteUnknown: the default route could not be determined (exec error, no
	// default route) -- a fail-open probe, reported as UNKNOWN, never PROTECTED.
	RouteUnknown RouteProtectionVerdict = iota
	// RouteProtected: the default route points at the proxy (via == cfg.ProxyIP).
	RouteProtected
	// RouteUnprotected: the default route points elsewhere -- the container
	// egresses DIRECT, around the proxy.
	RouteUnprotected
)

// RouteProtection pairs a workspace container name with its route-protection
// verdict and a human-readable detail.
type RouteProtection struct {
	Name    string
	Verdict RouteProtectionVerdict
	Detail  string
}

// classifyRouteProtection is the pure decision: given the `via` gateway a
// read-only route lookup returned (and its error), decide the verdict. A lookup
// error is UNKNOWN (fail-open -> never PROTECTED); via == proxyIP is PROTECTED;
// anything else is UNPROTECTED (direct egress). The caller sets Name.
func classifyRouteProtection(via string, lookupErr error, proxyIP string) RouteProtection {
	switch {
	case lookupErr != nil:
		return RouteProtection{Verdict: RouteUnknown, Detail: "route unreadable: " + lookupErr.Error()}
	case via == proxyIP:
		return RouteProtection{Verdict: RouteProtected, Detail: "default via " + via}
	default:
		return RouteProtection{Verdict: RouteUnprotected, Detail: fmt.Sprintf("default via %s (not the proxy %s)", via, proxyIP)}
	}
}

// WorkspaceRouteProtection reports, READ-ONLY, whether each workspace container
// on the proxy network routes its default via the proxy. It performs its own
// endpoint enumeration (the same NetworkInspect scan ProxyFixRoutes uses) and
// the same read-only
// `ip route show default` lookup as the doctor's default-route check
// (DefaultRouteOf); it NEVER mutates a route (no `ip route replace` -- that is
// ProxyFixRoutes' job, DR-SH1-4). A container whose route cannot be read is
// UNKNOWN, never PROTECTED. Enumeration failure itself is returned as an
// error so the caller renders UNKNOWN rather than an empty, falsely
// reassuring scan.
//
// NOTE: this is a third read-only reader of the proxy-network route topology
// (checkDefaultRoute and ProxyFixRoutes are the others); now that
// ProxyConnectedContainers propagates its enumeration errors, folding the three
// readers into one scan is a viable follow-up.
func WorkspaceRouteProtection(cfg config.Config) ([]RouteProtection, error) {
	cli, err := newClientFunc()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{})
	if err != nil {
		// Enumeration failure is surfaced, not swallowed: an uninspectable
		// proxy network must render UNKNOWN, never a silent "all clear"
		// (sweeping fail-open invariant). This mirrors ProxyFixRoutes' own
		// NetworkInspect error handling -- the same endpoint scan, made loud.
		return nil, fmt.Errorf("inspect network: %w", err)
	}

	out := make([]RouteProtection, 0, len(info.Containers))
	for _, ep := range info.Containers {
		if ep.Name == cfg.ProxyContainer {
			continue
		}
		via, rerr := DefaultRouteOf(ep.Name)
		rp := classifyRouteProtection(via, rerr, cfg.ProxyIP)
		rp.Name = ep.Name
		out = append(out, rp)
	}
	return out, nil
}

// NetworkSubnet returns the first IPAM subnet of the proxy network
// (NetworkInspect(...).IPAM.Config[0].Subnet), e.g. "172.28.0.0/16". Used by
// `ws proxy doctor` to confirm the ws-proxy network exists with the expected
// subnet.
func NetworkSubnet(cfg config.Config) (string, error) {
	cli, err := newClientFunc()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect network %q: %w", cfg.ProxyNetwork, err)
	}
	if len(info.IPAM.Config) == 0 || info.IPAM.Config[0].Subnet == "" {
		return "", fmt.Errorf("network %q has no IPAM subnet configured", cfg.ProxyNetwork)
	}
	return info.IPAM.Config[0].Subnet, nil
}

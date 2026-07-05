package docker

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/rtxnik/workspace-cli/internal/config"
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
	out, err := runWithTimeout(timeoutRead, "docker", "exec", container, "ip", "route", "show", "default")
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

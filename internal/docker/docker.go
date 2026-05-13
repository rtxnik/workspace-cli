package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/rtxnik/workspace-cli/internal/config"
)

// Timeouts for Docker operations.
const (
	timeoutRead    = 10 * time.Second
	timeoutWrite   = 30 * time.Second
	timeoutStop    = 15 * time.Second
)

// Status holds proxy container status info.
type Status struct {
	Running bool
	Health  string
	Uptime  string
	Image   string
}

func newClient() (*client.Client, error) {
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

// ProxyStatus returns the current status of the proxy container.
func ProxyStatus(cfg config.Config) (Status, error) {
	cli, err := newClientFunc()
	if err != nil {
		return Status{}, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return Status{Running: false}, nil
		}
		return Status{}, fmt.Errorf("inspect proxy: %w", err)
	}

	var health string
	if info.State.Health != nil {
		health = info.State.Health.Status
	}

	var uptime string
	if info.State.Running {
		started, _ := time.Parse(time.RFC3339Nano, info.State.StartedAt)
		uptime = time.Since(started).Truncate(time.Second).String()
	}

	return Status{
		Running: info.State.Running,
		Health:  health,
		Uptime:  uptime,
		Image:   info.Config.Image,
	}, nil
}

// ProxyUp starts the proxy container on the ws-proxy bridge network.
// Requires image to be pre-built. After starting, it fixes default routes
// in all connected workspace containers to restore proxy connectivity
// (routes are lost when Docker restarts containers after a system reboot).
func ProxyUp(cfg config.Config) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutWrite)
	defer cancel()

	// Check if container already exists.
	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err == nil {
		if info.State.Running {
			// Proxy already running — still fix routes for workspaces
			// that may have lost them after a reboot.
			if _, err := ProxyFixRoutes(cfg); err != nil {
				return err
			}
			return nil
		}
		if err := cli.ContainerStart(ctx, cfg.ProxyContainer, container.StartOptions{}); err != nil {
			return err
		}
		if _, err := ProxyFixRoutes(cfg); err != nil {
			return err
		}
		return nil
	}

	if !imageExists(ctx, cli, cfg.ProxyImage) {
		return fmt.Errorf("proxy image %q not found, run 'ws proxy rebuild' first", cfg.ProxyImage)
	}

	if _, err := os.Stat(cfg.XrayConfig); os.IsNotExist(err) {
		return fmt.Errorf("xray config not found at %s, run 'ws proxy init' first", cfg.XrayConfig)
	}

	if err := ensureProxyNetwork(cli, ctx, cfg); err != nil {
		return fmt.Errorf("create proxy network: %w", err)
	}

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: cfg.ProxyImage,
		},
		&container.HostConfig{
			// PROXY-PROFILE-15 / RESEARCH §5: whole-directory bind so
			// `xray run -test -config /etc/xray/profiles/<name>.json` (D-09)
			// sees the target profile inside the container. The relative
			// symlink config.json -> profiles/<name>.json resolves correctly
			// because both files live under the bound directory. :ro because
			// a vulnerability in xray must never write back into the
			// operator's home tree.
			Binds:         []string{filepath.Dir(cfg.XrayConfig) + ":/etc/xray/:ro"},
			CapAdd:        []string{"NET_ADMIN"},
			Sysctls:       map[string]string{"net.ipv4.ip_forward": "1"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.ProxyNetwork: {
					IPAMConfig: &network.EndpointIPAMConfig{
						IPv4Address: cfg.ProxyIP,
					},
				},
			},
		},
		nil, cfg.ProxyContainer,
	)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}

	return cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
}

// ProxyDown stops the proxy container. Workspace containers on the
// ws-proxy bridge network are unaffected and resume connectivity
// when the proxy is started again.
func ProxyDown(cfg config.Config) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutStop)
	defer cancel()

	timeout := 10
	if err := cli.ContainerStop(ctx, cfg.ProxyContainer, container.StopOptions{Timeout: &timeout}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("stop proxy: %w", err)
	}
	return nil
}

// CheckResult holds a single check result.
type CheckResult struct {
	Name   string
	Passed bool
}

// ProxyCheck verifies all prerequisites (docker, config, image, container).
func ProxyCheck(cfg config.Config) []CheckResult {
	results := make([]CheckResult, 4)
	results[0] = CheckResult{Name: "Docker running"}
	results[1] = CheckResult{Name: "Xray config exists"}
	results[2] = CheckResult{Name: "Proxy image built"}
	results[3] = CheckResult{Name: "Proxy container running"}

	cli, err := newClientFunc()
	if err != nil {
		return results
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	if _, err := cli.Ping(ctx); err != nil {
		return results
	}
	results[0].Passed = true

	if _, err := os.Stat(cfg.XrayConfig); err == nil {
		results[1].Passed = true
	}

	if imageExists(ctx, cli, cfg.ProxyImage) {
		results[2].Passed = true
	}

	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err == nil && info.State.Running {
		results[3].Passed = true
	}

	return results
}

// ProxyLogs returns the last n lines of proxy container logs.
func ProxyLogs(cfg config.Config, n int) (string, error) {
	cli, err := newClientFunc()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	reader, err := cli.ContainerLogs(ctx, cfg.ProxyContainer, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", n),
	})
	if err != nil {
		return "", fmt.Errorf("get logs: %w", err)
	}
	defer reader.Close()

	out, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ProxyRebuild rebuilds the proxy image with minimal downtime.
// Build happens first (while proxy may still be running), then the
// container is recreated on the same bridge network. Workspace
// containers are unaffected.
func ProxyRebuild(cfg config.Config) error {
	st, _ := ProxyStatus(cfg)
	wasRunning := st.Running

	if err := BuildProxyImage(cfg, ""); err != nil {
		return err
	}

	if wasRunning {
		if err := proxyRecreate(cfg); err != nil {
			return fmt.Errorf("restart after rebuild: %w", err)
		}
	}

	// Clean up dangling old image (best-effort).
	pruneCmd := exec.Command("docker", "image", "prune", "-f")
	_ = pruneCmd.Run()

	return nil
}

// ProxyRestart stops and starts the proxy container.
func ProxyRestart(cfg config.Config) error {
	if err := ProxyDown(cfg); err != nil {
		return err
	}
	return ProxyUp(cfg)
}

// ProxyRecreate removes and recreates the proxy container on the
// ws-proxy bridge network. Workspace containers are unaffected —
// they keep their own network namespace and resume connectivity
// when the new proxy comes up with the same IP.
func ProxyRecreate(cfg config.Config) error {
	return proxyRecreate(cfg)
}

func proxyRecreate(cfg config.Config) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutWrite)
	defer cancel()

	timeout := 10
	if err := cli.ContainerStop(ctx, cfg.ProxyContainer, container.StopOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("stop proxy: %w", err)
	}
	if err := cli.ContainerRemove(ctx, cfg.ProxyContainer, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove proxy: %w", err)
	}

	return ProxyUp(cfg)
}

// BuildProxyImage builds the proxy Docker image. If version is non-empty,
// it's passed as a build arg to override the default xray-core version.
func BuildProxyImage(cfg config.Config, version string) error {
	proxyDir := filepath.Join(cfg.ProfilesDir, "proxy")
	args := []string{"build", "-t", cfg.ProxyImage}
	if version != "" {
		args = append(args, "--build-arg", "XRAY_VERSION="+version)
	}
	args = append(args, proxyDir)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ProxyFixRoutes sets the default route to the proxy IP in all workspace
// containers connected to the proxy network. This is needed after a system
// reboot because Docker restarts containers without running devcontainer
// lifecycle hooks (postStartCommand), so the route override is lost.
func ProxyFixRoutes(cfg config.Config) (int, error) {
	cli, err := newClientFunc()
	if err != nil {
		return 0, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{})
	if err != nil {
		return 0, fmt.Errorf("inspect network: %w", err)
	}

	var fixed int
	for _, ep := range info.Containers {
		if ep.Name == cfg.ProxyContainer {
			continue
		}
		cmd := exec.Command("docker", "exec", ep.Name,
			"ip", "route", "replace", "default", "via", cfg.ProxyIP)
		if err := cmd.Run(); err != nil {
			continue
		}
		fixed++
	}
	return fixed, nil
}

// ProxyConnectedContainers returns names of running containers on the
// ws-proxy bridge network (excluding the proxy container itself).
func ProxyConnectedContainers(cfg config.Config) ([]string, error) {
	cli, err := newClientFunc()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{})
	if err != nil {
		return nil, nil
	}

	var names []string
	for _, ep := range info.Containers {
		if ep.Name == cfg.ProxyContainer {
			continue
		}
		names = append(names, ep.Name)
	}
	return names, nil
}

// ensureProxyNetwork creates the ws-proxy bridge network if it doesn't exist.
func ensureProxyNetwork(cli DockerClient, ctx context.Context, cfg config.Config) error {
	_, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{})
	if err == nil {
		return nil
	}
	_, err = cli.NetworkCreate(ctx, cfg.ProxyNetwork, network.CreateOptions{
		Driver: "bridge",
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{Subnet: cfg.ProxySubnet},
			},
		},
	})
	return err
}

func imageExists(ctx context.Context, cli DockerClient, image string) bool {
	_, _, err := cli.ImageInspectWithRaw(ctx, image)
	return err == nil
}

// WaitForHealth polls the container health status until healthy or timeout.
// Returns nil immediately if the container has no health check configured.
func WaitForHealth(cfg config.Config, timeout time.Duration) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
		if err != nil {
			return fmt.Errorf("inspect proxy: %w", err)
		}
		if info.State.Health == nil {
			return nil // no health check configured
		}
		switch info.State.Health.Status {
		case "healthy":
			return nil
		case "unhealthy":
			return fmt.Errorf("proxy container is unhealthy")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check timed out after %s", timeout)
		case <-ticker.C:
		}
	}
}

// ProxyExec runs `docker exec <cfg.ProxyContainer> <args...>` and returns
// combined stdout+stderr. Mirrors the established shell-out pattern used by
// BuildProxyImage and ProxyFixRoutes — the SDK ContainerExecCreate +
// ContainerExecAttach path requires ~25 LOC of stdcopy demux for an identical
// effect.
//
// Used by internal/xray for `xray run -test -config /etc/xray/profiles/<name>.json`
// validation before symlink swap (CONTEXT.md D-09).
func ProxyExec(cfg config.Config, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"exec", cfg.ProxyContainer}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker exec %s %v: %w (output: %s)", cfg.ProxyContainer, args, err, string(out))
	}
	return out, nil
}

// BindMountIsWholeDir inspects the running dev-proxy container and returns
// true if its bind mount uses the whole-directory form (cfg.XrayConfig's
// parent directory mounted to /etc/xray/), false if it uses the legacy
// single-file form (cfg.XrayConfig mounted to /etc/xray/config.json).
// Returns (false, err) if the container is missing or inspect fails.
//
// PROXY-PROFILE-15: switching to whole-dir is required for the xray -test
// validation gate (D-09) to see target profile files inside the container.
// Existing operators are NOT auto-recreated (feedback_no_auto_state_mutation);
// the CLI surfaces a one-time recreate prompt via internal/xray.SwitchTo.
func BindMountIsWholeDir(cfg config.Config) (bool, error) {
	cli, err := newClientFunc()
	if err != nil {
		return false, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", cfg.ProxyContainer, err)
	}

	// HostConfig.Binds entries are colon-separated "host:container[:flag]"
	// strings. Comparing the host-side path is the reliable discriminator:
	// whole-dir bind => host == filepath.Dir(cfg.XrayConfig); single-file bind
	// => host == cfg.XrayConfig. This avoids fragile prefix matching against
	// the container-side path, which docker may normalize with or without a
	// trailing slash (ProxyUp writes "/etc/xray/" with the slash — see line
	// ~135 — and earlier code mis-handled that form).
	wholeDirHost := filepath.Dir(cfg.XrayConfig)
	for _, b := range info.HostConfig.Binds {
		parts := strings.SplitN(b, ":", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case wholeDirHost:
			return true, nil
		case cfg.XrayConfig:
			return false, nil
		}
	}
	// No xray bind found at all — treat as legacy/missing (caller decides).
	return false, nil
}

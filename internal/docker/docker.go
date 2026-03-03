package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/rtxnik/ws/internal/config"
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
	cli, err := newClient()
	if err != nil {
		return Status{}, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err != nil {
		return Status{Running: false}, nil
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

// ProxyUp starts the proxy container. Requires image to be pre-built.
func ProxyUp(cfg config.Config) error {
	cli, err := newClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Check if container already exists.
	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err == nil {
		if info.State.Running {
			return nil
		}
		return cli.ContainerStart(ctx, cfg.ProxyContainer, container.StartOptions{})
	}

	if !imageExists(ctx, cli, cfg.ProxyImage) {
		return fmt.Errorf("proxy image %q not found, run 'ws proxy rebuild' first", cfg.ProxyImage)
	}

	if _, err := os.Stat(cfg.XrayConfig); os.IsNotExist(err) {
		return fmt.Errorf("xray config not found at %s, run 'ws proxy init' first", cfg.XrayConfig)
	}

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: cfg.ProxyImage,
		},
		&container.HostConfig{
			Binds:         []string{cfg.XrayConfig + ":/etc/xray/config.json:ro"},
			CapAdd:        []string{"NET_ADMIN"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		},
		nil, nil, cfg.ProxyContainer,
	)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return err
	}

	// Remove workspace containers that referenced the old proxy.
	// Docker resolves --network=container:<name> to a container ID at
	// creation time. After proxy recreation the stored ID is stale and
	// restart alone cannot fix it — the containers must be removed so
	// that devpod recreates them with a fresh network reference.
	ids, _ := connectedContainerIDs(cli, ctx, cfg)
	removeContainers(cli, ctx, ids)

	return nil
}

// ProxyDown stops the proxy container, preserving it for later restart.
// The container is kept so that workspace containers sharing its network
// namespace via --network=container:<name> retain a valid reference.
func ProxyDown(cfg config.Config) error {
	cli, err := newClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	timeout := 10
	_ = cli.ContainerStop(ctx, cfg.ProxyContainer, container.StopOptions{Timeout: &timeout})
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

	cli, err := newClient()
	if err != nil {
		return results
	}
	defer cli.Close()

	ctx := context.Background()
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
	cli, err := newClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	reader, err := cli.ContainerLogs(context.Background(), cfg.ProxyContainer, container.LogsOptions{
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
// container is recreated and workspace connections are restored.
// Returns the number of reconnected workspace containers.
func ProxyRebuild(cfg config.Config) (int, error) {
	st, _ := ProxyStatus(cfg)
	wasRunning := st.Running

	// Build new image first — Docker allows reusing the tag while the
	// old image is still in use (old image becomes dangling).
	if err := BuildProxyImage(cfg, ""); err != nil {
		return 0, err
	}

	// Recreate the container to pick up the new image.
	var reconnected int
	if wasRunning {
		n, err := proxyRecreate(cfg)
		if err != nil {
			return 0, fmt.Errorf("restart after rebuild: %w", err)
		}
		reconnected = n
	}

	// Clean up dangling old image (best-effort).
	pruneCmd := exec.Command("docker", "image", "prune", "-f")
	_ = pruneCmd.Run()

	return reconnected, nil
}

// ProxyRestart stops and starts the proxy container.
func ProxyRestart(cfg config.Config) error {
	_ = ProxyDown(cfg)
	return ProxyUp(cfg)
}

// ProxyRecreate removes and recreates the proxy container, then removes
// workspace containers with stale network references so that devpod
// recreates them on next start.
// Returns the number of removed workspace containers.
func ProxyRecreate(cfg config.Config) (int, error) {
	return proxyRecreate(cfg)
}

func proxyRecreate(cfg config.Config) (int, error) {
	cli, err := newClient()
	if err != nil {
		return 0, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Find workspace containers sharing the proxy network.
	ids, _ := connectedContainerIDs(cli, ctx, cfg)

	// Remove old proxy container.
	timeout := 10
	_ = cli.ContainerStop(ctx, cfg.ProxyContainer, container.StopOptions{Timeout: &timeout})
	_ = cli.ContainerRemove(ctx, cfg.ProxyContainer, container.RemoveOptions{Force: true})

	// Create and start a new proxy container.
	if err := ProxyUp(cfg); err != nil {
		return 0, err
	}

	// Remove stale workspace containers (see ProxyUp comment).
	n := removeContainers(cli, ctx, ids)
	return n, nil
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

// ProxyConnectedContainers returns names of running containers that share
// the proxy container's network namespace (--network=container:dev-proxy).
func ProxyConnectedContainers(cfg config.Config) ([]string, error) {
	cli, err := newClient()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	target := "container:" + cfg.ProxyContainer
	var names []string
	for _, c := range containers {
		if c.HostConfig.NetworkMode == target {
			name := c.ID[:12]
			if len(c.Names) > 0 {
				name = c.Names[0]
				if len(name) > 0 && name[0] == '/' {
					name = name[1:]
				}
			}
			names = append(names, name)
		}
	}
	return names, nil
}

// connectedContainerIDs returns IDs of all containers (including stopped)
// that share the proxy container's network namespace.
func connectedContainerIDs(cli *client.Client, ctx context.Context, cfg config.Config) ([]string, error) {
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	target := "container:" + cfg.ProxyContainer
	var ids []string
	for _, c := range containers {
		if c.HostConfig.NetworkMode == target {
			ids = append(ids, c.ID)
		}
	}
	return ids, nil
}

// removeContainers force-removes each container by ID and returns the
// number of successful removals.
func removeContainers(cli *client.Client, ctx context.Context, ids []string) int {
	var n int
	for _, id := range ids {
		if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err == nil {
			n++
		}
	}
	return n
}

// CleanStaleProxyRefs removes stopped containers whose proxy network
// reference is stale (container created before the current proxy).
// Returns the number of removed containers.
func CleanStaleProxyRefs(cfg config.Config) int {
	cli, err := newClient()
	if err != nil {
		return 0
	}
	defer cli.Close()

	ctx := context.Background()

	proxyInfo, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err != nil {
		// Proxy container doesn't exist — remove all referencing containers.
		ids, _ := connectedContainerIDs(cli, ctx, cfg)
		return removeContainers(cli, ctx, ids)
	}

	proxyCreated, err := time.Parse(time.RFC3339Nano, proxyInfo.Created)
	if err != nil {
		return 0
	}

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return 0
	}

	target := "container:" + cfg.ProxyContainer
	var staleIDs []string
	for _, c := range containers {
		if c.HostConfig.NetworkMode == target && c.State != "running" && c.Created < proxyCreated.Unix() {
			staleIDs = append(staleIDs, c.ID)
		}
	}

	return removeContainers(cli, ctx, staleIDs)
}

func imageExists(ctx context.Context, cli *client.Client, image string) bool {
	_, _, err := cli.ImageInspectWithRaw(ctx, image)
	return err == nil
}

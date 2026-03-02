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
	"github.com/docker/docker/api/types/image"
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
func ProxyStatus() (Status, error) {
	cli, err := newClient()
	if err != nil {
		return Status{}, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	info, err := cli.ContainerInspect(ctx, config.ProxyContainer)
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
	info, err := cli.ContainerInspect(ctx, config.ProxyContainer)
	if err == nil {
		if info.State.Running {
			return nil
		}
		return cli.ContainerStart(ctx, config.ProxyContainer, container.StartOptions{})
	}

	if !imageExists(ctx, cli) {
		return fmt.Errorf("proxy image %q not found, run 'ws proxy rebuild' first", config.ProxyImage)
	}

	if _, err := os.Stat(cfg.XrayConfig); os.IsNotExist(err) {
		return fmt.Errorf("xray config not found at %s, run 'ws proxy init' first", cfg.XrayConfig)
	}

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: config.ProxyImage,
		},
		&container.HostConfig{
			Binds:         []string{cfg.XrayConfig + ":/etc/xray/config.json:ro"},
			CapAdd:        []string{"NET_ADMIN"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		},
		nil, nil, config.ProxyContainer,
	)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}

	return cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
}

// ProxyDown stops and removes the proxy container.
func ProxyDown() error {
	cli, err := newClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	timeout := 10
	_ = cli.ContainerStop(ctx, config.ProxyContainer, container.StopOptions{Timeout: &timeout})
	_ = cli.ContainerRemove(ctx, config.ProxyContainer, container.RemoveOptions{Force: true})
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

	if imageExists(ctx, cli) {
		results[2].Passed = true
	}

	info, err := cli.ContainerInspect(ctx, config.ProxyContainer)
	if err == nil && info.State.Running {
		results[3].Passed = true
	}

	return results
}

// ProxyLogs returns the last n lines of proxy container logs.
func ProxyLogs(n int) (string, error) {
	cli, err := newClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	reader, err := cli.ContainerLogs(context.Background(), config.ProxyContainer, container.LogsOptions{
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

// ProxyRebuild stops the container, removes the image, and rebuilds.
func ProxyRebuild(cfg config.Config) error {
	_ = ProxyDown()

	cli, err := newClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()
	if imageExists(ctx, cli) {
		_, _ = cli.ImageRemove(ctx, config.ProxyImage, image.RemoveOptions{Force: true})
	}

	return BuildProxyImage(cfg, "")
}

// ProxyRestart stops and starts the proxy container.
func ProxyRestart(cfg config.Config) error {
	_ = ProxyDown()
	return ProxyUp(cfg)
}

// BuildProxyImage builds the proxy Docker image. If version is non-empty,
// it's passed as a build arg to override the default xray-core version.
func BuildProxyImage(cfg config.Config, version string) error {
	proxyDir := filepath.Join(cfg.ProfilesDir, "proxy")
	args := []string{"build", "-t", config.ProxyImage}
	if version != "" {
		args = append(args, "--build-arg", "XRAY_VERSION="+version)
	}
	args = append(args, proxyDir)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func imageExists(ctx context.Context, cli *client.Client) bool {
	_, _, err := cli.ImageInspectWithRaw(ctx, config.ProxyImage)
	return err == nil
}

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/procx"
	"github.com/rtxnik/workspace-cli/internal/proxyrecipe"
)

// Timeouts for Docker operations.
const (
	timeoutRead  = 10 * time.Second
	timeoutWrite = 30 * time.Second
	timeoutStop  = 15 * time.Second
)

// PruneImages removes dangling images (`docker image prune -f`) under a hard
// deadline so a wedged daemon cannot hang the caller. Best-effort by nature.
func PruneImages() error {
	_, err := procx.RunCombined(context.Background(), timeoutRead, "docker", "image", "prune", "-f")
	return err
}

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
	defer func() { _ = cli.Close() }()

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
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutWrite)
	defer cancel()

	// Check if container already exists.
	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err == nil {
		if info.State.Running {
			// Proxy already running -- still fix routes for workspaces that
			// may have lost them after a reboot. Partial failures are warned,
			// not returned: ProxyUp is shared by up/restart/switch, and a
			// degraded route must not fail a restart or trigger the switch
			// failure path. The up command surfaces the degraded outcome via
			// its dedicated route step.
			rep, err := ProxyFixRoutes(cfg)
			if err != nil {
				return err
			}
			warnRouteFailures(rep)
			return nil
		}
		if err := cli.ContainerStart(ctx, cfg.ProxyContainer, container.StartOptions{}); err != nil {
			return err
		}
		rep, err := ProxyFixRoutes(cfg)
		if err != nil {
			return err
		}
		warnRouteFailures(rep)
		return nil
	}

	if err := proxyCreatePreflight(ctx, cli, cfg); err != nil {
		return err
	}
	if err := proxyCreateAndStart(ctx, cli, cfg); err != nil {
		return err
	}
	// Cold create fixes routes too (the doc contract above): workspaces
	// already on the network may hold a stale default route. Same warn-only
	// surfacing as the other branches.
	rep, err := ProxyFixRoutes(cfg)
	if err != nil {
		return err
	}
	warnRouteFailures(rep)
	return nil
}

// proxyCreatePreflight runs the pre-mutation validation shared by ProxyUp's
// cold path and the recreate orchestrator: image present, on-disk xray config
// present, proxy network ensured, and (additive, P6) cfg.ProxyIP not held by a
// foreign container. The foreign-IP check excludes the proxy's own endpoint and
// the recreate backup so it never false-positives during a recreate preflight.
func proxyCreatePreflight(ctx context.Context, cli DockerClient, cfg config.Config) error {
	if !imageExists(ctx, cli, cfg.ProxyImage) {
		return fmt.Errorf("proxy image %q not found, run 'ws proxy rebuild' first", cfg.ProxyImage)
	}
	if _, err := os.Stat(cfg.XrayConfig); os.IsNotExist(err) {
		return fmt.Errorf("xray config not found at %s, run 'ws proxy init' first", cfg.XrayConfig)
	}
	if err := ensureProxyNetwork(cli, ctx, cfg); err != nil {
		return fmt.Errorf("create proxy network: %w", err)
	}
	if info, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{}); err == nil {
		for _, ep := range info.Containers {
			if ep.Name == cfg.ProxyContainer || ep.Name == backupName(cfg) {
				continue
			}
			epIP := ep.IPv4Address
			if i := strings.IndexByte(epIP, '/'); i >= 0 {
				epIP = epIP[:i]
			}
			if epIP == cfg.ProxyIP {
				return fmt.Errorf("IP %s is held by container %q, not the proxy -- free it then retry", cfg.ProxyIP, ep.Name)
			}
		}
	}
	return nil
}

// proxyCreateAndStart creates and starts the proxy container with the canonical
// HostConfig (TPROXY sysctls + NET_ADMIN + whole-dir bind + UnlessStopped) and
// the static IPAMConfig endpoint. Extracted verbatim from ProxyUp's create
// block; the start error is now wrapped "start container:" so the recreate
// orchestrator can distinguish a start failure (behavior-preserving for ProxyUp,
// whose only test asserts the HostConfig, not the start error string).
func proxyCreateAndStart(ctx context.Context, cli DockerClient, cfg config.Config) error {
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
			Binds:  []string{filepath.Dir(cfg.XrayConfig) + ":/etc/xray/:ro"},
			CapAdd: []string{"NET_ADMIN"},
			// /proc/sys is mounted read-only inside a default container (runc's
			// default readonlyPaths), so the entrypoint cannot reliably write
			// these at runtime — the runtime must supply them declaratively here.
			// `lo` is created at netns init, BEFORE these are applied, so it does
			// NOT inherit the `default` values and needs explicit `lo.*` entries;
			// `eth0` is attached afterwards and inherits `default.*`. Effective
			// rp_filter is max(conf.all, conf.<iface>), so every iface must be 0.
			Sysctls: map[string]string{
				"net.ipv4.ip_forward":                  "1",
				"net.ipv4.conf.all.rp_filter":          "0",
				"net.ipv4.conf.default.rp_filter":      "0",
				"net.ipv4.conf.lo.rp_filter":           "0",
				"net.ipv4.conf.all.route_localnet":     "1",
				"net.ipv4.conf.default.route_localnet": "1",
				"net.ipv4.conf.lo.route_localnet":      "1",
				// IPv4-only proxy: disable the IPv6 stack in the container netns so
				// there is no v6 to leak around the v4 TPROXY capture (H4 load-bearing
				// layer; the entrypoint ip6tables DROP is the belt on top). Set here
				// because /proc/sys is read-only inside a default container.
				"net.ipv6.conf.all.disable_ipv6":     "1",
				"net.ipv6.conf.default.disable_ipv6": "1",
				"net.ipv6.conf.lo.disable_ipv6":      "1",
			},
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
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// ProxyDown stops the proxy container. Workspace containers on the
// ws-proxy bridge network are unaffected and resume connectivity
// when the proxy is started again.
func ProxyDown(cfg config.Config) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

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
	defer func() { _ = cli.Close() }()

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
	defer func() { _ = cli.Close() }()

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
	defer func() { _ = reader.Close() }()

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

	if err := BuildProxyImage(cfg, "", false); err != nil {
		return err
	}

	if wasRunning {
		if err := proxyRecreate(cfg); err != nil {
			return fmt.Errorf("restart after rebuild: %w", err)
		}
	}

	// Clean up dangling old image (best-effort, bounded).
	_ = PruneImages()

	return nil
}

// RestartContainerNoVerify stops then starts the proxy container with no
// post-start health gate -- today's ProxyRestart body, preserving the
// missing/stopped idempotency. SwitchTo wires its restart step to this so it
// keeps owning its single profile-aware liveness wait (§2.4).
func RestartContainerNoVerify(cfg config.Config) error {
	if err := ProxyDown(cfg); err != nil {
		return err
	}
	return ProxyUp(cfg)
}

// ProxyRestart restarts the proxy container and verifies it became healthy.
// Same container, so no backup applies; on failure it leaves the container
// exactly where it landed (D-10: no auto-state-mutation) and returns an honest
// error, routing a likely-broken config to 'ws proxy doctor' (matrix RS1-RS3).
func ProxyRestart(cfg config.Config) error {
	if err := ProxyDown(cfg); err != nil {
		return fmt.Errorf("%w -- proxy left running, restart aborted", err)
	}
	if err := ProxyUp(cfg); err != nil {
		return fmt.Errorf("restart failed: proxy is DOWN -- %w. Try 'ws proxy up'; logs: docker logs %s --tail 50", err, cfg.ProxyContainer)
	}
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), proxyHealthTimeout)
	defer cancel()
	ok, weak, verr := verifyHealthyFn(ctx, cli, cfg, proxyHealthTimeout, healthStartGrace)
	if verr != nil {
		return fmt.Errorf("proxy restarted but is unhealthy -- %w. Likely a broken xray config; run 'ws proxy doctor'", verr)
	}
	if weak {
		output.Warn("proxy restarted (no healthcheck -- liveness unverified)")
	}
	_ = ok
	return nil
}

// ProxyRecreate removes and recreates the proxy container on the
// ws-proxy bridge network. Workspace containers are unaffected --
// they keep their own network namespace and resume connectivity
// when the new proxy comes up with the same IP.
func ProxyRecreate(cfg config.Config) error {
	return proxyRecreate(cfg)
}

// buildProxyArgs assembles the `docker build` arguments, gating on the recipe
// verification result. On drift without allowDrift it returns an error; with
// allowDrift it stamps the datapath label "unverified" so the doctor still flags
// the build rather than trusting it.
func buildProxyArgs(cfg config.Config, version string, res proxyrecipe.Result, allowDrift bool) ([]string, error) {
	datapath := res.Mode
	if !res.OK {
		if !allowDrift {
			return nil, fmt.Errorf("proxy recipe drift: %s. Run 'chezmoi apply' to restore the canonical recipe, or rebuild intentionally with 'ws proxy rebuild --allow-drift'", res.DriftSummary())
		}
		datapath = "unverified"
	}

	args := []string{
		"build", "-t", cfg.ProxyImage,
		"--label", LabelDatapath + "=" + datapath,
		"--label", LabelRecipe + "=" + res.CombinedDigest,
	}
	if version != "" {
		args = append(args, "--build-arg", "XRAY_VERSION="+version)
	}
	args = append(args, filepath.Join(cfg.ProfilesDir, "proxy"))
	return args, nil
}

// BuildProxyImage builds the proxy Docker image. It first verifies the on-disk
// recipe against the embedded content pin (C5): a drifted recipe is refused
// unless allowDrift is set (in which case the image is stamped "unverified").
// If version is non-empty it is passed as the XRAY_VERSION build arg.
//
// The build streams progress to the terminal and can legitimately take minutes;
// it is intentionally left unbounded (a deadline would kill a valid long build
// mid-run). Ctrl-C interrupts it if it truly wedges.
func BuildProxyImage(cfg config.Config, version string, allowDrift bool) error {
	res, err := proxyrecipe.Verify(cfg.ProfilesDir)
	if err != nil {
		return fmt.Errorf("verify proxy recipe: %w", err)
	}
	args, err := buildProxyArgs(cfg, version, res, allowDrift)
	if err != nil {
		return err
	}

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// FixRoutesReport describes the outcome of a best-effort route-fix pass.
type FixRoutesReport struct {
	Fixed     int      // containers whose default route was replaced
	Attempted int      // containers on the proxy network (excluding the proxy itself)
	Failures  []string // "name: error" for each container whose exec failed
}

// RouteFixError reports a partial route-fix failure: the pass itself ran
// (transport was fine) but one or more workspace containers did not get
// their default route replaced and therefore egress DIRECT.
type RouteFixError struct {
	Report FixRoutesReport
}

func (e *RouteFixError) Error() string {
	return fmt.Sprintf("%d of %d workspace container(s) kept a DIRECT route: %s",
		len(e.Report.Failures), e.Report.Attempted, strings.Join(e.Report.Failures, "; "))
}

// Err converts the report into its surfacing contract: a *RouteFixError when
// any workspace failed, nil otherwise. Callers that must fail loud on partial
// failure (the up command's route step) return this; callers that must not
// abort (ProxyUp branches, recreate COMMIT/rollback) render warnRouteFailures
// instead.
func (rep FixRoutesReport) Err() error {
	if len(rep.Failures) == 0 {
		return nil
	}
	return &RouteFixError{Report: rep}
}

// fixRouteExecFn runs the route-replace command for a single container.
// Extracted as a var so tests can stub it without shelling out.
var fixRouteExecFn = func(containerName, proxyIP string) error {
	_, err := procx.RunCombined(context.Background(), timeoutRead, "docker", "exec", containerName,
		"ip", "route", "replace", "default", "via", proxyIP)
	return err
}

// ProxyFixRoutes sets the default route to the proxy IP in all workspace
// containers connected to the proxy network. This is needed after a system
// reboot because Docker restarts containers without running devcontainer
// lifecycle hooks (postStartCommand), so the route override is lost.
func ProxyFixRoutes(cfg config.Config) (FixRoutesReport, error) {
	cli, err := newClientFunc()
	if err != nil {
		return FixRoutesReport{}, fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{})
	if err != nil {
		return FixRoutesReport{}, fmt.Errorf("inspect network: %w", err)
	}

	var rep FixRoutesReport
	for _, ep := range info.Containers {
		if ep.Name == cfg.ProxyContainer {
			continue
		}
		rep.Attempted++
		if err := fixRouteExecFn(ep.Name, cfg.ProxyIP); err != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("%s: %v", ep.Name, err))
			continue
		}
		rep.Fixed++
	}
	return rep, nil
}

// warnRouteFailures renders a route-fix report loudly without failing the
// caller: every failed workspace is named and the remediation command is
// spelled out. Shared surfacing point for the paths that must not abort on a
// partial failure (ProxyUp branches, recreate COMMIT/rollback).
func warnRouteFailures(rep FixRoutesReport) {
	if len(rep.Failures) == 0 {
		return
	}
	for _, f := range rep.Failures {
		output.Warn("route fix failed -- " + f)
	}
	output.Warn(fmt.Sprintf("%d of %d workspace container(s) kept a DIRECT route (unproxied egress) -- run 'ws proxy fix-routes'",
		len(rep.Failures), rep.Attempted))
}

// ProxyConnectedContainers returns names of running containers on the
// ws-proxy bridge network (excluding the proxy container itself). A missing
// proxy network is tolerated as zero connected containers (nil, nil), which
// keeps `ws proxy down`/`restart` idempotent; any other NetworkInspect
// failure is owned and propagated so callers can fail closed rather than
// mistaking an unreadable network for an empty one (mirrors ProxyFixRoutes).
func ProxyConnectedContainers(cfg config.Config) ([]string, error) {
	cli, err := newClientFunc()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.NetworkInspect(ctx, cfg.ProxyNetwork, network.InspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect proxy network: %w", err)
	}

	var names []string
	for _, ep := range info.Containers {
		if ep.Name == cfg.ProxyContainer {
			continue
		}
		names = append(names, ep.Name)
	}
	sort.Strings(names) // deterministic order -- Go map iteration is randomized
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

// Image LABEL keys stamped by BuildProxyImage and read by the datapath-contract
// doctor check (C5).
const (
	LabelDatapath = "ws.proxy.datapath"
	LabelRecipe   = "ws.proxy.recipe"
)

// ImageLabels returns the LABELs of cfg.ProxyImage. It parses the raw inspect
// JSON (.Config.Labels) rather than the typed struct so it is independent of
// docker SDK version churn in the ImageInspect type.
func ImageLabels(cfg config.Config) (map[string]string, error) {
	cli, err := newClientFunc()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	_, raw, err := cli.ImageInspectWithRaw(ctx, cfg.ProxyImage)
	if err != nil {
		return nil, fmt.Errorf("inspect image %q: %w", cfg.ProxyImage, err)
	}
	var parsed struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse image inspect for %q: %w", cfg.ProxyImage, err)
	}
	if parsed.Config.Labels == nil {
		return map[string]string{}, nil
	}
	return parsed.Config.Labels, nil
}

// WaitForHealth waits until cfg.ProxyContainer is healthy, its liveness is
// unverifiable (no HEALTHCHECK), or the timeout elapses. It is the thin,
// client-owning wrapper over verifyHealthy used by `ws proxy up`/`rebuild` and
// by internal/xray.SwitchTo, so those call sites keep their (cfg, timeout) shape
// and budgets. A terminal container state ("exited"/"dead") fails fast; a
// container still coming up is tolerated for the full budget so a slow start
// under load is not mistaken for a crash. A missing HEALTHCHECK is surfaced as a
// warning (liveness unverified) rather than a silent healthy result.
func WaitForHealth(cfg config.Config, timeout time.Duration) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// crashGrace == timeout: only a terminal state fails fast here; a not-yet-
	// running container is tolerated for the whole budget (the tight fast-fail
	// grace is reserved for the transactional recreate/restart paths).
	_, weak, verr := verifyHealthy(ctx, cli, cfg, timeout, timeout)
	if verr != nil {
		return verr
	}
	if weak {
		output.Warn("proxy liveness unverified: container has no HEALTHCHECK configured")
	}
	return nil
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
	out, err := procx.RunCombined(context.Background(), timeoutRead, "docker", cmdArgs...)
	if err != nil {
		return out, fmt.Errorf("docker exec %s %v: %w (output: %s)", cfg.ProxyContainer, args, err, string(out))
	}
	return out, nil
}

// bindMountIsWholeDir is the comparator shared by BindMountIsWholeDir and
// VerifyProxyReadyForReload. Takes an already-inspected ContainerJSON to
// avoid a duplicate ContainerInspect round-trip when both checks run.
//
// Returns true if HostConfig.Binds contains an entry whose host side is
// filepath.Dir(cfg.XrayConfig) (whole-dir mount), false if it contains an
// entry whose host side is cfg.XrayConfig itself (legacy single-file
// mount). Returns (false, nil) if no xray-related bind is found at all
// — the caller decides whether that's legacy or missing.
func bindMountIsWholeDir(info types.ContainerJSON, cfg config.Config) (bool, error) {
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
	return false, nil
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
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", cfg.ProxyContainer, err)
	}
	return bindMountIsWholeDir(info, cfg)
}

// VerifyProxyReadyForReload runs pre-flight checks required before
// triggering a config reload via container restart. Validation-only —
// no state mutation (D-10 / feedback_no_auto_state_mutation:
// validation-before-mutation is OK, automation-after-failure is NOT).
//
// Checks (all must pass for nil return):
//   - container exists (not 'no such container')
//   - container is in running state
//   - whole-dir bind mount is active (else profile swap is invisible to
//     xray inside the container)
//
// Callers gate their mutations on a nil return; on non-nil they render
// the error and exit without touching state.
func VerifyProxyReadyForReload(cfg config.Config) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeoutRead)
	defer cancel()

	info, err := cli.ContainerInspect(ctx, cfg.ProxyContainer)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return fmt.Errorf("proxy container %q not found — run 'ws proxy up' first", cfg.ProxyContainer)
		}
		return fmt.Errorf("inspect proxy container: %w", err)
	}
	if !info.State.Running {
		return fmt.Errorf("proxy container %q is not running (state=%s) — run 'ws proxy up' first", cfg.ProxyContainer, info.State.Status)
	}

	isWhole, err := bindMountIsWholeDir(info, cfg)
	if err != nil {
		return fmt.Errorf("inspect proxy bind mount: %w", err)
	}
	if !isWhole {
		return fmt.Errorf("proxy container %q uses legacy single-file bind mount — run 'ws proxy rebuild --force' to migrate to whole-dir bind", cfg.ProxyContainer)
	}

	return nil
}

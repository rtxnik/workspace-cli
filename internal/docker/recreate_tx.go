package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// backupName is the deterministic name the recreate transaction renames the
// previous-good proxy container to, so an interrupted run is recoverable on the
// next invocation (spec §2.2).
func backupName(cfg config.Config) string {
	return cfg.ProxyContainer + "-backup"
}

// proxyHealthTimeout is the post-create health-verify budget. A package var (not
// const) so tests shrink it to ms. 60s matches cmd/proxy.go's WaitForHealth
// precedent; a freshly created container resets HEALTHCHECK to "starting" so the
// 15s SwitchTo liveness budget would spuriously time it out (spec §3).
var proxyHealthTimeout = 60 * time.Second

// Poll cadence and the grace window before a not-yet-running container is judged
// crashed. Vars so tests can shrink them.
var (
	healthPollInterval = 1 * time.Second
	healthStartGrace   = 2 * time.Second
)

// verifyHealthy polls ContainerInspect until cfg.ProxyContainer is healthy,
// fails, or the timeout elapses (spec §3). Returns:
//   - ok=true, weak=false  : State.Health.Status == "healthy"
//   - ok=true, weak=true   : State.Health == nil (no HEALTHCHECK) + Running
//   - ok=false, err!=nil   : unhealthy, crashed (Running==false past grace), or timeout
func verifyHealthy(ctx context.Context, cli DockerClient, cfg config.Config, timeout time.Duration) (ok bool, weak bool, err error) {
	deadline := time.Now().Add(timeout)
	start := time.Now()
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()

	for {
		info, ierr := cli.ContainerInspect(ctx, cfg.ProxyContainer)
		if ierr != nil {
			return false, false, fmt.Errorf("inspect proxy: %w", ierr)
		}
		switch {
		case !info.State.Running:
			if time.Since(start) >= healthStartGrace {
				return false, false, fmt.Errorf("proxy container exited before becoming healthy")
			}
		case info.State.Health == nil:
			return true, true, nil
		case info.State.Health.Status == "healthy":
			return true, false, nil
		case info.State.Health.Status == "unhealthy":
			return false, false, fmt.Errorf("proxy container is unhealthy")
		}
		if !time.Now().Before(deadline) {
			return false, false, fmt.Errorf("proxy health check timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return false, false, fmt.Errorf("proxy health check timed out after %s", timeout)
		case <-ticker.C:
		}
	}
}

package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
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

// verifyHealthyFn is the test seam for verifyHealthy; the orchestrator,
// verifyNew, and ProxyRestart call through it so tests drive health
// deterministically. Declared here (its first reader) -- not in Task 2 -- so it
// is never an unused package var at an earlier task's golangci gate.
var verifyHealthyFn = verifyHealthy

// recreateMutateTimeout bounds each mutating forward phase and the fresh COMMIT
// and ROLLBACK contexts (spec §5). A package var (defaults to timeoutWrite) so
// the context-budget regression test can shrink it. Production value = 30s.
var recreateMutateTimeout = timeoutWrite

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

// proxyRecreate is the Level-B transactional recreate orchestrator (spec §2.2/
// §2.3/§6). PREFLIGHT validates before any destruction; the previous container
// is preserved as a backup until the new one is verified healthy, then COMMIT
// drops the backup or ROLLBACK restores it. Per-phase contexts prevent a long
// health wait from expiring the mutating/commit budget (spec §5).
func proxyRecreate(cfg config.Config) error {
	cli, err := newClientFunc()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	// --- Classification (before any mutation) ---
	cctx, ccancel := context.WithTimeout(context.Background(), recreateMutateTimeout)
	defer ccancel()

	_, perr := cli.ContainerInspect(cctx, cfg.ProxyContainer)
	primaryExists := perr == nil
	if perr != nil && !errdefs.IsNotFound(perr) {
		return fmt.Errorf("inspect proxy: %w", perr)
	}
	_, berr := cli.ContainerInspect(cctx, backupName(cfg))
	backupExists := berr == nil
	if berr != nil && !errdefs.IsNotFound(berr) {
		return fmt.Errorf("inspect backup: %w", berr)
	}

	// --- Stale-backup triage (§6.2) ---
	if backupExists {
		if err := triageStaleBackup(cctx, cli, cfg, primaryExists); err != nil {
			return err
		}
		if !primaryExists {
			return nil // Case B: restored last-known-good; do NOT re-run swap (DR-3)
		}
		// Case A: garbage removed; continue with primary present.
	}

	// --- Cold create (primary absent, no backup) ---
	if !primaryExists {
		if err := proxyCreatePreflight(cctx, cli, cfg); err != nil {
			return err
		}
		if err := proxyCreateAndStart(cctx, cli, cfg); err != nil {
			return err
		}
		return verifyNew(cli, cfg) // honest failure, no rollback target
	}

	// --- PREFLIGHT (validate) before destroying OLD ---
	if err := proxyCreatePreflight(cctx, cli, cfg); err != nil {
		return err
	}

	// --- BACKUP: stop + rename + disconnect ---
	// The stop is LOAD-BEARING: RestartPolicy=UnlessStopped will NOT auto-restart
	// a manually-stopped container, so once stopped the backup cannot resurrect
	// and re-grab the name/IP mid-swap. Do not optimize this stop away (spec §2.2).
	stopTimeout := 10
	if err := cli.ContainerStop(cctx, cfg.ProxyContainer, container.StopOptions{Timeout: &stopTimeout}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("stop proxy: %w -- proxy left running, no changes made", err)
	}
	if err := cli.ContainerRename(cctx, cfg.ProxyContainer, backupName(cfg)); err != nil {
		_ = cli.ContainerStart(cctx, cfg.ProxyContainer, container.StartOptions{})
		return fmt.Errorf("rename to backup failed: %w -- restored running proxy", err)
	}
	if err := cli.NetworkDisconnect(cctx, cfg.ProxyNetwork, backupName(cfg), false); err != nil {
		_ = cli.ContainerRename(cctx, backupName(cfg), cfg.ProxyContainer)
		_ = cli.ContainerStart(cctx, cfg.ProxyContainer, container.StartOptions{})
		return fmt.Errorf("could not free proxy IP %s: %w -- rolled back", cfg.ProxyIP, err)
	}

	// --- CREATE-NEW ---
	if err := proxyCreateAndStart(cctx, cli, cfg); err != nil {
		return rollbackToBackup(cli, cfg, fmt.Errorf("new proxy failed: %w -- rolled back", err))
	}

	// --- VERIFY (own ctx, §5 phase b) ---
	vctx, vcancel := context.WithTimeout(context.Background(), proxyHealthTimeout)
	defer vcancel()
	ok, weak, verr := verifyHealthyFn(vctx, cli, cfg, proxyHealthTimeout)
	if verr != nil {
		return rollbackToBackup(cli, cfg, fmt.Errorf("%w -- rolled back. Likely a broken on-disk xray config (run 'ws proxy doctor'/'upgrade-config')", verr))
	}
	if weak {
		output.Warn("proxy started (no healthcheck -- liveness unverified)")
	}
	_ = ok

	// --- COMMIT (fresh ctx, §5 phase c; all best-effort) ---
	commitCtx, commitCancel := context.WithTimeout(context.Background(), recreateMutateTimeout)
	defer commitCancel()
	_ = cli.ContainerRemove(commitCtx, backupName(cfg), container.RemoveOptions{Force: true})
	_, _ = ProxyFixRoutes(cfg)
	// §8 end-state observability: name which container now serves cfg.ProxyIP so
	// the reused-IP swap is unambiguous and LEDGER-quotable (vocab: NEW / restored
	// OLD / DOWN -- "restored OLD" and "DOWN" appear in the rollback/CRITICAL paths).
	output.Success(fmt.Sprintf("recreate committed -- NEW proxy now serving %s", cfg.ProxyIP))
	return nil
}

// verifyNew runs the post-create health verify with its own ctx and maps a
// failure to an honest error (used by the cold-create path, which has no
// rollback target).
func verifyNew(cli DockerClient, cfg config.Config) error {
	vctx, cancel := context.WithTimeout(context.Background(), proxyHealthTimeout)
	defer cancel()
	ok, weak, verr := verifyHealthyFn(vctx, cli, cfg, proxyHealthTimeout)
	if verr != nil {
		return fmt.Errorf("new proxy failed health check: %w", verr)
	}
	if weak {
		output.Warn("proxy started (no healthcheck -- liveness unverified)")
	}
	_ = ok
	output.Success(fmt.Sprintf("proxy created -- NEW proxy now serving %s", cfg.ProxyIP))
	return nil
}

// triageStaleBackup handles a leftover backup found at PREFLIGHT (§6.2). Case A
// (primary present): the backup is garbage from a failed cleanup -> force-remove.
// Case B (primary absent): the backup is the last-known-good after an interrupted
// recreate -> restore it (re-reserve IP, start, rename) and report; the caller
// returns success without re-running the swap (DR-3).
func triageStaleBackup(ctx context.Context, cli DockerClient, cfg config.Config, primaryExists bool) error {
	if primaryExists {
		if err := cli.ContainerRemove(ctx, backupName(cfg), container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove stale backup: %w", err)
		}
		return nil
	}
	if err := cli.NetworkConnect(ctx, cfg.ProxyNetwork, backupName(cfg), &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: cfg.ProxyIP},
	}); err != nil {
		return fmt.Errorf("restore backup network: %w", err)
	}
	if err := cli.ContainerStart(ctx, backupName(cfg), container.StartOptions{}); err != nil {
		return fmt.Errorf("restore backup start: %w", err)
	}
	if err := cli.ContainerRename(ctx, backupName(cfg), cfg.ProxyContainer); err != nil {
		return fmt.Errorf("restore backup rename: %w", err)
	}
	output.Info("recovered an interrupted recreate: restored OLD proxy from the backup")
	return nil
}

type rollbackState struct {
	newExists       bool // broken NEW may still hold the name/IP
	backupConnected bool
	backupRunning   bool
	backupRenamed   bool
}

// rollbackToBackup restores the backup to cfg.ProxyContainer (§2.3) and returns
// the honest, wrapped original error. It tracks real progress in rollbackState
// so that a double fault (any sub-step failing) yields a CRITICAL message built
// from the actual situation, enumerating only the remaining manual recovery
// commands. If the restore succeeds but the restored backup is itself unhealthy
// (shared broken on-disk config), it returns the honest RV error.
func rollbackToBackup(cli DockerClient, cfg config.Config, origErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), recreateMutateTimeout)
	defer cancel()

	st := rollbackState{newExists: true}

	// 1+2: remove the broken NEW first (frees IP+endpoint before reattaching).
	stopTimeout := 10
	_ = cli.ContainerStop(ctx, cfg.ProxyContainer, container.StopOptions{Timeout: &stopTimeout})
	if err := cli.ContainerRemove(ctx, cfg.ProxyContainer, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return criticalRollback(cfg, st, origErr, fmt.Errorf("remove broken new proxy: %w", err))
	}
	st.newExists = false

	// 3: re-reserve the IP for the backup.
	if err := cli.NetworkConnect(ctx, cfg.ProxyNetwork, backupName(cfg), &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: cfg.ProxyIP},
	}); err != nil {
		return criticalRollback(cfg, st, origErr, fmt.Errorf("reconnect backup to network: %w", err))
	}
	st.backupConnected = true

	// 4: start the backup.
	if err := cli.ContainerStart(ctx, backupName(cfg), container.StartOptions{}); err != nil {
		return criticalRollback(cfg, st, origErr, fmt.Errorf("start backup: %w", err))
	}
	st.backupRunning = true

	// 5: rename backup -> primary so status/health reads resolve.
	if err := cli.ContainerRename(ctx, backupName(cfg), cfg.ProxyContainer); err != nil {
		return criticalRollback(cfg, st, origErr, fmt.Errorf("rename backup to primary: %w", err))
	}
	st.backupRenamed = true

	// 6: best-effort verify of the restored backup + fix routes.
	vctx, vcancel := context.WithTimeout(context.Background(), proxyHealthTimeout)
	defer vcancel()
	ok, _, verr := verifyHealthyFn(vctx, cli, cfg, proxyHealthTimeout)
	_, _ = ProxyFixRoutes(cfg)
	if verr != nil || !ok {
		return fmt.Errorf("recreate failed AND the restored proxy is also unhealthy -- on-disk xray config is likely broken; run 'ws proxy doctor'. Original failure: %w", origErr)
	}
	return origErr
}

// criticalRollback builds a CRITICAL multi-line error from the rollback state,
// printing only the manual-recovery commands still required and never a
// command that would now fail (e.g. no 'network connect --ip' once connected).
func criticalRollback(cfg config.Config, st rollbackState, origErr, faultErr error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "CRITICAL: recreate failed and automatic rollback also failed -- the proxy is DOWN.\n")
	fmt.Fprintf(&b, "Original failure: %v\nRollback fault: %v\n", origErr, faultErr)
	b.WriteString("Manual recovery (run the remaining steps):\n")
	if st.newExists {
		fmt.Fprintf(&b, "  docker rm -f %s\n", cfg.ProxyContainer)
	}
	if !st.backupConnected {
		fmt.Fprintf(&b, "  docker network connect --ip %s %s %s\n", cfg.ProxyIP, cfg.ProxyNetwork, backupName(cfg))
	}
	if !st.backupRunning {
		fmt.Fprintf(&b, "  docker start %s\n", backupName(cfg))
	}
	if !st.backupRenamed {
		fmt.Fprintf(&b, "  docker rename %s %s\n", backupName(cfg), cfg.ProxyContainer)
	}
	return errors.New(b.String())
}

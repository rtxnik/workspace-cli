package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// captureStderr runs fn with os.Stderr redirected and returns everything fn
// wrote to it. Capture-and-assert twin of cmd's quietStderr: keeps
// passing-test output pristine while letting tests assert operator-facing
// warnings.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	os.Stderr = orig
	_ = w.Close()
	var b strings.Builder
	if _, err := io.Copy(&b, r); err != nil {
		t.Fatalf("drain stderr pipe: %v", err)
	}
	_ = r.Close()
	return b.String()
}

// fixRoutesNetworkInspFn returns a proxy-network topology with the proxy's
// own endpoint holding cfg.ProxyIP (so recreate preflight sees no foreign
// holder) plus one workspace container.
func fixRoutesNetworkInspFn(_ context.Context, _ string, _ network.InspectOptions) (network.Inspect, error) {
	return network.Inspect{
		Containers: map[string]network.EndpointResource{
			"abc": {Name: "ws-proxy", IPv4Address: "172.30.0.2/24"},
			"def": {Name: "my-workspace", IPv4Address: "172.30.0.3/24"},
		},
	}, nil
}

// TestVerify_E_FixRoutesFailuresSwallowedByErrOnlyCaller recreates audit
// claim E (L2-05 / finding #2): ProxyFixRoutes records per-workspace failures
// in rep.Failures while returning err == nil, so a caller binding only the
// error swallows the report. rep.Err() is the surfacing contract the
// up/recreate paths now use: a typed non-nil error exactly when Failures is
// non-empty.
func TestVerify_E_FixRoutesFailuresSwallowedByErrOnlyCaller(t *testing.T) {
	mock := &mockClient{networkInspFn: fixRoutesNetworkInspFn}
	defer withMock(mock)()
	restore, _ := withFixRouteExec(func(name, _ string) error {
		if name == "my-workspace" {
			return errors.New("exit status 1")
		}
		return nil
	})
	defer restore()

	rep, err := ProxyFixRoutes(testCfg())
	// The swallow mechanism: transport error is nil even though a workspace failed.
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if rep.Attempted != 1 || rep.Fixed != 0 || len(rep.Failures) != 1 {
		t.Fatalf("want Attempted=1 Fixed=0 len(Failures)=1, got %+v", rep)
	}
	// The surfacing contract.
	serr := rep.Err()
	if serr == nil {
		t.Fatal("rep.Err() must be non-nil when Failures is non-empty")
	}
	var rf *RouteFixError
	if !errors.As(serr, &rf) {
		t.Fatalf("rep.Err() must be a *RouteFixError, got %T", serr)
	}
	if !strings.Contains(serr.Error(), "my-workspace") {
		t.Errorf("error text must name the failing workspace, got %q", serr.Error())
	}
}

func TestFixRoutesReportErr_NilOnCleanReport(t *testing.T) {
	rep := FixRoutesReport{Fixed: 2, Attempted: 2}
	if err := rep.Err(); err != nil {
		t.Fatalf("clean report must convert to a nil error, got %v", err)
	}
	if err := (FixRoutesReport{}).Err(); err != nil {
		t.Fatalf("empty report (no workspaces) must convert to a nil error, got %v", err)
	}
}

// TestProxyUp_RunningBranch_WarnsRouteFixFailures: ProxyUp on an
// already-running proxy must stay non-fatal on a partial route-fix failure
// (ProxyRestart and the profile-switch restart depend on that contract) but
// must name the failed workspace on stderr instead of discarding the report.
func TestProxyUp_RunningBranch_WarnsRouteFixFailures(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{State: &types.ContainerState{Running: true}},
				Config:            &container.Config{},
			}, nil
		},
		networkInspFn: fixRoutesNetworkInspFn,
	}
	defer withMock(mock)()
	restore, _ := withFixRouteExec(func(string, string) error { return errors.New("exit status 1") })
	defer restore()

	var err error
	got := captureStderr(t, func() { err = ProxyUp(testCfg()) })
	if err != nil {
		t.Fatalf("partial route-fix failure must not fail ProxyUp, got %v", err)
	}
	if !strings.Contains(got, "my-workspace") {
		t.Errorf("stderr must name the failed workspace; got %q", got)
	}
	if !strings.Contains(got, "fix-routes") {
		t.Errorf("stderr must point at the remediation command; got %q", got)
	}
}

// TestProxyUp_StartBranch_WarnsRouteFixFailures: same surfacing on the
// stopped-container start branch.
func TestProxyUp_StartBranch_WarnsRouteFixFailures(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{State: &types.ContainerState{Running: false}},
				Config:            &container.Config{},
			}, nil
		},
		networkInspFn: fixRoutesNetworkInspFn,
	}
	defer withMock(mock)()
	restore, _ := withFixRouteExec(func(string, string) error { return errors.New("exit status 1") })
	defer restore()

	var err error
	got := captureStderr(t, func() { err = ProxyUp(testCfg()) })
	if err != nil {
		t.Fatalf("partial route-fix failure must not fail ProxyUp, got %v", err)
	}
	if !strings.Contains(got, "my-workspace") {
		t.Errorf("stderr must name the failed workspace; got %q", got)
	}
}

// TestProxyUp_ColdCreateBranch_WarnsRouteFixFailures (DR-HB2-8): the
// cold-create branch (container absent) must run the same route-fix pass as
// the other two branches -- ProxyUp's contract says routes are fixed after
// starting, and workspaces already on the network may hold a stale default
// route -- and surface failures without failing the start.
func TestProxyUp_ColdCreateBranch_WarnsRouteFixFailures(t *testing.T) {
	mock := &mockClient{
		// default inspectFn returns not-found -> cold-create path
		imageInspFn: func(_ context.Context, _ string) (types.ImageInspect, []byte, error) {
			return types.ImageInspect{}, nil, nil
		},
		networkInspFn: func(_ context.Context, _ string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{
				Containers: map[string]network.EndpointResource{
					"def": {Name: "my-workspace", IPv4Address: "172.30.0.3/24"},
				},
			}, nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	restore, _ := withFixRouteExec(func(string, string) error { return errors.New("exit status 1") })
	defer restore()

	var err error
	got := captureStderr(t, func() { err = ProxyUp(cfg) })
	if err != nil {
		t.Fatalf("partial route-fix failure must not fail cold-create ProxyUp, got %v", err)
	}
	if !strings.Contains(got, "my-workspace") {
		t.Errorf("stderr must name the failed workspace; got %q", got)
	}
}

// TestProxyRecreate_CommitSurfacesRouteFixFailures: COMMIT stays best-effort
// (a committed recreate must not be reported as a failed transaction -- the
// recreate/rebuild/update callers all frame a non-nil error as "recreate
// failed"), but a workspace that did not get its route back must be named
// loudly with the remediation command.
func TestProxyRecreate_CommitSurfacesRouteFixFailures(t *testing.T) {
	shrinkHealthTimers(t)
	mock := &mockClient{
		inspectFn: func(_ context.Context, id string) (types.ContainerJSON, error) {
			if id == "ws-proxy-backup" {
				return types.ContainerJSON{}, errdefs.NotFound(errors.New("no backup"))
			}
			return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
				State: &types.ContainerState{Running: true}}, Config: &container.Config{}}, nil
		},
		imageInspFn: func(_ context.Context, _ string) (types.ImageInspect, []byte, error) {
			return types.ImageInspect{}, nil, nil
		},
		networkInspFn: fixRoutesNetworkInspFn,
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, false, nil
	})()
	restore, _ := withFixRouteExec(func(string, string) error { return errors.New("exit status 1") })
	defer restore()

	var err error
	got := captureStderr(t, func() { err = ProxyRecreate(cfg) })
	if err != nil {
		t.Fatalf("committed recreate must not fail on a route-fix failure, got %v", err)
	}
	if !strings.Contains(got, "my-workspace") {
		t.Errorf("COMMIT must name the failed workspace; got %q", got)
	}
	if !strings.Contains(got, "fix-routes") {
		t.Errorf("COMMIT must point at the remediation command; got %q", got)
	}
}

// TestProxyRecreate_RollbackSurfacesRouteFixFailures: the rollback path
// already returns the original failure (non-zero outcome); this pins that the
// restored backup's route-fix pass also names its failures instead of
// discarding both return values.
func TestProxyRecreate_RollbackSurfacesRouteFixFailures(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := recreateForwardMock(t, &seq)
	mock.networkInspFn = fixRoutesNetworkInspFn
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	calls := 0
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		calls++
		if calls == 1 {
			return false, false, errors.New("proxy container is unhealthy") // NEW unhealthy
		}
		return true, false, nil // restored backup healthy
	})()
	restore, _ := withFixRouteExec(func(string, string) error { return errors.New("exit status 1") })
	defer restore()

	var err error
	got := captureStderr(t, func() { err = ProxyRecreate(cfg) })
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("rollback must still return the original failure, got: %v", err)
	}
	if !strings.Contains(got, "my-workspace") {
		t.Errorf("rollback route-fix failures must be named on stderr; got %q", got)
	}
}

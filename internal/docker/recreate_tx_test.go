package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rtxnik/workspace-cli/internal/config"
)

func shrinkHealthTimers(t *testing.T) {
	t.Helper()
	op, opi, og := proxyHealthTimeout, healthPollInterval, healthStartGrace
	proxyHealthTimeout = 50 * time.Millisecond
	healthPollInterval = 2 * time.Millisecond
	healthStartGrace = 0
	t.Cleanup(func() {
		proxyHealthTimeout, healthPollInterval, healthStartGrace = op, opi, og
	})
}

func writeTempXrayConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func swapVerify(fn func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error)) func() {
	orig := verifyHealthyFn
	verifyHealthyFn = fn
	return func() { verifyHealthyFn = orig }
}

func assertSeq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("seq mismatch:\n got=%v\nwant=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seq[%d]=%q want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

func TestVerifyHealthy_Healthy(t *testing.T) {
	shrinkHealthTimers(t)
	mock := &mockClient{inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
		return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
			State: &types.ContainerState{Running: true, Health: &types.Health{Status: "healthy"}}},
			Config: &container.Config{}}, nil
	}}
	ok, weak, err := verifyHealthy(context.Background(), mock, testCfg(), proxyHealthTimeout)
	if err != nil || !ok || weak {
		t.Fatalf("want ok=true weak=false err=nil, got ok=%v weak=%v err=%v", ok, weak, err)
	}
}

func TestVerifyHealthy_Unhealthy(t *testing.T) {
	shrinkHealthTimers(t)
	mock := &mockClient{inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
		return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
			State: &types.ContainerState{Running: true, Health: &types.Health{Status: "unhealthy"}}},
			Config: &container.Config{}}, nil
	}}
	ok, _, err := verifyHealthy(context.Background(), mock, testCfg(), proxyHealthTimeout)
	if ok || err == nil {
		t.Fatalf("want unhealthy failure, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyHealthy_Timeout(t *testing.T) {
	shrinkHealthTimers(t)
	mock := &mockClient{inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
		return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
			State: &types.ContainerState{Running: true, Health: &types.Health{Status: "starting"}}},
			Config: &container.Config{}}, nil
	}}
	start := time.Now()
	ok, _, err := verifyHealthy(context.Background(), mock, testCfg(), proxyHealthTimeout)
	if ok || err == nil {
		t.Fatalf("want timeout failure, got ok=%v err=%v", ok, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("verify did not honor the shrunk timeout")
	}
}

func TestVerifyHealthy_NilHealthIsWeak(t *testing.T) {
	shrinkHealthTimers(t)
	mock := &mockClient{inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
		return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
			State: &types.ContainerState{Running: true, Health: nil}},
			Config: &container.Config{}}, nil
	}}
	ok, weak, err := verifyHealthy(context.Background(), mock, testCfg(), proxyHealthTimeout)
	if err != nil || !ok || !weak {
		t.Fatalf("want ok=true weak=true err=nil, got ok=%v weak=%v err=%v", ok, weak, err)
	}
}

func TestVerifyHealthy_FastExitWhenNotRunning(t *testing.T) {
	shrinkHealthTimers(t) // healthStartGrace = 0 -> fail on first observation
	mock := &mockClient{inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
		return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
			State: &types.ContainerState{Running: false}},
			Config: &container.Config{}}, nil
	}}
	start := time.Now()
	ok, _, err := verifyHealthy(context.Background(), mock, testCfg(), proxyHealthTimeout)
	if ok || err == nil {
		t.Fatalf("want fast-exit failure, got ok=%v err=%v", ok, err)
	}
	if time.Since(start) >= proxyHealthTimeout {
		t.Fatalf("expected fast exit well under the timeout")
	}
}

func TestVerifyHealthy_InspectError(t *testing.T) {
	shrinkHealthTimers(t)
	mock := &mockClient{inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
		return types.ContainerJSON{}, errors.New("daemon down")
	}}
	if _, _, err := verifyHealthy(context.Background(), mock, testCfg(), proxyHealthTimeout); err == nil {
		t.Fatal("want inspect error propagated")
	}
}

func TestProxyRecreate_HappyCommitOrderAndStaticIP(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := &mockClient{
		inspectFn: func(_ context.Context, id string) (types.ContainerJSON, error) {
			if id == "ws-proxy-backup" {
				return types.ContainerJSON{}, errdefs.NotFound(errors.New("no backup"))
			}
			// primary: exists+running for the classify inspect.
			return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
				State: &types.ContainerState{Running: true, Health: &types.Health{Status: "healthy"}}},
				Config: &container.Config{}}, nil
		},
		imageInspFn: func(_ context.Context, _ string) (types.ImageInspect, []byte, error) {
			return types.ImageInspect{}, nil, nil
		},
		stopFn: func(_ context.Context, id string, _ container.StopOptions) error {
			seq = append(seq, "stop:"+id)
			return nil
		},
		renameFn: func(_ context.Context, id, nn string) error { seq = append(seq, "rename:"+id+"->"+nn); return nil },
		networkDisconnectFn: func(_ context.Context, _, id string, _ bool) error {
			seq = append(seq, "disconnect:"+id)
			return nil
		},
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, nc *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
			seq = append(seq, "create:"+name)
			ep := nc.EndpointsConfig["ws-proxy"]
			if ep == nil || ep.IPAMConfig == nil || ep.IPAMConfig.IPv4Address != "172.30.0.2" {
				t.Errorf("create did not pin the static IP, got %#v", nc.EndpointsConfig)
			}
			return container.CreateResponse{ID: "new-id"}, nil
		},
		startFn: func(_ context.Context, id string, _ container.StartOptions) error {
			seq = append(seq, "start:"+id)
			return nil
		},
		removeFn: func(_ context.Context, id string, _ container.RemoveOptions) error {
			seq = append(seq, "remove:"+id)
			return nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, false, nil
	})()

	if err := ProxyRecreate(cfg); err != nil {
		t.Fatalf("happy recreate: %v", err)
	}
	want := []string{"stop:ws-proxy", "rename:ws-proxy->ws-proxy-backup", "disconnect:ws-proxy-backup", "create:ws-proxy", "start:new-id", "remove:ws-proxy-backup"}
	assertSeq(t, seq, want)
}

func TestProxyRecreate_PreflightBeforeDestroy(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := &mockClient{
		inspectFn: func(_ context.Context, id string) (types.ContainerJSON, error) {
			if id == "ws-proxy-backup" {
				return types.ContainerJSON{}, errdefs.NotFound(errors.New("no backup"))
			}
			return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
				State: &types.ContainerState{Running: true}}, Config: &container.Config{}}, nil
		},
		imageInspFn: func(_ context.Context, _ string) (types.ImageInspect, []byte, error) {
			return types.ImageInspect{}, nil, errdefs.NotFound(errors.New("no image")) // image MISSING
		},
		stopFn: func(_ context.Context, id string, _ container.StopOptions) error {
			seq = append(seq, "stop")
			return nil
		},
		renameFn:            func(_ context.Context, _, _ string) error { seq = append(seq, "rename"); return nil },
		networkDisconnectFn: func(_ context.Context, _, _ string, _ bool) error { seq = append(seq, "disconnect"); return nil },
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			seq = append(seq, "create")
			return container.CreateResponse{}, nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "ws proxy rebuild") {
		t.Fatalf("want image-missing preflight error, got: %v", err)
	}
	if len(seq) != 0 {
		t.Fatalf("PREFLIGHT must abort before any mutation; got seq=%v", seq)
	}
}

// TestProxyRecreate_ForeignIPAbortsBeforeDestroy is the recreate-path twin of
// the Task-1 cold-path guard (spec §9 T8): a foreign holder of cfg.ProxyIP must
// abort the recreate during PREFLIGHT, before any stop/rename/disconnect/create,
// proving the own/backup exclusion does not suppress a genuine foreign holder.
func TestProxyRecreate_ForeignIPAbortsBeforeDestroy(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
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
		networkInspFn: func(_ context.Context, _ string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{
				Containers: map[string]network.EndpointResource{
					"intruder": {Name: "intruder", IPv4Address: "172.30.0.2/24"},
				},
			}, nil
		},
		stopFn: func(_ context.Context, _ string, _ container.StopOptions) error {
			seq = append(seq, "stop")
			return nil
		},
		renameFn:            func(_ context.Context, _, _ string) error { seq = append(seq, "rename"); return nil },
		networkDisconnectFn: func(_ context.Context, _, _ string, _ bool) error { seq = append(seq, "disconnect"); return nil },
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			seq = append(seq, "create")
			return container.CreateResponse{}, nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "is held by container") {
		t.Fatalf("want foreign-IP abort on the recreate path, got: %v", err)
	}
	if len(seq) != 0 {
		t.Fatalf("foreign-IP must abort before any mutation; seq=%v", seq)
	}
}

func TestProxyRecreate_ColdCreateNoRename(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{}, errdefs.NotFound(errors.New("absent")) // primary AND backup absent
		},
		imageInspFn: func(_ context.Context, _ string) (types.ImageInspect, []byte, error) {
			return types.ImageInspect{}, nil, nil
		},
		renameFn:            func(_ context.Context, _, _ string) error { seq = append(seq, "rename"); return nil },
		networkDisconnectFn: func(_ context.Context, _, _ string, _ bool) error { seq = append(seq, "disconnect"); return nil },
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			seq = append(seq, "create")
			return container.CreateResponse{ID: "x"}, nil
		},
		startFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			seq = append(seq, "start")
			return nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, false, nil
	})()

	if err := ProxyRecreate(cfg); err != nil {
		t.Fatalf("cold create: %v", err)
	}
	for _, s := range seq {
		if s == "rename" || s == "disconnect" {
			t.Fatalf("cold create must not rename/disconnect; seq=%v", seq)
		}
	}
}

// TestProxyRecreate_WeakHealthCommitsWithBackupDropped covers §9 T10 at the
// recreate level: a nil-HEALTHCHECK (weak) new container is a committed success
// and the backup is still dropped.
func TestProxyRecreate_WeakHealthCommitsWithBackupDropped(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
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
		stopFn:              func(_ context.Context, _ string, _ container.StopOptions) error { return nil },
		renameFn:            func(_ context.Context, _, _ string) error { return nil },
		networkDisconnectFn: func(_ context.Context, _, _ string, _ bool) error { return nil },
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "n"}, nil
		},
		startFn: func(_ context.Context, _ string, _ container.StartOptions) error { return nil },
		removeFn: func(_ context.Context, id string, _ container.RemoveOptions) error {
			seq = append(seq, "remove:"+id)
			return nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, true, nil
	})()

	if err := ProxyRecreate(cfg); err != nil {
		t.Fatalf("weak health (no healthcheck) must commit, got: %v", err)
	}
	if indexOf(seq, "remove:ws-proxy-backup") < 0 {
		t.Fatalf("weak-success COMMIT must drop the backup; seq=%v", seq)
	}
}

// TestProxyRecreate_CommitBestEffort_RemoveBackupFails covers §9 T11: a failing
// backup-remove during COMMIT is best-effort and still returns success.
func TestProxyRecreate_CommitBestEffort_RemoveBackupFails(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
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
		stopFn:              func(_ context.Context, _ string, _ container.StopOptions) error { return nil },
		renameFn:            func(_ context.Context, _, _ string) error { return nil },
		networkDisconnectFn: func(_ context.Context, _, _ string, _ bool) error { return nil },
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "n"}, nil
		},
		startFn: func(_ context.Context, _ string, _ container.StartOptions) error { return nil },
		removeFn: func(_ context.Context, id string, _ container.RemoveOptions) error {
			seq = append(seq, "remove:"+id)
			if id == "ws-proxy-backup" {
				return errors.New("cannot remove backup")
			}
			return nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, false, nil
	})()

	if err := ProxyRecreate(cfg); err != nil {
		t.Fatalf("COMMIT must be best-effort: a failed backup-remove still succeeds, got: %v", err)
	}
	if indexOf(seq, "remove:ws-proxy-backup") < 0 {
		t.Fatalf("COMMIT must still attempt the backup remove; seq=%v", seq)
	}
}

func TestProxyRecreate_StaleBackupCaseA_RemovesGarbage(t *testing.T) {
	shrinkHealthTimers(t)
	removed := ""
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			// both primary and backup "exist".
			return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
				State: &types.ContainerState{Running: true}}, Config: &container.Config{}}, nil
		},
		imageInspFn: func(_ context.Context, _ string) (types.ImageInspect, []byte, error) {
			return types.ImageInspect{}, nil, nil
		},
		removeFn: func(_ context.Context, id string, _ container.RemoveOptions) error { removed = id; return nil },
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "n"}, nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, false, nil
	})()

	if err := ProxyRecreate(cfg); err != nil {
		t.Fatalf("case A: %v", err)
	}
	if removed != "ws-proxy-backup" {
		t.Fatalf("case A should force-remove the stale backup first; removed=%q", removed)
	}
}

func TestProxyRecreate_StaleBackupCaseB_RestoresAndReturnsSuccess(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := &mockClient{
		inspectFn: func(_ context.Context, id string) (types.ContainerJSON, error) {
			if id == "ws-proxy-backup" {
				return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: false}}, Config: &container.Config{}}, nil // backup present
			}
			return types.ContainerJSON{}, errdefs.NotFound(errors.New("primary gone")) // primary absent
		},
		networkConnectFn: func(_ context.Context, _, id string, c *network.EndpointSettings) error {
			seq = append(seq, "connect:"+id)
			if c.IPAMConfig == nil || c.IPAMConfig.IPv4Address != "172.30.0.2" {
				t.Errorf("restore must re-reserve the static IP")
			}
			return nil
		},
		startFn: func(_ context.Context, id string, _ container.StartOptions) error {
			seq = append(seq, "start:"+id)
			return nil
		},
		renameFn: func(_ context.Context, id, nn string) error { seq = append(seq, "rename:"+id+"->"+nn); return nil },
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			t.Error("case B must NOT re-run the swap (DR-3)")
			return container.CreateResponse{}, nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)

	if err := ProxyRecreate(cfg); err != nil {
		t.Fatalf("case B should return success after restore: %v", err)
	}
	assertSeq(t, seq, []string{"connect:ws-proxy-backup", "start:ws-proxy-backup", "rename:ws-proxy-backup->ws-proxy"})
}

// recreateForwardMock: classify shows primary present + no backup, image present,
// xray config present; forward swap (stop/rename/disconnect) all succeed.
func recreateForwardMock(t *testing.T, seq *[]string) *mockClient {
	t.Helper()
	return &mockClient{
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
		stopFn: func(_ context.Context, id string, _ container.StopOptions) error {
			*seq = append(*seq, "stop:"+id)
			return nil
		},
		renameFn:            func(_ context.Context, id, nn string) error { *seq = append(*seq, "rename:"+id+"->"+nn); return nil },
		networkDisconnectFn: func(_ context.Context, _, id string, _ bool) error { *seq = append(*seq, "disconnect:"+id); return nil },
	}
}

// REGRESSION GUARD: already green against Task-3's basic rollback (which restores
// in §2.3 order); kept here as the canonical clean create-fails rollback trace.
func TestProxyRecreate_CreateFails_RollsBackInOrder(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := recreateForwardMock(t, &seq)
	mock.createFn = func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		return container.CreateResponse{}, errors.New("daemon refused create")
	}
	mock.removeFn = func(_ context.Context, id string, _ container.RemoveOptions) error {
		seq = append(seq, "remove:"+id)
		return nil
	}
	mock.networkConnectFn = func(_ context.Context, _, id string, c *network.EndpointSettings) error {
		seq = append(seq, "connect:"+id)
		if c.IPAMConfig == nil || c.IPAMConfig.IPv4Address != "172.30.0.2" {
			t.Errorf("rollback must re-reserve the static IP")
		}
		return nil
	}
	mock.startFn = func(_ context.Context, id string, _ container.StartOptions) error {
		if id == "ws-proxy-backup" {
			seq = append(seq, "start-backup")
		}
		return nil
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	// restored-backup re-verify (Task-4 hardened rollback step 6) is healthy.
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, false, nil
	})()

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("want rolled-back error, got: %v", err)
	}
	// broken NEW removed before reconnecting the backup to the same IP.
	idxRemove, idxConnect := indexOf(seq, "remove:ws-proxy"), indexOf(seq, "connect:ws-proxy-backup")
	if idxRemove < 0 || idxConnect < 0 || idxRemove > idxConnect {
		t.Fatalf("rollback must remove broken NEW before reconnecting backup; seq=%v", seq)
	}
	if indexOf(seq, "rename:ws-proxy-backup->ws-proxy") < 0 {
		t.Fatalf("rollback must rename backup->primary; seq=%v", seq)
	}
}

// DRIVER (§9 T5): a START failure (NEW created but not started) must roll back,
// removing the created-but-dead NEW before reconnecting the backup. Folded
// create+start means this exercises a distinct error path from create-fails.
func TestProxyRecreate_StartNewFails_RollsBack(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := recreateForwardMock(t, &seq)
	mock.createFn = func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		seq = append(seq, "create:new")
		return container.CreateResponse{ID: "new-id"}, nil
	}
	mock.startFn = func(_ context.Context, id string, _ container.StartOptions) error {
		if id == "new-id" {
			return errors.New("new container will not start")
		}
		if id == "ws-proxy-backup" {
			seq = append(seq, "start-backup")
		}
		return nil
	}
	mock.removeFn = func(_ context.Context, id string, _ container.RemoveOptions) error {
		seq = append(seq, "remove:"+id)
		return nil
	}
	mock.networkConnectFn = func(_ context.Context, _, id string, c *network.EndpointSettings) error {
		seq = append(seq, "connect:"+id)
		if c.IPAMConfig == nil || c.IPAMConfig.IPv4Address != "172.30.0.2" {
			t.Errorf("rollback must re-reserve the static IP")
		}
		return nil
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return true, false, nil
	})()

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("want rolled-back on start failure, got: %v", err)
	}
	idxRemove, idxConnect := indexOf(seq, "remove:ws-proxy"), indexOf(seq, "connect:ws-proxy-backup")
	if idxRemove < 0 || idxConnect < 0 || idxRemove > idxConnect {
		t.Fatalf("rollback must remove the created-but-dead NEW before reconnecting backup; seq=%v", seq)
	}
}

// DRIVER (§9 T10 not-running branch): a NEW that exits before becoming healthy
// must roll back to the backup.
func TestProxyRecreate_NewExited_RollsBack(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := recreateForwardMock(t, &seq)
	mock.createFn = func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		return container.CreateResponse{ID: "n"}, nil
	}
	mock.removeFn = func(_ context.Context, id string, _ container.RemoveOptions) error {
		seq = append(seq, "remove:"+id)
		return nil
	}
	mock.networkConnectFn = func(_ context.Context, _, id string, _ *network.EndpointSettings) error {
		seq = append(seq, "connect:"+id)
		return nil
	}
	mock.startFn = func(_ context.Context, id string, _ container.StartOptions) error {
		if id == "ws-proxy-backup" {
			seq = append(seq, "start-backup")
		}
		return nil
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	// NEW verify reports an exited container; restored-backup re-verify is healthy.
	calls := 0
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		calls++
		if calls == 1 {
			return false, false, errors.New("proxy container exited before becoming healthy")
		}
		return true, false, nil
	})()

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("an exited NEW must roll back, got: %v", err)
	}
	if indexOf(seq, "rename:ws-proxy-backup->ws-proxy") < 0 {
		t.Fatalf("rollback must restore the backup; seq=%v", seq)
	}
}

// DRIVER: an unhealthy NEW rolls back to a HEALTHY backup -- the clean-rollback
// path (distinct from the RV path below). A call-counting verify seam makes the
// NEW verify unhealthy while the restored-backup re-verify is healthy.
func TestProxyRecreate_Unhealthy_RollsBackToHealthyBackup(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := recreateForwardMock(t, &seq)
	mock.createFn = func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		return container.CreateResponse{ID: "n"}, nil
	}
	mock.removeFn = func(_ context.Context, id string, _ container.RemoveOptions) error {
		seq = append(seq, "remove:"+id)
		return nil
	}
	mock.networkConnectFn = func(_ context.Context, _, id string, _ *network.EndpointSettings) error {
		seq = append(seq, "connect:"+id)
		return nil
	}
	mock.startFn = func(_ context.Context, id string, _ container.StartOptions) error {
		if id == "ws-proxy-backup" {
			seq = append(seq, "start-backup")
		}
		return nil
	}
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

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "rolled back") || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("want clean rollback to a healthy backup, got: %v", err)
	}
	if strings.Contains(err.Error(), "restored proxy is also unhealthy") {
		t.Fatalf("restored backup was healthy; this must be the clean rollback path, got: %v", err)
	}
	if indexOf(seq, "rename:ws-proxy-backup->ws-proxy") < 0 {
		t.Fatalf("rollback must restore the backup; seq=%v", seq)
	}
}

// DRIVER: any rollback sub-step fault yields CRITICAL naming only the remaining
// manual recovery commands (and NOT a command that would now fail).
func TestProxyRecreate_DoubleFault_CriticalNamesState(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := recreateForwardMock(t, &seq)
	mock.createFn = func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		return container.CreateResponse{}, errors.New("create boom")
	}
	mock.removeFn = func(_ context.Context, _ string, _ container.RemoveOptions) error { return nil }
	mock.networkConnectFn = func(_ context.Context, _, _ string, _ *network.EndpointSettings) error { return nil }
	mock.startFn = func(_ context.Context, id string, _ container.StartOptions) error {
		if id == "ws-proxy-backup" {
			return errors.New("backup will not start") // rollback sub-step fault
		}
		return nil
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "CRITICAL") {
		t.Fatalf("want CRITICAL double-fault, got: %v", err)
	}
	if !strings.Contains(err.Error(), "docker start ws-proxy-backup") {
		t.Fatalf("CRITICAL must emit the remaining manual recovery for the un-started backup; got: %v", err)
	}
	if strings.Contains(err.Error(), "network connect --ip") {
		t.Fatalf("must NOT print 'network connect --ip' when the backup is already connected; got: %v", err)
	}
}

// DRIVER: restore succeeds but the restored backup is itself unhealthy (shared
// broken on-disk config) -> honest RV routing to 'ws proxy doctor'.
func TestProxyRecreate_RestoredBackupAlsoUnhealthy_HonestRV(t *testing.T) {
	shrinkHealthTimers(t)
	var seq []string
	mock := recreateForwardMock(t, &seq)
	mock.createFn = func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		return container.CreateResponse{}, errors.New("create boom")
	}
	mock.removeFn = func(_ context.Context, _ string, _ container.RemoveOptions) error { return nil }
	mock.networkConnectFn = func(_ context.Context, _, _ string, _ *network.EndpointSettings) error { return nil }
	mock.startFn = func(_ context.Context, _ string, _ container.StartOptions) error { return nil }
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	// both NEW verify and restored-backup re-verify are unhealthy.
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		return false, false, errors.New("proxy container is unhealthy")
	})()

	err := ProxyRecreate(cfg)
	if err == nil || !strings.Contains(err.Error(), "restored proxy is also unhealthy") {
		t.Fatalf("want honest RV message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ws proxy doctor") {
		t.Fatalf("RV must route to 'ws proxy doctor'; got: %v", err)
	}
}

func TestProxyRecreate_ContextBudget_NoFalseRollbackVerifyOutlivesMutateCtx(t *testing.T) {
	// Regression for the HIGH timeout bug (spec §5): a verify that consumes
	// wall-clock beyond the mutating-phase deadline must NOT expire the COMMIT
	// ctx. Shrink the mutating budget; make verify sleep past it; assert COMMIT
	// removes the backup under a NON-expired ctx, and recreate returns nil.
	origMutate := recreateMutateTimeout
	recreateMutateTimeout = 5 * time.Millisecond
	t.Cleanup(func() { recreateMutateTimeout = origMutate })

	var committedUnderExpiredCtx bool
	var removedBackup bool
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
		createFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "n"}, nil
		},
		removeFn: func(ctx context.Context, id string, _ container.RemoveOptions) error {
			if id == "ws-proxy-backup" {
				removedBackup = true
				if ctx.Err() != nil {
					committedUnderExpiredCtx = true
				}
			}
			return nil
		},
	}
	defer withMock(mock)()
	cfg := testCfg()
	cfg.XrayConfig = writeTempXrayConfig(t)
	defer swapVerify(func(context.Context, DockerClient, config.Config, time.Duration) (bool, bool, error) {
		time.Sleep(40 * time.Millisecond) // outlives the 5ms mutating budget
		return true, false, nil
	})()

	if err := ProxyRecreate(cfg); err != nil {
		t.Fatalf("verify outliving the mutate ctx must NOT cause a false rollback; got: %v", err)
	}
	if !removedBackup {
		t.Fatal("COMMIT must remove the backup on success")
	}
	if committedUnderExpiredCtx {
		t.Fatal("COMMIT ran under an expired (shared) ctx -- phases are not isolated")
	}
}

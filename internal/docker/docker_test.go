package docker

import (
	"context"
	"errors"
	"io"
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

// mockClient implements DockerClient for testing.
type mockClient struct {
	inspectFn       func(ctx context.Context, id string) (types.ContainerJSON, error)
	createFn        func(ctx context.Context, cfg *container.Config, host *container.HostConfig, net *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error)
	startFn         func(ctx context.Context, id string, opts container.StartOptions) error
	stopFn          func(ctx context.Context, id string, opts container.StopOptions) error
	removeFn        func(ctx context.Context, id string, opts container.RemoveOptions) error
	logsFn          func(ctx context.Context, id string, opts container.LogsOptions) (io.ReadCloser, error)
	networkInspFn   func(ctx context.Context, id string, opts network.InspectOptions) (network.Inspect, error)
	networkCreateFn func(ctx context.Context, name string, opts network.CreateOptions) (network.CreateResponse, error)
	imageInspFn     func(ctx context.Context, id string) (types.ImageInspect, []byte, error)
	pingFn          func(ctx context.Context) (types.Ping, error)
}

func (m *mockClient) ContainerInspect(ctx context.Context, id string) (types.ContainerJSON, error) {
	if m.inspectFn != nil {
		return m.inspectFn(ctx, id)
	}
	return types.ContainerJSON{}, errdefs.NotFound(errors.New("not found"))
}

func (m *mockClient) ContainerCreate(ctx context.Context, cfg *container.Config, host *container.HostConfig, net *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
	if m.createFn != nil {
		return m.createFn(ctx, cfg, host, net, platform, name)
	}
	return container.CreateResponse{ID: "test-id"}, nil
}

func (m *mockClient) ContainerStart(ctx context.Context, id string, opts container.StartOptions) error {
	if m.startFn != nil {
		return m.startFn(ctx, id, opts)
	}
	return nil
}

func (m *mockClient) ContainerStop(ctx context.Context, id string, opts container.StopOptions) error {
	if m.stopFn != nil {
		return m.stopFn(ctx, id, opts)
	}
	return nil
}

func (m *mockClient) ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error {
	if m.removeFn != nil {
		return m.removeFn(ctx, id, opts)
	}
	return nil
}

func (m *mockClient) ContainerLogs(ctx context.Context, id string, opts container.LogsOptions) (io.ReadCloser, error) {
	if m.logsFn != nil {
		return m.logsFn(ctx, id, opts)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockClient) NetworkInspect(ctx context.Context, id string, opts network.InspectOptions) (network.Inspect, error) {
	if m.networkInspFn != nil {
		return m.networkInspFn(ctx, id, opts)
	}
	return network.Inspect{}, nil
}

func (m *mockClient) NetworkCreate(ctx context.Context, name string, opts network.CreateOptions) (network.CreateResponse, error) {
	if m.networkCreateFn != nil {
		return m.networkCreateFn(ctx, name, opts)
	}
	return network.CreateResponse{}, nil
}

func (m *mockClient) ImageInspectWithRaw(ctx context.Context, id string) (types.ImageInspect, []byte, error) {
	if m.imageInspFn != nil {
		return m.imageInspFn(ctx, id)
	}
	return types.ImageInspect{}, nil, errdefs.NotFound(errors.New("not found"))
}

func (m *mockClient) Ping(ctx context.Context) (types.Ping, error) {
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return types.Ping{}, nil
}

func (m *mockClient) Close() error { return nil }

func testCfg() config.Config {
	return config.Config{
		ProxyContainer: "ws-proxy",
		ProxyImage:     "ws-proxy:latest",
		ProxyNetwork:   "ws-proxy",
		ProxySubnet:    "172.30.0.0/24",
		ProxyIP:        "172.30.0.2",
		XrayConfig:     "/tmp/test-xray-config.json",
	}
}

func withMock(mock *mockClient) func() {
	orig := newClientFunc
	newClientFunc = func() (DockerClient, error) { return mock, nil }
	return func() { newClientFunc = orig }
}

// --- ProxyStatus tests ---

func TestProxyStatus_Running(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running:   true,
						StartedAt: "2025-01-01T00:00:00Z",
					},
				},
				Config: &container.Config{Image: "ws-proxy:latest"},
			}, nil
		},
	}
	defer withMock(mock)()

	st, err := ProxyStatus(testCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !st.Running {
		t.Error("expected Running=true")
	}
	if st.Image != "ws-proxy:latest" {
		t.Errorf("expected image ws-proxy:latest, got %s", st.Image)
	}
}

func TestProxyStatus_Stopped(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: false},
				},
				Config: &container.Config{Image: "ws-proxy:latest"},
			}, nil
		},
	}
	defer withMock(mock)()

	st, err := ProxyStatus(testCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Running {
		t.Error("expected Running=false")
	}
}

func TestProxyStatus_NotFound(t *testing.T) {
	mock := &mockClient{} // default inspectFn returns not-found
	defer withMock(mock)()

	st, err := ProxyStatus(testCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Running {
		t.Error("expected Running=false for not-found container")
	}
}

func TestProxyStatus_DockerError(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{}, errors.New("daemon unreachable")
		},
	}
	defer withMock(mock)()

	_, err := ProxyStatus(testCfg())
	if err == nil {
		t.Fatal("expected error for Docker daemon failure")
	}
	if !strings.Contains(err.Error(), "daemon unreachable") {
		t.Errorf("expected daemon error, got: %v", err)
	}
}

// --- ProxyDown tests ---

func TestProxyDown_Running(t *testing.T) {
	stopped := false
	mock := &mockClient{
		stopFn: func(_ context.Context, _ string, _ container.StopOptions) error {
			stopped = true
			return nil
		},
	}
	defer withMock(mock)()

	if err := ProxyDown(testCfg()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stopped {
		t.Error("expected stop to be called")
	}
}

func TestProxyDown_NotFound(t *testing.T) {
	mock := &mockClient{
		stopFn: func(_ context.Context, _ string, _ container.StopOptions) error {
			return errdefs.NotFound(errors.New("not found"))
		},
	}
	defer withMock(mock)()

	if err := ProxyDown(testCfg()); err != nil {
		t.Fatalf("expected nil for not-found, got: %v", err)
	}
}

// --- ProxyCheck tests ---

func TestProxyCheck_AllPass(t *testing.T) {
	cfg := testCfg()

	mock := &mockClient{
		pingFn: func(_ context.Context) (types.Ping, error) {
			return types.Ping{}, nil
		},
		imageInspFn: func(_ context.Context, _ string) (types.ImageInspect, []byte, error) {
			return types.ImageInspect{}, nil, nil
		},
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	results := ProxyCheck(cfg)
	// Docker running should pass
	if !results[0].Passed {
		t.Error("expected Docker running check to pass")
	}
	// Image built should pass
	if !results[2].Passed {
		t.Error("expected image check to pass")
	}
	// Container running should pass
	if !results[3].Passed {
		t.Error("expected container running check to pass")
	}
}

func TestProxyCheck_NoDaemon(t *testing.T) {
	mock := &mockClient{
		pingFn: func(_ context.Context) (types.Ping, error) {
			return types.Ping{}, errors.New("connection refused")
		},
	}
	defer withMock(mock)()

	results := ProxyCheck(testCfg())
	for _, r := range results {
		if r.Passed {
			t.Errorf("expected check %q to fail when daemon is down", r.Name)
		}
	}
}

// --- ProxyConnectedContainers tests ---

func TestProxyConnectedContainers(t *testing.T) {
	mock := &mockClient{
		networkInspFn: func(_ context.Context, _ string, _ network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{
				Containers: map[string]network.EndpointResource{
					"abc": {Name: "ws-proxy"},
					"def": {Name: "my-workspace"},
					"ghi": {Name: "another-ws"},
				},
			}, nil
		},
	}
	defer withMock(mock)()

	names, err := ProxyConnectedContainers(testCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should exclude ws-proxy itself.
	for _, n := range names {
		if n == "ws-proxy" {
			t.Error("should not include proxy container itself")
		}
	}
	if len(names) != 2 {
		t.Errorf("expected 2 connected containers, got %d", len(names))
	}
}

// --- WaitForHealth tests ---

func TestWaitForHealth_Healthy(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Health: &types.Health{Status: "healthy"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	if err := WaitForHealth(testCfg(), 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForHealth_NoHealthCheck(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Health: nil},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	if err := WaitForHealth(testCfg(), 5*time.Second); err != nil {
		t.Fatalf("expected nil for container without health check, got: %v", err)
	}
}

func TestWaitForHealth_Unhealthy(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Health: &types.Health{Status: "unhealthy"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	err := WaitForHealth(testCfg(), 5*time.Second)
	if err == nil {
		t.Fatal("expected error for unhealthy container")
	}
	if !strings.Contains(err.Error(), "unhealthy") {
		t.Errorf("expected unhealthy error, got: %v", err)
	}
}

// --- ProxyExec tests ---

func TestProxyExecSignature(t *testing.T) {
	cfg := config.Config{ProxyContainer: "this-container-does-not-exist-xx22-test"}
	_, err := ProxyExec(cfg, "echo", "hello")
	if err == nil {
		return // happy coincidence; not a helper failure
	}
	if !strings.Contains(err.Error(), "docker exec this-container-does-not-exist-xx22-test") {
		t.Fatalf("error %q missing `docker exec <container>` prefix", err.Error())
	}
	if !strings.Contains(err.Error(), "[echo hello]") {
		t.Fatalf("error %q missing composed args", err.Error())
	}
}

// --- BindMountIsWholeDir tests ---

func TestBindMountIsWholeDir_DetectsLegacySingleFile(t *testing.T) {
	// Composition-only: signature exists and accepts the expected Config.
	// Live HostConfig inspection is for integration tests (Plan 22-06).
	cfg := config.Config{
		ProxyContainer: "this-container-does-not-exist-xx22-bind-test",
		XrayConfig:     "/home/test/.config/xray/config.json",
	}
	_, err := BindMountIsWholeDir(cfg)
	if err == nil {
		t.Skip("docker may be available and a container of that name exists; not a test failure")
	}
	if !strings.Contains(err.Error(), "inspect this-container-does-not-exist-xx22-bind-test") {
		t.Errorf("error does not wrap inspect target: %v", err)
	}
}

// TestBindMountIsWholeDir_WholeDirBind verifies the helper returns true when
// the running container's HostConfig.Binds carries the new whole-directory
// shape `<host-dir>:/etc/xray:ro`.
func TestBindMountIsWholeDir_WholeDirBind(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
					HostConfig: &container.HostConfig{
						Binds: []string{"/home/test/.config/xray:/etc/xray:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	cfg := config.Config{
		ProxyContainer: "dev-proxy",
		XrayConfig:     "/home/test/.config/xray/config.json",
	}
	ok, err := BindMountIsWholeDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected whole-dir bind to be detected as true")
	}
}

// TestBindMountIsWholeDir_LegacySingleFile verifies the helper returns false
// when the running container's HostConfig.Binds carries the legacy
// single-file shape `<host-file>:/etc/xray/config.json:ro`.
func TestBindMountIsWholeDir_LegacySingleFile(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
					HostConfig: &container.HostConfig{
						Binds: []string{"/home/test/.config/xray/config.json:/etc/xray/config.json:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	cfg := config.Config{
		ProxyContainer: "dev-proxy",
		XrayConfig:     "/home/test/.config/xray/config.json",
	}
	ok, err := BindMountIsWholeDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected legacy single-file bind to be detected as false")
	}
}

// TestBindMountIsWholeDir_TrailingSlash verifies the helper returns true when
// the running container's HostConfig.Binds carries the docker-normalized form
// with a trailing slash on the container path, i.e. `<host-dir>:/etc/xray/:ro`.
// This is the form ProxyUp actually writes (docker.go line ~135). The 2026-05-13
// prod incident surfaced a regression where the prefix-based comparator failed
// to recognize this form and falsely reported a legacy single-file bind.
func TestBindMountIsWholeDir_TrailingSlash(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
					HostConfig: &container.HostConfig{
						// docker-normalized form: ProxyUp writes "/etc/xray/"
						// with a trailing slash on the container path.
						Binds: []string{"/home/test/.config/xray:/etc/xray/:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	cfg := config.Config{
		ProxyContainer: "dev-proxy",
		XrayConfig:     "/home/test/.config/xray/config.json",
	}
	ok, err := BindMountIsWholeDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected trailing-slash whole-dir bind to be detected as true (prod hotfix regression guard)")
	}
}

// TestBindMountIsWholeDir_NoSlash verifies the helper returns true when the
// running container's HostConfig.Binds carries the whole-dir form WITHOUT a
// trailing slash on the container path, i.e. `<host-dir>:/etc/xray:ro`. Both
// forms must be tolerated because docker may normalize either way depending on
// API version. Symmetrical companion of TestBindMountIsWholeDir_TrailingSlash.
func TestBindMountIsWholeDir_NoSlash(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
					HostConfig: &container.HostConfig{
						Binds: []string{"/home/test/.config/xray:/etc/xray:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	cfg := config.Config{
		ProxyContainer: "dev-proxy",
		XrayConfig:     "/home/test/.config/xray/config.json",
	}
	ok, err := BindMountIsWholeDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected no-slash whole-dir bind to be detected as true")
	}
}

// TestBindMountIsWholeDir_SingleFile is the named-for-symmetry companion of
// TestBindMountIsWholeDir_LegacySingleFile. Kept as a separate test so the
// 2026-05-13 hotfix spec's three-test list ({TrailingSlash, NoSlash, SingleFile})
// is satisfied verbatim and regressions of either shape land on a clearly
// named failure.
func TestBindMountIsWholeDir_SingleFile(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
					HostConfig: &container.HostConfig{
						Binds: []string{"/home/test/.config/xray/config.json:/etc/xray/config.json:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	cfg := config.Config{
		ProxyContainer: "dev-proxy",
		XrayConfig:     "/home/test/.config/xray/config.json",
	}
	ok, err := BindMountIsWholeDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected single-file bind to be detected as false")
	}
}

// --- VerifyProxyReadyForReload tests ---

func TestVerifyProxyReadyForReload_HappyPath(t *testing.T) {
	cfg := testCfg()
	wholeDirHost := filepath.Dir(cfg.XrayConfig)
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Status:  "running",
					},
					HostConfig: &container.HostConfig{
						Binds: []string{wholeDirHost + ":/etc/xray/:ro"},
					},
				},
				Config: &container.Config{Image: "ws-proxy:latest"},
			}, nil
		},
	}
	defer withMock(mock)()

	if err := VerifyProxyReadyForReload(cfg); err != nil {
		t.Fatalf("expected nil error for happy path; got %v", err)
	}
}

func TestVerifyProxyReadyForReload_ContainerNotFound(t *testing.T) {
	mock := &mockClient{} // default returns errdefs.NotFound
	defer withMock(mock)()

	err := VerifyProxyReadyForReload(testCfg())
	if err == nil {
		t.Fatal("expected error for not-found container")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to contain 'not found'; got %v", err)
	}
	if !strings.Contains(err.Error(), "ws proxy up") {
		t.Errorf("expected recovery hint 'ws proxy up'; got %v", err)
	}
}

func TestVerifyProxyReadyForReload_ContainerStopped(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: false,
						Status:  "exited",
					},
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	err := VerifyProxyReadyForReload(testCfg())
	if err == nil {
		t.Fatal("expected error for stopped container")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("expected 'not running'; got %v", err)
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("expected status 'exited' in error; got %v", err)
	}
}

func TestVerifyProxyReadyForReload_LegacyBind(t *testing.T) {
	cfg := testCfg()
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{
						Running: true,
						Status:  "running",
					},
					HostConfig: &container.HostConfig{
						Binds: []string{cfg.XrayConfig + ":/etc/xray/config.json:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	defer withMock(mock)()

	err := VerifyProxyReadyForReload(cfg)
	if err == nil {
		t.Fatal("expected error for legacy single-file bind")
	}
	if !strings.Contains(err.Error(), "legacy single-file bind") {
		t.Errorf("expected 'legacy single-file bind'; got %v", err)
	}
	if !strings.Contains(err.Error(), "ws proxy rebuild") {
		t.Errorf("expected recovery hint 'ws proxy rebuild'; got %v", err)
	}
}

func TestVerifyProxyReadyForReload_InspectError(t *testing.T) {
	mock := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{}, errors.New("daemon unreachable")
		},
	}
	defer withMock(mock)()

	err := VerifyProxyReadyForReload(testCfg())
	if err == nil {
		t.Fatal("expected error for inspect failure")
	}
	if !strings.Contains(err.Error(), "daemon unreachable") {
		t.Errorf("expected wrapped daemon error; got %v", err)
	}
}

// --- BindMountIsWholeDir regression ---

func TestBindMountIsWholeDir_StillWorks(t *testing.T) {
	cfg := testCfg()
	wholeDirHost := filepath.Dir(cfg.XrayConfig)

	// Whole-dir case -> true.
	mockWhole := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
					HostConfig: &container.HostConfig{
						Binds: []string{wholeDirHost + ":/etc/xray/:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	restore := withMock(mockWhole)
	ok, err := BindMountIsWholeDir(cfg)
	restore()
	if err != nil {
		t.Fatalf("whole-dir case: unexpected error %v", err)
	}
	if !ok {
		t.Error("whole-dir case: expected ok=true")
	}

	// Single-file case -> false.
	mockLegacy := &mockClient{
		inspectFn: func(_ context.Context, _ string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					State: &types.ContainerState{Running: true},
					HostConfig: &container.HostConfig{
						Binds: []string{cfg.XrayConfig + ":/etc/xray/config.json:ro"},
					},
				},
				Config: &container.Config{},
			}, nil
		},
	}
	restore = withMock(mockLegacy)
	ok, err = BindMountIsWholeDir(cfg)
	restore()
	if err != nil {
		t.Fatalf("legacy case: unexpected error %v", err)
	}
	if ok {
		t.Error("legacy case: expected ok=false")
	}
}

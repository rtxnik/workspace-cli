package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rtxnik/ws/internal/config"
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

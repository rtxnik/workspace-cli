package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
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

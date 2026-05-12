//go:build integration

package xray

import (
	"os/exec"
	"testing"
)

func TestIntegration_Cycle(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if exec.Command("docker", "ps", "-q", "-f", "name=dev-proxy").Run() != nil {
		t.Skip("dev-proxy not running")
	}
	t.Skip("pending Plan 22-06 integration test")
}

func TestExistingStateDiscovery(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	t.Skip("pending Plan 22-06 existing-state discovery")
}

func TestProfileLifecycleE2E(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	t.Skip("pending Plan 22-06 E2E lifecycle")
}

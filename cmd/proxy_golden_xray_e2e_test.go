//go:build docker_e2e

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireProxyImage returns the proxy image to validate against. Under CI an
// absent image is FATAL — a CI pass must never come from a skip. Locally an
// absent image is a graceful skip.
func requireProxyImage(t *testing.T) string {
	t.Helper()
	img := os.Getenv("WS_PROXY_IMAGE")
	if img == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("WS_PROXY_IMAGE empty under CI — the H6 golden xray-test gate must not be skipped")
		}
		t.Skip("WS_PROXY_IMAGE unset — set it to a built proxy image to run the H6 gate")
	}
	return img
}

// xrayTestConfig runs `xray run -test` over one config file using the image's
// own pinned xray-core (version-correct by construction).
func xrayTestConfig(image, hostPath string) (string, error) {
	dir := filepath.Dir(hostPath)
	base := filepath.Base(hostPath)
	// The image's xray carries file capability cap_net_admin+ep (set via setcap
	// in the recipe Dockerfile). Exec'ing a binary whose effective file-cap is
	// outside the container's capability bounding set fails with EPERM
	// ("operation not permitted"), so NET_ADMIN must be added even though
	// `xray -test` only parses the config and never uses the capability.
	cmd := exec.Command("docker", "run", "--rm",
		"--cap-add", "NET_ADMIN",
		"-v", dir+":/cfg:ro",
		"--entrypoint", "/usr/local/bin/xray",
		image, "run", "-test", "-c", "/cfg/"+base)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestXrayValidatesConfigs is the H6 gate: every committed golden and every
// generated VLESS-matrix config must pass `xray -test` under the image's
// pinned xray-core. A deliberately broken config must be rejected (proves the
// harness can fail).
func TestXrayValidatesConfigs(t *testing.T) {
	image := requireProxyImage(t)

	cases := collectXrayConfigs(t) // from proxy_golden_configs_test.go
	dir := t.TempDir()
	for i, c := range cases {
		path := filepath.Join(dir, fmt.Sprintf("cfg-%02d.json", i))
		if err := os.WriteFile(path, c.json, 0o600); err != nil {
			t.Fatalf("%s: write temp config: %v", c.name, err)
		}
		if out, err := xrayTestConfig(image, path); err != nil {
			t.Errorf("%s: xray -test FAILED: %v\n%s", c.name, err, out)
		} else {
			t.Logf("%s: xray -test OK", c.name)
		}
	}

	// Negative self-test: a broken config MUST be rejected.
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte(`{"outbounds":[{"protocol":"this-protocol-does-not-exist"}]}`), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	if _, err := xrayTestConfig(image, broken); err == nil {
		t.Fatal("negative self-test: xray -test accepted a broken config — the gate cannot detect failures")
	}
}

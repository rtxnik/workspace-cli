//go:build docker_e2e

package cmd

// TestProxyE2E is an operator-gate integration test that requires:
//   - Docker available and reachable on the host
//   - A "primary" hysteria2 profile initialised via `ws proxy init <hy2-uri>`
//     (the test skips, not fails, when either prerequisite is absent)
//
// Approach: build+exec the `ws` binary (most faithful to operator reality).
// In-process cobra calls are fragile here because `ws proxy doctor` and
// `ws proxy test` call os.Exit on failure — exiting the test process would
// mark the entire suite failed. exec.Command isolates each CLI invocation in
// its own process and lets the harness inspect exit codes cleanly.
//
// Run: make test-e2e
// or:  go test -tags docker_e2e ./cmd/ -run TestProxyE2E -v -timeout 5m

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestProxyE2E builds the ws binary, ups the proxy, and runs doctor + test.
// It asserts that the tunnel is active (Tunneled == true in `ws proxy test --json`
// and OK == true in `ws proxy doctor --json`).
func TestProxyE2E(t *testing.T) {
	// --- prerequisite: docker reachable ---
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker not available (%v) — skipping e2e", err)
	}

	// --- prerequisite: primary profile present (XRAY_CONFIG symlink -> profiles/primary.json) ---
	xrayCfg := os.Getenv("XRAY_CONFIG")
	if xrayCfg == "" {
		home, _ := os.UserHomeDir()
		xrayCfg = filepath.Join(home, ".config", "xray", "config.json")
	}
	xrayProfilesDir := os.Getenv("XRAY_PROFILES_DIR")
	if xrayProfilesDir == "" {
		xrayProfilesDir = filepath.Join(filepath.Dir(xrayCfg), "profiles")
	}
	primaryPath := filepath.Join(xrayProfilesDir, "primary.json")
	if _, err := os.Stat(primaryPath); os.IsNotExist(err) {
		t.Skipf("primary hysteria2 profile not found at %s — skipping e2e; init with: ws proxy init <hy2-uri>", primaryPath)
	}

	// --- build the ws binary into a temp dir ---
	tmpDir := t.TempDir()
	wsBin := filepath.Join(tmpDir, "ws")
	if runtime.GOOS == "windows" {
		wsBin += ".exe"
	}
	// Find module root (two levels up from cmd/): walk to directory containing go.mod.
	// We rely on the test binary being built from the module root, so we use
	// runtime.Caller to locate the source tree.
	_, testFile, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Dir(filepath.Dir(testFile)) // cmd/ -> module root

	buildCmd := exec.Command("go", "build", "-o", wsBin, ".")
	buildCmd.Dir = moduleRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ws binary: %v\n%s", err, out)
	}

	// ws is the helper: runs the binary with the given args and returns
	// (combined output, exit error).
	ws := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(wsBin, args...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// --- step 1: ws proxy up (start or ensure running) ---
	// Intentionally leaves the container running after the test — this is an
	// operator gate that requires a live environment; teardown is the operator's
	// responsibility.
	t.Log("step 1: ws proxy up")
	if out, err := ws("proxy", "up"); err != nil {
		t.Fatalf("ws proxy up: %v\n%s", err, out)
	}

	// --- step 2: ws proxy doctor --json (all checks must pass) ---
	t.Log("step 2: ws proxy doctor --json")
	doctorOut, doctorErr := ws("proxy", "doctor", "--json")
	if doctorErr != nil {
		// Non-zero exit means at least one hard check failed.
		t.Fatalf("ws proxy doctor failed (exit %v):\n%s", doctorErr, doctorOut)
	}

	var doctorResult struct {
		OK       bool `json:"ok"`
		FailedAt int  `json:"failedAt"`
		Checks   []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(doctorOut), &doctorResult); err != nil {
		t.Fatalf("parse doctor JSON: %v\nraw: %s", err, doctorOut)
	}
	if !doctorResult.OK {
		t.Fatalf("ws proxy doctor: not all checks passed (failedAt=%d)\n%s", doctorResult.FailedAt, doctorOut)
	}
	t.Logf("doctor: %d checks, all OK", len(doctorResult.Checks))

	// --- step 3: ws proxy test --json (tunnel must be active) ---
	t.Log("step 3: ws proxy test --json")
	testOut, testErr := ws("proxy", "test", "--json")
	if testErr != nil {
		t.Fatalf("ws proxy test failed (exit %v):\n%s", testErr, testOut)
	}

	var probeResult struct {
		DirectIP  string `json:"directIP"`
		ProxiedIP string `json:"proxiedIP"`
		Tunneled  bool   `json:"tunneled"`
	}
	if err := json.Unmarshal([]byte(testOut), &probeResult); err != nil {
		t.Fatalf("parse test JSON: %v\nraw: %s", err, testOut)
	}
	if !probeResult.Tunneled {
		t.Fatalf("tunnel NOT active: directIP=%s proxiedIP=%s — exit IPs are identical; check the proxy config",
			probeResult.DirectIP, probeResult.ProxiedIP)
	}
	t.Logf("tunnel active: directIP=%s proxiedIP=%s", probeResult.DirectIP, probeResult.ProxiedIP)
}

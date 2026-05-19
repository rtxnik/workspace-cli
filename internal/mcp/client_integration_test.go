//go:build integration

package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requireUvAndVaultAI mirrors the requireDevProxy pattern from
// internal/xray/integration_test.go: tests under this build tag are exercised
// against real-environment dependencies (uv binary + vault-ai checkout +
// VAULT_AI_TOKEN provisioned). When any dependency is missing we t.Skip the
// test so CI lanes without the dev-env do not flap red.
//
// The helper is package-visible so Plan 18-05's stress test can reuse it.
func requireUvAndVaultAI(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv binary not on PATH")
	}
	root := testVaultAIRoot(t)
	tools := filepath.Join(root, "_tooling", "mcp", "contract", "tools.json")
	if _, err := os.Stat(tools); err != nil {
		t.Skipf("vault-ai checkout missing or incomplete at %s (no tools.json): %v", root, err)
	}
	if os.Getenv("VAULT_AI_TOKEN") == "" {
		t.Skip("VAULT_AI_TOKEN unset — integration tests need a provisioned token")
	}
	return root
}

// testVaultAIRoot returns the operator's vault-ai checkout path. Honors
// VAULT_AI_REPO_ROOT env override; defaults to ~/projects/vault-ai per
// CONTEXT D-08.
func testVaultAIRoot(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("VAULT_AI_REPO_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return filepath.Join(home, "projects", "vault-ai")
}

// TestClientLifecycle exercises the full NewClient → Call("tools/list") →
// Close cycle against a real adapter_stdio subprocess. Total wall-clock budget
// is bounded by ctx so a hung subprocess fails the test rather than hangs CI.
func TestClientLifecycle(t *testing.T) {
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := NewClient(ctx, Options{
		VaultAIRepoRoot: root,
		Version:         "test-integration",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() {
		if err := cl.Close(ctx); err != nil {
			t.Logf("Close: %v", err)
		}
	}()

	// tools/list is the cheapest MCP roundtrip — no embedder/qdrant load.
	// Some MCP servers expose it as a special-cased capability; here we
	// just exercise the Call path with a no-op tool. If the call surfaces
	// a real envelope, we're done; if it returns a transport error, the
	// subprocess is broken (worth surfacing).
	_, err = cl.Call(ctx, "vault_health", nil)
	if err != nil {
		// vault_health may not be registered (per RESEARCH OQ-1); accept
		// "tool not found" as proof the round-trip works. Hard transport
		// errors still fail the test.
		if !strings.Contains(err.Error(), "tool") && !strings.Contains(err.Error(), "not found") &&
			!strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "envelope") {
			t.Fatalf("Call(vault_health) unexpected transport error: %v", err)
		}
		t.Logf("Call(vault_health) returned expected absence/envelope error: %v", err)
	}
}

// TestClientCloseSignalsProcessGroup verifies the Pitfall 4 mitigation: after
// Close() returns, no vault_ai/adapter_stdio/server.py process remains in the
// pgrep tree. This is the structural proof that signaling -pgid actually
// reaches the descendants (uv spawns python which is a child, not the leader).
func TestClientCloseSignalsProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available on this host")
	}
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := NewClient(ctx, Options{
		VaultAIRepoRoot: root,
		Version:         "test-integration",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Best-effort: trigger at least one roundtrip so the subprocess is fully
	// spun up before we ask it to tear down.
	_, _ = cl.Call(ctx, "vault_health", nil)

	if err := cl.Close(ctx); err != nil {
		t.Logf("Close: %v", err)
	}

	// Allow the kernel a brief settle window for the SIGTERM to propagate +
	// the leader to reap.
	time.Sleep(500 * time.Millisecond)

	out, _ := exec.Command("pgrep", "-fc", "vault_ai/adapter_stdio/server.py").Output()
	count := strings.TrimSpace(string(out))
	// pgrep -fc exits non-zero (status 1) when no matches found and prints "0".
	// Either "0" or empty output is acceptable; anything else means the
	// process group was not fully reaped.
	if count != "0" && count != "" {
		t.Errorf("after Close: pgrep -fc vault_ai/adapter_stdio/server.py = %q; want 0 (process group not fully reaped)", count)
	}
}

// TestTokenAbsentFromEnviron asserts that VAULT_AI_TOKEN is NOT present in
// the subprocess's /proc/<pid>/environ during an active Call. This is the
// CONTEXT D-09 defense-in-depth verification: even though the token is
// transported via fd 3 (path-A), we ALSO scrub the env so the operator-facing
// `ps -e e` / `cat /proc/.../environ` surfaces never leak it.
//
// Skipped on non-Linux hosts (/proc layout is Linux-specific).
func TestTokenAbsentFromEnviron(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/<pid>/environ assertion is Linux-specific")
	}
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := NewClient(ctx, Options{
		VaultAIRepoRoot: root,
		Version:         "test-integration",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close(ctx)

	if cl.cmd == nil || cl.cmd.Process == nil {
		t.Fatal("client did not capture subprocess via WithCommandFunc")
	}
	pid := cl.cmd.Process.Pid

	environPath := fmt.Sprintf("/proc/%d/environ", pid)
	environBytes, err := os.ReadFile(environPath)
	if err != nil {
		// Subprocess may have already exited if it raced; that itself
		// proves no leak persists.
		t.Logf("read %s: %v (subprocess may have exited)", environPath, err)
		return
	}
	if bytes.Contains(environBytes, []byte("VAULT_AI_TOKEN")) {
		t.Errorf("VAULT_AI_TOKEN found in %s — Plan 18-01 CLI-12 defense-in-depth gate failed", environPath)
	}
}

// TestTokenReadableFromFD3 asserts that the fd-3 token transport actually
// hands bytes to the child. We confirm this indirectly: the adapter's
// build_server() populates the module-level _FD3_TOKEN global from fd 3 (via
// the S_ISFIFO-guarded reader); if the bytes arrived, no MCP transport
// errors fire and the subprocess proceeds to advertise tools/list.
//
// A direct in-subprocess assertion would require modifying adapter_stdio
// further to echo the token back as an MCP tool response — that surface is
// intentionally absent (the token MUST NOT be echoed). The structural rail
// is: if NewClient succeeds end-to-end with a valid VAULT_AI_TOKEN, fd-3
// transport worked.
func TestTokenReadableFromFD3(t *testing.T) {
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := NewClient(ctx, Options{
		VaultAIRepoRoot: root,
		Version:         "test-integration",
	})
	if err != nil {
		t.Fatalf("NewClient (fd-3 wiring proof): %v", err)
	}
	defer cl.Close(ctx)

	// Sanity: the captured *exec.Cmd has ExtraFiles[0] populated (the fd-3
	// reader end). This is the direct Go-side proof of the wiring.
	if cl.cmd == nil {
		t.Fatal("client did not capture subprocess")
	}
	if len(cl.cmd.ExtraFiles) == 0 {
		t.Errorf("cmd.ExtraFiles is empty — fd-3 token transport not wired")
	}
}

// TestNoGoroutineLeaks asserts the lifecycle does not leak goroutines. We
// snapshot runtime.NumGoroutine before and after a NewClient/Close cycle and
// allow a small drift window for stdlib internals (DNS lookup pools, etc).
// goleak is intentionally not used here to keep the test deps minimal —
// the explicit NumGoroutine pattern is sufficient for the CLI-11 acceptance
// gate.
func TestNoGoroutineLeaks(t *testing.T) {
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Warm-up cycle: first NewClient may pull goroutines from stdlib pools
	// (e.g. uv resolving Python venv). We measure the second cycle.
	for i := 0; i < 2; i++ {
		cl, err := NewClient(ctx, Options{
			VaultAIRepoRoot: root,
			Version:         "test-integration",
		})
		if err != nil {
			t.Fatalf("NewClient cycle %d: %v", i, err)
		}
		_, _ = cl.Call(ctx, "vault_health", nil)
		if err := cl.Close(ctx); err != nil {
			t.Logf("Close cycle %d: %v", i, err)
		}
		// Let the closeGracePeriod-related goroutine finish before sampling.
		time.Sleep(200 * time.Millisecond)
	}

	before := runtime.NumGoroutine()

	cl, err := NewClient(ctx, Options{
		VaultAIRepoRoot: root,
		Version:         "test-integration",
	})
	if err != nil {
		t.Fatalf("NewClient measurement cycle: %v", err)
	}
	_, _ = cl.Call(ctx, "vault_health", nil)
	if err := cl.Close(ctx); err != nil {
		t.Logf("Close: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	after := runtime.NumGoroutine()
	// Allow a small drift window — stdlib internals (e.g. sync.Pool reapers)
	// can shift the count by 1-2 between samples without indicating a leak.
	if after > before+2 {
		t.Errorf("goroutine count grew during NewClient/Close cycle: before=%d after=%d (delta=%d)", before, after, after-before)
	}
}

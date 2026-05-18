//go:build integration

package mcp

// client_stress_test.go — Plan 18-05 Task 1 (CLI-11 ship-gate).
//
// Stresses the subprocess-lifecycle invariants from Plan 18-01 at scale: 100
// sequential NewClient → ListTools → Close cycles MUST leave zero residual
// processes, zero leaked pipe file descriptors, and the goroutine count
// MUST return to ≤2 of the pre-loop baseline.
//
// Source-of-truth: PITFALLS §Pitfall 7 §How-to-avoid §Stress-test +
// RESEARCH §Architecture Patterns §Pattern 3 L275-292. The verbatim spec used
// `Call(ctx, "tools/list", nil)` as the per-iteration roundtrip; the live
// Client.Call wraps MCP `tools/call` (NOT the `tools/list` JSON-RPC method),
// so `Call("tools/list", nil)` would invoke a non-existent tool literally
// named "tools/list" and immediately fail. We use Client.ListTools instead,
// which issues the actual `tools/list` JSON-RPC method against the subprocess
// (proven cheapest roundtrip per client.go L259-282). Rule 1 deviation from
// the verbatim spec; documented in 18-05-SUMMARY.md under Deviations.
//
// Run locally:
//   go test -tags integration ./internal/mcp/ \
//     -run TestClientStress100Sequential -count=1 -timeout 10m -v
//
// Skip pattern matches client_integration_test.go: t.Skip when uv / vault-ai
// checkout / VAULT_AI_TOKEN missing.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stressIterations is the published ship-gate count per PITFALLS §Pitfall 7.
// 100 is large enough to surface a per-iteration leak (1 fd / 1 goroutine /
// 1 zombie per cycle would compound to 100 by end-of-loop) yet small enough
// to complete in well under the 10-minute test deadline at ~2-3s cold-start
// per NewClient (CONTEXT D-06): 100 × 3s ≈ 5min wall-clock budget.
const stressIterations = 100

// stressCtxBudget bounds the full 100-cycle loop. Per-iteration NewClient
// inherits this ctx via the integration test pattern — a hung subprocess in
// any one iteration fails the test rather than hanging CI indefinitely.
const stressCtxBudget = 9 * time.Minute

// goroutineDriftTolerance allows the post-loop goroutine count to drift by
// a small noise floor relative to pre-loop. Matches the tolerance from
// TestNoGoroutineLeaks in client_integration_test.go (stdlib sync.Pool reapers
// + the closeGracePeriod goroutine can shift the count by 1-2 between samples
// without indicating a real leak).
const goroutineDriftTolerance = 2

// pipeDescriptorDriftTolerance allows the post-loop "pipe" descriptor count
// in `lsof -p $$` to drift by a small amount. Real per-iteration fd leaks
// would compound to ~100; a tolerance of 4 catches genuine leakage while
// absorbing stdlib internals (DNS resolver pipes, sync.Pool, etc.).
const pipeDescriptorDriftTolerance = 4

// TestClientStress100Sequential — CLI-11 ship-gate. Runs 100 sequential
// NewClient → ListTools → Close cycles. After the final teardown, asserts
// `pgrep -fc vault_ai/adapter_stdio/server.py == 0`. If any residual process
// remains, the Plan 18-01 process-group teardown is broken (Pitfall 4
// regression) — fix client.go before merge.
func TestClientStress100Sequential(t *testing.T) {
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), stressCtxBudget)
	defer cancel()

	start := time.Now()
	for i := 0; i < stressIterations; i++ {
		cl, err := NewClient(ctx, Options{
			VaultAIRepoRoot: root,
			Version:         "test-stress",
		})
		if err != nil {
			t.Fatalf("iter %d NewClient: %v", i, err)
		}
		if _, err := cl.ListTools(ctx); err != nil {
			// Best-effort teardown before bailing so we don't leak the
			// just-spawned subprocess on test failure.
			_ = cl.Close(ctx)
			t.Fatalf("iter %d ListTools: %v", i, err)
		}
		if err := cl.Close(ctx); err != nil {
			t.Fatalf("iter %d Close: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	t.Logf("stress: %d iterations completed in %s (%.2fs per iteration)",
		stressIterations, elapsed.Round(time.Millisecond),
		elapsed.Seconds()/float64(stressIterations))

	// Allow the kernel a brief settle window for the final iteration's
	// SIGTERM to propagate + the leader to reap. Mirrors the 500ms used in
	// TestClientCloseSignalsProcessGroup.
	time.Sleep(500 * time.Millisecond)

	out, _ := exec.Command("pgrep", "-fc", "vault_ai/adapter_stdio/server.py").Output()
	count := strings.TrimSpace(string(out))
	// pgrep -fc exits non-zero (status 1) when no matches found and prints "0".
	// Either "0" or empty output is acceptable; anything else means the
	// process group was not fully reaped at scale.
	if count != "0" && count != "" {
		// On failure, surface the leaked PIDs so the operator can diagnose
		// the Plan 18-01 regression source.
		leaked, _ := exec.Command("pgrep", "-fl", "vault_ai/adapter_stdio/server.py").Output()
		t.Fatalf(
			"after %d stress iterations: pgrep -fc returned %q (want 0). "+
				"Leaked processes:\n%s\n"+
				"CLI-11 ship-gate FAILED — subprocess-lifecycle regression in client.go process-group teardown.",
			stressIterations, count, string(leaked),
		)
	}
}

// TestClientStress100SequentialNoFileDescriptorLeaks — Plan 18-05 Task 1
// belt-and-suspenders alongside the pgrep gate. Counts pipe-type file
// descriptors in `lsof -p $$` before and after the 100-cycle loop; asserts
// the count returns to within pipeDescriptorDriftTolerance of the baseline.
//
// Each NewClient creates two pipes: the fd-3 token pipe (path-A per CONTEXT
// D-09a) and the library's stdio pipe pair. A leak of even one fd per cycle
// compounds to 100 leaked fds; this catches that class of regression while
// absorbing stdlib noise.
func TestClientStress100SequentialNoFileDescriptorLeaks(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not on PATH — required for fd-leak detection")
	}
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), stressCtxBudget)
	defer cancel()

	// Warm-up cycle: first NewClient may pull resources from stdlib pools
	// (DNS resolver, uv venv resolution). Measure the steady-state delta.
	for i := 0; i < 2; i++ {
		cl, err := NewClient(ctx, Options{
			VaultAIRepoRoot: root,
			Version:         "test-stress-fd-warmup",
		})
		if err != nil {
			t.Fatalf("warmup %d NewClient: %v", i, err)
		}
		_, _ = cl.ListTools(ctx)
		_ = cl.Close(ctx)
		time.Sleep(200 * time.Millisecond)
	}

	before := countPipeDescriptors(t)

	for i := 0; i < stressIterations; i++ {
		cl, err := NewClient(ctx, Options{
			VaultAIRepoRoot: root,
			Version:         "test-stress-fd",
		})
		if err != nil {
			_ = cl.Close(ctx)
			t.Fatalf("iter %d NewClient: %v", i, err)
		}
		if _, err := cl.ListTools(ctx); err != nil {
			_ = cl.Close(ctx)
			t.Fatalf("iter %d ListTools: %v", i, err)
		}
		if err := cl.Close(ctx); err != nil {
			t.Fatalf("iter %d Close: %v", i, err)
		}
	}

	// Let the closeGracePeriod-related goroutine finish + kernel reap pipes.
	time.Sleep(500 * time.Millisecond)

	after := countPipeDescriptors(t)

	delta := after - before
	if delta > pipeDescriptorDriftTolerance {
		t.Errorf(
			"pipe descriptor count grew during %d stress iterations: before=%d after=%d delta=%d "+
				"(tolerance=%d). Likely cause: tokenPipe.Close() or stdio pipe pair not released on Close.",
			stressIterations, before, after, delta, pipeDescriptorDriftTolerance,
		)
	}
}

// TestClientStressGoroutineLeaks — Plan 18-05 Task 1 third assertion of the
// stress trio. Snapshots runtime.NumGoroutine before and after the 100-cycle
// loop; asserts the delta is within goroutineDriftTolerance.
//
// Each Client.Close spawns one goroutine that races library.Close against the
// 5s grace timer (client.go L223-241). That goroutine MUST exit before the
// next iteration begins, otherwise the count grows linearly with iterations.
func TestClientStressGoroutineLeaks(t *testing.T) {
	root := requireUvAndVaultAI(t)

	ctx, cancel := context.WithTimeout(context.Background(), stressCtxBudget)
	defer cancel()

	// Warm-up to absorb stdlib pool initialization.
	for i := 0; i < 2; i++ {
		cl, err := NewClient(ctx, Options{
			VaultAIRepoRoot: root,
			Version:         "test-stress-goroutine-warmup",
		})
		if err != nil {
			t.Fatalf("warmup %d NewClient: %v", i, err)
		}
		_, _ = cl.ListTools(ctx)
		_ = cl.Close(ctx)
		time.Sleep(200 * time.Millisecond)
	}

	before := runtime.NumGoroutine()

	for i := 0; i < stressIterations; i++ {
		cl, err := NewClient(ctx, Options{
			VaultAIRepoRoot: root,
			Version:         "test-stress-goroutine",
		})
		if err != nil {
			_ = cl.Close(ctx)
			t.Fatalf("iter %d NewClient: %v", i, err)
		}
		if _, err := cl.ListTools(ctx); err != nil {
			_ = cl.Close(ctx)
			t.Fatalf("iter %d ListTools: %v", i, err)
		}
		if err := cl.Close(ctx); err != nil {
			t.Fatalf("iter %d Close: %v", i, err)
		}
	}

	// Let the closeGracePeriod-related goroutine from the last iteration
	// finish before sampling.
	time.Sleep(500 * time.Millisecond)

	after := runtime.NumGoroutine()
	delta := after - before
	if delta > goroutineDriftTolerance {
		t.Errorf(
			"goroutine count grew during %d stress iterations: before=%d after=%d delta=%d "+
				"(tolerance=%d). Likely cause: closeGracePeriod goroutine in client.go Close() not exiting.",
			stressIterations, before, after, delta, goroutineDriftTolerance,
		)
	}
}

// countPipeDescriptors invokes `lsof -p <our-pid>` and returns the number of
// rows whose TYPE column is "PIPE" or "FIFO" (both denote pipe-type fds on
// Linux). Returns 0 + t.Skip on lsof failure (e.g. sandbox restrictions).
func countPipeDescriptors(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("lsof", "-p", fmt.Sprint(os.Getpid())).Output()
	if err != nil {
		// lsof may exit non-zero on sandboxed CI; skip rather than fail.
		t.Skipf("lsof failed; cannot measure pipe descriptors: %v", err)
	}
	var count int
	for _, line := range strings.Split(string(out), "\n") {
		// lsof columns: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
		// We match on TYPE field == PIPE or FIFO.
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		typ := fields[4]
		if typ == "PIPE" || typ == "FIFO" {
			count++
		}
	}
	return count
}

package procx

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRun_KillsOnDeadline proves the deadline fires: a `sleep 10` under a
// 50ms timeout must return an error quickly, not block for 10s.
// (Behavior moved from docker.runWithTimeout / workspace.runProbe tests.)
func TestRun_KillsOnDeadline(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), 50*time.Millisecond, "sleep", "10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error, got nil (command was not bounded)")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline not honored: Run took %v", elapsed)
	}
}

// TestRunCombined_KillsOnDeadline mirrors the deadline guarantee for the
// combined-output variant.
func TestRunCombined_KillsOnDeadline(t *testing.T) {
	start := time.Now()
	_, err := RunCombined(context.Background(), 50*time.Millisecond, "sleep", "10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error, got nil (command was not bounded)")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline not honored: RunCombined took %v", elapsed)
	}
}

// TestRun_Success returns stdout when the command finishes in time.
func TestRun_Success(t *testing.T) {
	out, err := Run(context.Background(), 5*time.Second, "echo", "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hi" {
		t.Errorf("output = %q, want %q", strings.TrimSpace(string(out)), "hi")
	}
}

// TestRun_StderrKeptOutOfStdout pins the Output() contract callers parse
// against: stderr must NOT pollute the returned bytes, and on failure it must
// arrive in *exec.ExitError.Stderr unwrapped (callers errors.As on it).
func TestRun_StderrKeptOutOfStdout(t *testing.T) {
	out, err := Run(context.Background(), 5*time.Second,
		"sh", "-c", "echo visible; echo hidden >&2; exit 3")
	if strings.Contains(string(out), "hidden") {
		t.Errorf("stderr leaked into stdout bytes: %q", out)
	}
	if !strings.Contains(string(out), "visible") {
		t.Errorf("stdout missing: %q", out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error is not *exec.ExitError (unwrapped stdlib contract): %v", err)
	}
	if ee.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", ee.ExitCode())
	}
	if !strings.Contains(string(ee.Stderr), "hidden") {
		t.Errorf("ExitError.Stderr missing stderr text: %q", ee.Stderr)
	}
}

// TestRunCombined_MergesStderr pins the CombinedOutput() contract: callers
// (ProxyExec, execInContainer, devpod status) read merged diagnostics.
func TestRunCombined_MergesStderr(t *testing.T) {
	out, err := RunCombined(context.Background(), 5*time.Second,
		"sh", "-c", "echo one; echo two >&2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "one") || !strings.Contains(string(out), "two") {
		t.Errorf("combined output missing a stream: %q", out)
	}
}

// TestRun_ParentContextCancelWins proves the ctx parameter composes: an
// already-cancelled parent fails immediately even under a generous timeout.
func TestRun_ParentContextCancelWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := Run(ctx, 5*time.Second, "sleep", "10")
	if err == nil {
		t.Fatal("expected error from cancelled parent context, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancelled parent not honored promptly: %v", elapsed)
	}
}

// TestRun_NotFoundIsUnwrapped pins that a missing binary surfaces
// exec.ErrNotFound through errors.Is — devpodStatuses branches on it.
func TestRun_NotFoundIsUnwrapped(t *testing.T) {
	_, err := Run(context.Background(), 5*time.Second, "definitely-not-a-binary-xx22")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("errors.Is(err, exec.ErrNotFound) = false; err=%v", err)
	}
}

package docker

import (
	"strings"
	"testing"
	"time"
)

// TestRunWithTimeout_KillsOnDeadline proves the deadline fires: a `sleep 10`
// under a 50ms timeout must return an error quickly, not block for 10s.
func TestRunWithTimeout_KillsOnDeadline(t *testing.T) {
	start := time.Now()
	_, err := runWithTimeout(50*time.Millisecond, "sleep", "10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error, got nil (command was not bounded)")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline not honored: runWithTimeout took %v", elapsed)
	}
}

// TestRunWithTimeout_Success returns output when the command finishes in time.
func TestRunWithTimeout_Success(t *testing.T) {
	out, err := runWithTimeout(5*time.Second, "echo", "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hi" {
		t.Errorf("output = %q, want %q", strings.TrimSpace(string(out)), "hi")
	}
}

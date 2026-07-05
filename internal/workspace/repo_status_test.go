package workspace

import (
	"strings"
	"testing"
	"time"
)

// TestRunProbe_KillsOnDeadline proves the deadline fires: a `sleep 10` under a
// 50ms timeout must return an error quickly, not block for 10s.
func TestRunProbe_KillsOnDeadline(t *testing.T) {
	start := time.Now()
	_, err := runProbe(50*time.Millisecond, "sleep", "10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error, got nil (command was not bounded)")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline not honored: runProbe took %v", elapsed)
	}
}

// TestRunProbe_Success returns stdout when the command finishes in time.
func TestRunProbe_Success(t *testing.T) {
	out, err := runProbe(5*time.Second, "echo", "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hi" {
		t.Errorf("output = %q, want %q", strings.TrimSpace(string(out)), "hi")
	}
}

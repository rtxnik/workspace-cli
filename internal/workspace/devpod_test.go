package workspace

import (
	"testing"
	"time"
)

// TestDevpodExec_TimeoutKills proves the bounded branch honors the deadline:
// `sleep 10` under a 50ms timeout returns an error quickly. devpodBin is swapped
// to `sleep` so the test needs neither devpod nor docker.
func TestDevpodExec_TimeoutKills(t *testing.T) {
	orig := devpodBin
	defer func() { devpodBin = orig }()
	devpodBin = "sleep"

	start := time.Now()
	err := devpodExec(50*time.Millisecond, "10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error, got nil (command was not bounded)")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline not honored: devpodExec took %v", elapsed)
	}
}

// TestDevpodExec_NoTimeoutRuns proves the unbounded branch (timeout <= 0) still
// runs the command to completion.
func TestDevpodExec_NoTimeoutRuns(t *testing.T) {
	orig := devpodBin
	defer func() { devpodBin = orig }()
	devpodBin = "true"

	if err := devpodExec(0, "ignored"); err != nil {
		t.Fatalf("unbounded devpodExec should succeed, got %v", err)
	}
}

package workspace

import (
	"reflect"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/output"
)

// TestDevpodExec_TimeoutKills proves the bounded branch honors the deadline:
// `sleep 10` under a 50ms timeout returns an error quickly. devpodBin is swapped
// to `sleep` so the test needs neither devpod nor docker.
func TestDevpodExec_TimeoutKills(t *testing.T) {
	orig := devpodBin
	defer func() { devpodBin = orig }()
	devpodBin = "sleep"

	start := time.Now()
	err := devpodExec(50*time.Millisecond, nil, "10")
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

	if err := devpodExec(0, nil, "ignored"); err != nil {
		t.Fatalf("unbounded devpodExec should succeed, got %v", err)
	}
}

// TestDevpodExecWritesTheChildToTheLog: under a step runner task the child's
// stdout and stderr both go to the task's log, and nothing to the terminal.
func TestDevpodExecWritesTheChildToTheLog(t *testing.T) {
	orig := devpodBin
	defer func() { devpodBin = orig }()
	devpodBin = "sh"

	var tail []string
	err := output.Run(output.Task{Title: "Stopping workspace", Run: func(log *output.Log) error {
		if err := devpodExec(timeoutLifecycle, log, "-c", "echo 'info stop: step one'; echo 'warn stop: step two' >&2"); err != nil {
			return err
		}
		tail = log.Tail()
		return nil
	}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := []string{"info stop: step one", "warn stop: step two"}; !reflect.DeepEqual(tail, want) {
		t.Errorf("the log holds %q; want %q", tail, want)
	}
}

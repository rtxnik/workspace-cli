package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/rtxnik/workspace-cli/internal/output"
)

// Timeouts for devpod shell-outs. Probe/query commands get a short bound;
// lifecycle mutations a longer one. Provisioning/streaming/interactive commands
// (up, code, logs, ssh) intentionally run unbounded.
const (
	timeoutProbe     = 10 * time.Second
	timeoutLifecycle = 60 * time.Second
)

// devpodBin is the devpod executable name. A package var so tests can point it
// at a stand-in (e.g. `sleep`) without devpod installed.
var devpodBin = "devpod"

// DevpodUp starts a workspace using devpod. Provisioning streams progress and can
// legitimately take minutes -- run unbounded. The child writes to log, a step
// runner task's log, or to the terminal when log is nil; so do DevpodStop's
// and DevpodDelete's.
func DevpodUp(source string, log *output.Log) error {
	return devpodExec(0, log, "up", source)
}

// DevpodStop stops a running workspace.
func DevpodStop(name string, log *output.Log) error {
	return devpodExec(timeoutLifecycle, log, "stop", name)
}

// DevpodDelete removes a workspace from devpod.
func DevpodDelete(name string, log *output.Log) error {
	return devpodExec(timeoutLifecycle, log, "delete", name)
}

// DevpodSSH opens an SSH session to a workspace.
// It connects stdin/stdout/stderr for interactive use. Interactive session --
// intentionally unbounded (a deadline would kill a live SSH session).
func DevpodSSH(name string) error {
	cmd := exec.Command("devpod", "ssh", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// DevpodCode opens a workspace in VS Code. Provisions + streams -- run unbounded.
func DevpodCode(name string) error {
	return devpodExec(0, nil, "up", name, "--ide", "vscode")
}

// DevpodLogs shows workspace logs from devpod. Streaming output -- run unbounded.
func DevpodLogs(name string) error {
	return devpodExec(0, nil, "logs", name)
}

// devpodExec runs `devpod <args...>`, its stdout and stderr both wired to log's
// file, or to the terminal's when log is nil. A positive timeout bounds the
// command with a hard deadline; timeout <= 0 runs it unbounded (for
// streaming/provisioning commands whose progress is visible and Ctrl-C-able).
func devpodExec(timeout time.Duration, log *output.Log, args ...string) error {
	var cmd *exec.Cmd
	if timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd = exec.CommandContext(ctx, devpodBin, args...)
	} else {
		cmd = exec.Command(devpodBin, args...)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if log != nil {
		cmd.Stdout, cmd.Stderr = log.File(), log.File()
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("devpod %s: %w", args[0], err)
	}
	return nil
}

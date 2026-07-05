package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
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
// legitimately take minutes -- run unbounded.
func DevpodUp(source string) error {
	return devpodExec(0, "up", source)
}

// DevpodStop stops a running workspace.
func DevpodStop(name string) error {
	return devpodExec(timeoutLifecycle, "stop", name)
}

// DevpodDelete removes a workspace from devpod.
func DevpodDelete(name string) error {
	return devpodExec(timeoutLifecycle, "delete", name)
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
	return devpodExec(0, "up", name, "--ide", "vscode")
}

// DevpodLogs shows workspace logs from devpod. Streaming output -- run unbounded.
func DevpodLogs(name string) error {
	return devpodExec(0, "logs", name)
}

// devpodExec runs `devpod <args...>`, wiring stdout/stderr. A positive timeout
// bounds the command with a hard deadline; timeout <= 0 runs it unbounded (for
// streaming/provisioning commands whose progress is visible and Ctrl-C-able).
func devpodExec(timeout time.Duration, args ...string) error {
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
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("devpod %s: %w", args[0], err)
	}
	return nil
}

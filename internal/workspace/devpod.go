package workspace

import (
	"fmt"
	"os"
	"os/exec"
)

// DevpodUp starts a workspace using devpod.
func DevpodUp(name, source string) error {
	return devpodExec("up", name, "--source", source)
}

// DevpodStop stops a running workspace.
func DevpodStop(name string) error {
	return devpodExec("stop", name)
}

// DevpodDelete removes a workspace from devpod.
func DevpodDelete(name string) error {
	return devpodExec("delete", name)
}

// DevpodSSH opens an SSH session to a workspace.
// It connects stdin/stdout/stderr for interactive use.
func DevpodSSH(name string) error {
	cmd := exec.Command("devpod", "ssh", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// DevpodCode opens a workspace in VS Code.
func DevpodCode(name string) error {
	return devpodExec("up", name, "--ide", "vscode")
}

func devpodExec(args ...string) error {
	cmd := exec.Command("devpod", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("devpod %s: %w", args[0], err)
	}
	return nil
}

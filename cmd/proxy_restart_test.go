package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
)

func TestProxyRestartHappyPath(t *testing.T) {
	orig := proxyRestartCmdFn
	t.Cleanup(func() { proxyRestartCmdFn = orig })
	origConn := proxyConnectedContainersFn
	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) { return nil, nil }
	t.Cleanup(func() { proxyConnectedContainersFn = origConn })
	var called int
	proxyRestartCmdFn = func(_ config.Config) error {
		called++
		return nil
	}

	cmd := rootCmd
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "restart"})
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if called != 1 {
		t.Errorf("expected proxyRestartCmdFn called once, got %d", called)
	}
	if !strings.Contains(out.String(), "Proxy restarted") {
		t.Errorf("expected stdout to contain 'Proxy restarted'; got %q", out.String())
	}
}

func TestProxyRestartFailure(t *testing.T) {
	orig := proxyRestartCmdFn
	t.Cleanup(func() { proxyRestartCmdFn = orig })
	origConn := proxyConnectedContainersFn
	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) { return nil, nil }
	t.Cleanup(func() { proxyConnectedContainersFn = origConn })
	proxyRestartCmdFn = func(_ config.Config) error {
		return errors.New("docker daemon unreachable")
	}

	cmd := rootCmd
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "restart"})
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when proxyRestartCmdFn fails")
	}
	combined := out.String() + errOut.String() + err.Error()
	if !strings.Contains(combined, "proxy restart failed") {
		t.Errorf("expected wrapped error 'proxy restart failed'; got %q / err=%v", combined, err)
	}
	if !strings.Contains(combined, "docker daemon unreachable") {
		t.Errorf("expected underlying error preserved; got %q / err=%v", combined, err)
	}
	if strings.Contains(combined, "Usage:") {
		t.Errorf("SilenceUsage must suppress usage block; got %q", combined)
	}
}

func TestProxyRestartWarnGate_DeclineAborts(t *testing.T) {
	// Connected workspaces present + operator declines -> restart exits
	// cleanly (nil error, not errAborted) and must NOT touch the container.
	origConn := proxyConnectedContainersFn
	origConfirm := warnConfirmFn
	origRestart := proxyRestartCmdFn
	t.Cleanup(func() {
		proxyConnectedContainersFn = origConn
		warnConfirmFn = origConfirm
		proxyRestartCmdFn = origRestart
		_ = proxyRestartCmd.Flags().Set("force", "false")
	})

	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) {
		return []string{"ws-alpha"}, nil
	}
	warnConfirmFn = func(_, _ string) bool { return false } // operator declines
	var called int
	proxyRestartCmdFn = func(_ config.Config) error { called++; return nil }
	if err := proxyRestartCmd.Flags().Set("force", "false"); err != nil {
		t.Fatal(err)
	}

	cmd := rootCmd
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "restart"})
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	var execErr error
	stderr := captureStderr(t, func() {
		execErr = cmd.Execute()
	})
	if execErr != nil {
		t.Fatalf("declined confirm must exit cleanly with nil error; got %v (cmd stderr=%q)", execErr, errOut.String())
	}
	if called != 0 {
		t.Errorf("restart must not run after a decline; got called=%d", called)
	}
	if !strings.Contains(stderr, "Aborted") {
		t.Errorf("expected 'Aborted' to be printed on decline; got %q", stderr)
	}
}

func TestProxyRestartRegistered(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "proxy" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "restart" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("`ws proxy restart` not registered as a subcommand of `ws proxy`")
	}
}

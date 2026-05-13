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

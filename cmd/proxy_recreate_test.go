package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
)

func TestProxyRecreateHappyPath(t *testing.T) {
	orig := proxyRecreateCmdFn
	t.Cleanup(func() { proxyRecreateCmdFn = orig })
	var called int
	proxyRecreateCmdFn = func(_ config.Config) error {
		called++
		return nil
	}

	cmd := rootCmd
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "recreate"})
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if called != 1 {
		t.Errorf("expected proxyRecreateCmdFn called once, got %d", called)
	}
	if !strings.Contains(out.String(), "Proxy recreated") {
		t.Errorf("expected stdout to contain 'Proxy recreated'; got %q", out.String())
	}
}

func TestProxyRecreateFailure(t *testing.T) {
	orig := proxyRecreateCmdFn
	t.Cleanup(func() { proxyRecreateCmdFn = orig })
	proxyRecreateCmdFn = func(_ config.Config) error {
		return errors.New("docker daemon unreachable")
	}

	cmd := rootCmd
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "recreate"})
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when proxyRecreateCmdFn fails")
	}
	combined := out.String() + errOut.String() + err.Error()
	if !strings.Contains(combined, "proxy recreate failed") {
		t.Errorf("expected wrapped error 'proxy recreate failed'; got %q / err=%v", combined, err)
	}
	if !strings.Contains(combined, "docker daemon unreachable") {
		t.Errorf("expected underlying error preserved; got %q / err=%v", combined, err)
	}
	if strings.Contains(combined, "Usage:") {
		t.Errorf("SilenceUsage must suppress usage block; got %q", combined)
	}
}

func TestProxyRecreateRegistered(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "proxy" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "recreate" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("`ws proxy recreate` not registered as a subcommand of `ws proxy`")
	}
}

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
	origConn := proxyConnectedContainersFn
	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) { return nil, nil }
	t.Cleanup(func() { proxyConnectedContainersFn = origConn })
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
	origConn := proxyConnectedContainersFn
	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) { return nil, nil }
	t.Cleanup(func() { proxyConnectedContainersFn = origConn })
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

func TestProxyRecreateWarnGate_DeclineAborts(t *testing.T) {
	// Connected workspaces present + operator declines -> recreate must abort
	// with errAborted and must NOT touch the container.
	origConn := proxyConnectedContainersFn
	origConfirm := warnConfirmFn
	origRecreate := proxyRecreateCmdFn
	t.Cleanup(func() {
		proxyConnectedContainersFn = origConn
		warnConfirmFn = origConfirm
		proxyRecreateCmdFn = origRecreate
		_ = proxyRecreateCmd.Flags().Set("force", "false")
	})

	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) {
		return []string{"ws-alpha"}, nil
	}
	warnConfirmFn = func(_, _ string) bool { return false } // operator declines
	var called int
	proxyRecreateCmdFn = func(_ config.Config) error { called++; return nil }
	if err := proxyRecreateCmd.Flags().Set("force", "false"); err != nil {
		t.Fatal(err)
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
	if !errors.Is(err, errAborted) {
		t.Fatalf("expected errAborted on operator decline; got %v (stderr=%q)", err, errOut.String())
	}
	if called != 0 {
		t.Errorf("recreate must not run after a decline; got called=%d", called)
	}
}

func TestProxyRecreateWarnGate_ForceSkipsPrompt(t *testing.T) {
	// --force proceeds even with connected workspaces, without prompting.
	origConn := proxyConnectedContainersFn
	origConfirm := warnConfirmFn
	origRecreate := proxyRecreateCmdFn
	t.Cleanup(func() {
		proxyConnectedContainersFn = origConn
		warnConfirmFn = origConfirm
		proxyRecreateCmdFn = origRecreate
		_ = proxyRecreateCmd.Flags().Set("force", "false")
	})

	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) {
		return []string{"ws-alpha"}, nil
	}
	var confirmCalls int
	warnConfirmFn = func(_, _ string) bool { confirmCalls++; return false }
	var called int
	proxyRecreateCmdFn = func(_ config.Config) error { called++; return nil }

	cmd := rootCmd
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "recreate", "--force"})
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("--force must proceed without error; got %v (stderr=%q)", err, errOut.String())
	}
	if confirmCalls != 0 {
		t.Errorf("--force must not prompt; confirm called %d times", confirmCalls)
	}
	if called != 1 {
		t.Errorf("expected recreate to run once under --force; got %d", called)
	}
}

func TestProxyRecreateWarnGate_EnumErrorFailsClosed(t *testing.T) {
	// A genuine enumeration failure must fail closed: recreate aborts with the
	// error and does NOT silently proceed, nor report a user abort.
	origConn := proxyConnectedContainersFn
	origConfirm := warnConfirmFn
	origRecreate := proxyRecreateCmdFn
	t.Cleanup(func() {
		proxyConnectedContainersFn = origConn
		warnConfirmFn = origConfirm
		proxyRecreateCmdFn = origRecreate
		_ = proxyRecreateCmd.Flags().Set("force", "false")
	})

	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) {
		return nil, errors.New("docker daemon unreachable")
	}
	warnConfirmFn = func(_, _ string) bool { return true } // would confirm, but must never be asked
	var called int
	proxyRecreateCmdFn = func(_ config.Config) error { called++; return nil }
	if err := proxyRecreateCmd.Flags().Set("force", "false"); err != nil {
		t.Fatal(err)
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
		t.Fatal("genuine enumeration error must fail closed (non-nil error)")
	}
	if errors.Is(err, errAborted) {
		t.Fatalf("enumeration failure must not be reported as a user abort; got %v", err)
	}
	if !strings.Contains(err.Error(), "connected workspaces") {
		t.Errorf("expected fail-closed error to mention connected-workspace enumeration; got %v", err)
	}
	if called != 0 {
		t.Errorf("recreate must not run when enumeration failed; got called=%d", called)
	}
}

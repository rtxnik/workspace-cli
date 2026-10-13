package cmd

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
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

	resetSilenceUsage(t, "proxy", "restart")
	failed, err := cmd.ExecuteC()
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
	// A runtime error, not a usage error: the root prints no usage lines.
	if _, _, isUsage := usageTarget(failed, err); isUsage {
		t.Errorf("a failed restart is classified as a usage error: %v", err)
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
	warnConfirmFn = func(_, _ string) (bool, error) { return false, nil } // operator declines
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

// With workspaces connected, a gate's prompt that cannot ask makes ws proxy
// restart return the Problem that names the stream and the step with
// --force, with nothing printed and the proxy left alone; a cancelled prompt
// is returned as it is, for the root to die of its signal, with nothing
// printed either.
func TestProxyRestartWarnGate_PromptRefusals(t *testing.T) {
	origConn := proxyConnectedContainersFn
	origConfirm := warnConfirmFn
	origRestart := proxyRestartCmdFn
	t.Cleanup(func() {
		proxyConnectedContainersFn = origConn
		warnConfirmFn = origConfirm
		proxyRestartCmdFn = origRestart
	})
	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) {
		return []string{"ws-alpha"}, nil
	}
	var called int
	proxyRestartCmdFn = func(_ config.Config) error { called++; return nil }

	resetFlags(t, proxyRestartCmd)
	warnConfirmFn = func(_, _ string) (bool, error) { return false, errNoTerminal }
	var out, errOut *bytes.Buffer
	var err error
	stdout, stderr := captureStd(t, func() { out, errOut, err = execCapture(t, "proxy", "restart") })
	if !errors.Is(err, output.ErrNoTTY) {
		t.Errorf("errors.Is(%v, ErrNoTTY) = false; want true", err)
	}
	want := output.Problem{
		Title: "Cannot ask for confirmation: no terminal",
		Cause: "stdin is not a terminal",
		Steps: []output.Remedy{{Label: "Skip the prompt", Words: []string{"ws", "proxy", "restart", "--force"}}},
	}
	if p, ok := output.ProblemOf(err); !ok || !reflect.DeepEqual(p, want) {
		t.Errorf("no terminal: the Problem is %+v, %t; want %+v", p, ok, want)
	}
	if printed := stdout + stderr + out.String() + errOut.String(); printed != "" {
		t.Errorf("no terminal: the command printed %q; want nothing", printed)
	}

	resetFlags(t, proxyRestartCmd)
	warnConfirmFn = func(_, _ string) (bool, error) { return false, &output.Cancelled{Signal: syscall.SIGTERM} }
	stdout, stderr = captureStd(t, func() { out, errOut, err = execCapture(t, "proxy", "restart") })
	var cancelled *output.Cancelled
	if !errors.As(err, &cancelled) || cancelled.Signal != syscall.SIGTERM {
		t.Errorf("cancelled: the command returned %v; want the SIGTERM cancellation", err)
	}
	if printed := stdout + stderr + out.String() + errOut.String(); printed != "" {
		t.Errorf("cancelled: the command printed %q; want nothing", printed)
	}

	if called != 0 {
		t.Errorf("the proxy was restarted %d time(s) by a prompt that did not answer", called)
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

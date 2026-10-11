package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it. Needed because output.Info writes directly to
// os.Stderr rather than through the cobra command's configured error writer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	fn()

	os.Stderr = orig
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("drain stderr pipe: %v", err)
	}
	_ = r.Close()
	return buf.String()
}

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

	resetSilenceUsage(t, "proxy", "recreate")
	failed, err := cmd.ExecuteC()
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
	// A runtime error, not a usage error: the root prints no usage lines.
	if _, _, isUsage := usageTarget(failed, err); isUsage {
		t.Errorf("a failed recreate is classified as a usage error: %v", err)
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
	// Connected workspaces present + operator declines -> recreate exits
	// cleanly (nil error, not errAborted) and must NOT touch the container.
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
	warnConfirmFn = func(_, _ string) (bool, error) { return false, nil } // operator declines
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

	var execErr error
	stderr := captureStderr(t, func() {
		execErr = cmd.Execute()
	})
	if execErr != nil {
		t.Fatalf("declined confirm must exit cleanly with nil error; got %v (cmd stderr=%q)", execErr, errOut.String())
	}
	if called != 0 {
		t.Errorf("recreate must not run after a decline; got called=%d", called)
	}
	if !strings.Contains(stderr, "Aborted") {
		t.Errorf("expected 'Aborted' to be printed on decline; got %q", stderr)
	}
}

// With workspaces connected, a gate's prompt that cannot ask makes ws proxy
// recreate return the Problem that names the stream and the step with
// --force, with nothing printed and the proxy left alone; a cancelled prompt
// is returned as it is, with nothing printed either.
func TestProxyRecreateWarnGate_PromptRefusals(t *testing.T) {
	origConn := proxyConnectedContainersFn
	origConfirm := warnConfirmFn
	origRecreate := proxyRecreateCmdFn
	t.Cleanup(func() {
		proxyConnectedContainersFn = origConn
		warnConfirmFn = origConfirm
		proxyRecreateCmdFn = origRecreate
	})
	proxyConnectedContainersFn = func(_ config.Config) ([]string, error) {
		return []string{"ws-alpha"}, nil
	}
	var called int
	proxyRecreateCmdFn = func(_ config.Config) error { called++; return nil }

	resetFlags(t, proxyRecreateCmd)
	warnConfirmFn = func(_, _ string) (bool, error) { return false, errNoTerminal }
	var out, errOut *bytes.Buffer
	var err error
	stdout, stderr := captureStd(t, func() { out, errOut, err = execCapture(t, "proxy", "recreate") })
	if !errors.Is(err, output.ErrNoTTY) {
		t.Errorf("errors.Is(%v, ErrNoTTY) = false; want true", err)
	}
	want := output.Problem{
		Title: "Cannot ask for confirmation: no terminal",
		Cause: "stdin is not a terminal",
		Steps: []output.Remedy{{Label: "Skip the prompt", Words: []string{"ws", "proxy", "recreate", "--force"}}},
	}
	if p, ok := output.ProblemOf(err); !ok || !reflect.DeepEqual(p, want) {
		t.Errorf("no terminal: the Problem is %+v, %t; want %+v", p, ok, want)
	}
	if printed := stdout + stderr + out.String() + errOut.String(); printed != "" {
		t.Errorf("no terminal: the command printed %q; want nothing", printed)
	}

	resetFlags(t, proxyRecreateCmd)
	warnConfirmFn = func(_, _ string) (bool, error) { return false, &output.Cancelled{Signal: syscall.SIGINT} }
	stdout, stderr = captureStd(t, func() { out, errOut, err = execCapture(t, "proxy", "recreate") })
	var cancelled *output.Cancelled
	if !errors.As(err, &cancelled) || cancelled.Signal != syscall.SIGINT {
		t.Errorf("cancelled: the command returned %v; want the SIGINT cancellation", err)
	}
	if printed := stdout + stderr + out.String() + errOut.String(); printed != "" {
		t.Errorf("cancelled: the command printed %q; want nothing", printed)
	}

	if called != 0 {
		t.Errorf("the proxy was recreated %d time(s) by a prompt that did not answer", called)
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
	warnConfirmFn = func(_, _ string) (bool, error) { confirmCalls++; return false, nil }
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
	warnConfirmFn = func(_, _ string) (bool, error) { return true, nil } // would confirm, but must never be asked
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

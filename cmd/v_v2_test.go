package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// quietStd silences both os.Stdout and os.Stderr for the duration of fn. The
// delete path renders a spinner and status lines (spinner + output.* write to
// os.Stderr; the spinner success line reaches os.Stdout via fmt.Println) that
// would otherwise leak raw into test output.
func quietStd(t *testing.T, fn func()) {
	t.Helper()
	_, _ = captureStd(t, fn)
}

// captureStd runs fn with os.Stdout and os.Stderr each a pipe of its own and
// returns what fn wrote to each — the layer's streams write to whatever the
// two are at the time of the write. Both pipes are drained while fn runs, so
// that a chatty fn cannot fill one and block.
func captureStd(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	var out, errOut bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&out, rOut) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errOut, rErr) }()
	os.Stdout, os.Stderr = wOut, wErr
	fn()
	os.Stdout, os.Stderr = origOut, origErr
	_ = wOut.Close()
	_ = wErr.Close()
	wg.Wait()
	_ = rOut.Close()
	_ = rErr.Close()
	return out.String(), errOut.String()
}

// resetFlags sets every flag of c, inherited ones included, back to its
// default and unmarks it, so that a run of the package's command tree starts
// as a fresh one would: the tree is a singleton, and a flag keeps what an
// earlier run in this process gave it.
func resetFlags(t *testing.T, c *cobra.Command) {
	t.Helper()
	reset := func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	}
	c.LocalFlags().VisitAll(reset)
	c.InheritedFlags().VisitAll(reset)
}

// stubDeleteConfirm makes ws delete's prompt answer ok and err.
func stubDeleteConfirm(t *testing.T, ok bool, err error) {
	t.Helper()
	orig := deleteConfirmFn
	deleteConfirmFn = func(_ bool, _, _ string) (bool, error) { return ok, err }
	t.Cleanup(func() { deleteConfirmFn = orig })
}

// setWorkspacesDir points config.Load at a temp workspaces root for the test.
func setWorkspacesDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("WORKSPACES_DIR", dir)
}

// An empty workspace name joins to the workspaces root itself; delete must
// reject it before RemoveAll, leaving every workspace intact.
func TestSEC4_WorkspaceDelete_EmptyNameRejected(t *testing.T) {
	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	setWorkspacesDir(t, wsRoot)

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "delete", "--", "") })
	if err == nil {
		t.Fatal(`delete "" must return an error`)
	}
	if _, statErr := os.Stat(filepath.Join(wsRoot, "keep")); statErr != nil {
		t.Fatalf("workspaces root wiped by empty-name delete: %v", statErr)
	}
}

// A traversal name must be rejected before RemoveAll can reach a sibling.
func TestV2a_WorkspaceDelete_TraversalNameRejected(t *testing.T) {
	root := t.TempDir()
	wsRoot := filepath.Join(root, "workspaces")
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	setWorkspacesDir(t, wsRoot)

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "delete", "--", "../victim") })
	if err == nil {
		t.Fatal(`delete "../victim" must return an error`)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("sibling removed via traversal delete: %v", statErr)
	}
}

// A leading-dash name is rejected at entry, so it never reaches the devpod /
// RemoveAll sinks as a parsed flag or a path.
func TestSEC4_WorkspaceDelete_LeadingDashNameRejected(t *testing.T) {
	wsRoot := t.TempDir()
	setWorkspacesDir(t, wsRoot)

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "delete", "--", "--force-something") })
	if err == nil {
		t.Fatal(`delete "--force-something" must be rejected at entry (leading dash)`)
	}
}

// A valid workspace still deletes (devpod delete failure is tolerated with a
// warning; the local directory is still removed).
func TestV2a_WorkspaceDelete_ValidNameDeletes(t *testing.T) {
	wsRoot := t.TempDir()
	wsDir := filepath.Join(wsRoot, "myws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setWorkspacesDir(t, wsRoot)
	stubDeleteConfirm(t, true, nil)

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "delete", "myws") })
	if err != nil {
		t.Fatalf("valid delete returned error: %v", err)
	}
	if _, statErr := os.Stat(wsDir); !os.IsNotExist(statErr) {
		t.Fatalf("workspace dir still present after delete: %v", statErr)
	}
}

// Declining the confirmation exits 0, says Aborted and removes nothing.
func TestV2a_WorkspaceDelete_DeclineExits0(t *testing.T) {
	wsRoot := t.TempDir()
	wsDir := filepath.Join(wsRoot, "myws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setWorkspacesDir(t, wsRoot)
	stubDeleteConfirm(t, false, nil)

	var err error
	stdout, stderr := captureStd(t, func() { _, _, err = execCapture(t, "delete", "myws") })
	if err != nil {
		t.Fatalf("declining delete must exit 0; got %v", err)
	}
	if stdout != "" || stderr != "Aborted\n" {
		t.Errorf("a decline printed stdout %q, stderr %q; want only Aborted on stderr", stdout, stderr)
	}
	if _, statErr := os.Stat(wsDir); statErr != nil {
		t.Fatalf("workspace removed despite decline: %v", statErr)
	}
}

// A prompt that cannot ask makes ws delete return the Problem that names the
// stream and the step with --force — through the alias, the command's name —
// with nothing printed by the command and nothing removed. A cancelled
// prompt is returned as it is, for the root to die of its signal, with
// nothing printed either.
func TestWorkspaceDeletePromptRefusals(t *testing.T) {
	wsRoot := t.TempDir()
	wsDir := filepath.Join(wsRoot, "myws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setWorkspacesDir(t, wsRoot)

	resetFlags(t, deleteCmd)
	stubDeleteConfirm(t, false, errNoTerminal)
	var err error
	stdout, stderr := captureStd(t, func() { _, _, err = execCapture(t, "rm", "myws") })
	if !errors.Is(err, output.ErrNoTTY) {
		t.Errorf("errors.Is(%v, ErrNoTTY) = false; want true", err)
	}
	want := output.Problem{
		Title: "Cannot ask for confirmation: no terminal",
		Cause: "stdin is not a terminal",
		Steps: []output.Remedy{{Label: "Skip the prompt", Words: []string{"ws", "delete", "--force", "myws"}}},
	}
	if p, ok := output.ProblemOf(err); !ok || !reflect.DeepEqual(p, want) {
		t.Errorf("no terminal: the Problem is %+v, %t; want %+v", p, ok, want)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("no terminal: the command printed stdout %q, stderr %q; want nothing", stdout, stderr)
	}

	resetFlags(t, deleteCmd)
	stubDeleteConfirm(t, false, &output.Cancelled{Signal: syscall.SIGTERM})
	stdout, stderr = captureStd(t, func() { _, _, err = execCapture(t, "delete", "myws") })
	var cancelled *output.Cancelled
	if !errors.As(err, &cancelled) || cancelled.Signal != syscall.SIGTERM {
		t.Errorf("cancelled: the command returned %v; want the SIGTERM cancellation", err)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("cancelled: the command printed stdout %q, stderr %q; want nothing", stdout, stderr)
	}

	if _, statErr := os.Stat(wsDir); statErr != nil {
		t.Fatalf("workspace removed by a prompt that did not answer: %v", statErr)
	}
}

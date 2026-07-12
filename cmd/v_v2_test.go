package cmd

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// quietStd silences both os.Stdout and os.Stderr for the duration of fn. The
// delete path renders a spinner and status lines (spinner + output.* write to
// os.Stderr; the spinner success line reaches os.Stdout via fmt.Println) that
// would otherwise leak raw into test output. Both streams are drained
// concurrently so a chatty fn cannot fill a pipe buffer and block.
func quietStd(t *testing.T, fn func()) {
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
	os.Stdout, os.Stderr = wOut, wErr
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(io.Discard, rOut) }()
	go func() { defer wg.Done(); _, _ = io.Copy(io.Discard, rErr) }()
	fn()
	os.Stdout, os.Stderr = origOut, origErr
	_ = wOut.Close()
	_ = wErr.Close()
	wg.Wait()
	_ = rOut.Close()
	_ = rErr.Close()
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

	orig := confirmDestructiveFn
	confirmDestructiveFn = func(_ bool, _, _ string) bool { return true }
	t.Cleanup(func() { confirmDestructiveFn = orig })

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "delete", "myws") })
	if err != nil {
		t.Fatalf("valid delete returned error: %v", err)
	}
	if _, statErr := os.Stat(wsDir); !os.IsNotExist(statErr) {
		t.Fatalf("workspace dir still present after delete: %v", statErr)
	}
}

// Declining the confirmation exits 0 and removes nothing.
func TestV2a_WorkspaceDelete_DeclineExits0(t *testing.T) {
	wsRoot := t.TempDir()
	wsDir := filepath.Join(wsRoot, "myws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setWorkspacesDir(t, wsRoot)

	orig := confirmDestructiveFn
	confirmDestructiveFn = func(_ bool, _, _ string) bool { return false }
	t.Cleanup(func() { confirmDestructiveFn = orig })

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "delete", "myws") })
	if err != nil {
		t.Fatalf("declining delete must exit 0; got %v", err)
	}
	if _, statErr := os.Stat(wsDir); statErr != nil {
		t.Fatalf("workspace removed despite decline: %v", statErr)
	}
}

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/output"
)

func setProfilesDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PROFILES_DIR", dir)
}

// stubProfileDeleteConfirm makes ws profile-delete's prompt answer ok and err.
func stubProfileDeleteConfirm(t *testing.T, ok bool, err error) {
	t.Helper()
	orig := profileDeleteConfirmFn
	profileDeleteConfirmFn = func(_ bool, _, _ string) (bool, error) { return ok, err }
	t.Cleanup(func() { profileDeleteConfirmFn = orig })
}

// Empty name must be rejected before RemoveAll wipes the profiles root.
func TestSEC4_ProfileDeleteCmd_EmptyNameRejected(t *testing.T) {
	profilesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(profilesDir, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	setProfilesDir(t, profilesDir)

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "profile-delete", "--", "") })
	if err == nil {
		t.Fatal(`profile-delete "" must return an error`)
	}
	if _, statErr := os.Stat(filepath.Join(profilesDir, "keep")); statErr != nil {
		t.Fatalf("profiles root wiped by empty-name profile-delete: %v", statErr)
	}
}

// Traversal name rejected before reaching a sibling.
func TestSEC4_ProfileDeleteCmd_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	setProfilesDir(t, profilesDir)

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "profile-delete", "--", "../victim") })
	if err == nil {
		t.Fatal(`profile-delete "../victim" must return an error`)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("sibling removed via traversal profile-delete: %v", statErr)
	}
}

// Built-in profile still refused.
func TestSEC4_ProfileDeleteCmd_BuiltinRefused(t *testing.T) {
	setProfilesDir(t, t.TempDir())
	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "profile-delete", "default") })
	if err == nil {
		t.Fatal(`profile-delete "default" must refuse a built-in`)
	}
}

// A valid custom profile deletes after confirmation.
func TestV2b_ProfileDeleteCmd_ConfirmDeletes(t *testing.T) {
	profilesDir := t.TempDir()
	dir := filepath.Join(profilesDir, "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setProfilesDir(t, profilesDir)
	stubProfileDeleteConfirm(t, true, nil)

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "profile-delete", "mine") })
	if err != nil {
		t.Fatalf("valid profile-delete returned error: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("profile still present after delete: %v", statErr)
	}
}

// Declining exits 0, says Aborted and removes nothing.
func TestV2b_ProfileDeleteCmd_DeclineExits0(t *testing.T) {
	profilesDir := t.TempDir()
	dir := filepath.Join(profilesDir, "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setProfilesDir(t, profilesDir)
	stubProfileDeleteConfirm(t, false, nil)

	var err error
	stdout, stderr := captureStd(t, func() { _, _, err = execCapture(t, "profile-delete", "mine") })
	if err != nil {
		t.Fatalf("declining profile-delete must exit 0; got %v", err)
	}
	if stdout != "" || stderr != "Aborted\n" {
		t.Errorf("a decline printed stdout %q, stderr %q; want only Aborted on stderr", stdout, stderr)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("profile removed despite decline: %v", statErr)
	}
}

// A prompt that cannot ask makes ws profile-delete return the Problem that
// names the stream and the step with --force, with nothing printed by the
// command and nothing removed. A cancelled prompt is returned as it is, for
// the root to die of its signal, with nothing printed either.
func TestProfileDeletePromptRefusals(t *testing.T) {
	profilesDir := t.TempDir()
	dir := filepath.Join(profilesDir, "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setProfilesDir(t, profilesDir)

	resetFlags(t, profileDeleteCmd)
	stubProfileDeleteConfirm(t, false, errNoTerminal)
	var err error
	stdout, stderr := captureStd(t, func() { _, _, err = execCapture(t, "profile-delete", "mine") })
	if !errors.Is(err, output.ErrNoTTY) {
		t.Errorf("errors.Is(%v, ErrNoTTY) = false; want true", err)
	}
	want := output.Problem{
		Title: "Cannot ask for confirmation: no terminal",
		Cause: "stdin is not a terminal",
		Steps: []output.Remedy{{Label: "Skip the prompt", Words: []string{"ws", "profile-delete", "--force", "mine"}}},
	}
	if p, ok := output.ProblemOf(err); !ok || !reflect.DeepEqual(p, want) {
		t.Errorf("no terminal: the Problem is %+v, %t; want %+v", p, ok, want)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("no terminal: the command printed stdout %q, stderr %q; want nothing", stdout, stderr)
	}

	resetFlags(t, profileDeleteCmd)
	stubProfileDeleteConfirm(t, false, &output.Cancelled{Signal: syscall.SIGINT})
	stdout, stderr = captureStd(t, func() { _, _, err = execCapture(t, "profile-delete", "mine") })
	var cancelled *output.Cancelled
	if !errors.As(err, &cancelled) || cancelled.Signal != syscall.SIGINT {
		t.Errorf("cancelled: the command returned %v; want the SIGINT cancellation", err)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("cancelled: the command printed stdout %q, stderr %q; want nothing", stdout, stderr)
	}

	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("profile removed by a prompt that did not answer: %v", statErr)
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func setProfilesDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PROFILES_DIR", dir)
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

	orig := confirmDestructiveFn
	confirmDestructiveFn = func(_ bool, _, _ string) bool { return true }
	t.Cleanup(func() { confirmDestructiveFn = orig })

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "profile-delete", "mine") })
	if err != nil {
		t.Fatalf("valid profile-delete returned error: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("profile still present after delete: %v", statErr)
	}
}

// Declining exits 0 and removes nothing.
func TestV2b_ProfileDeleteCmd_DeclineExits0(t *testing.T) {
	profilesDir := t.TempDir()
	dir := filepath.Join(profilesDir, "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setProfilesDir(t, profilesDir)

	orig := confirmDestructiveFn
	confirmDestructiveFn = func(_ bool, _, _ string) bool { return false }
	t.Cleanup(func() { confirmDestructiveFn = orig })

	var err error
	quietStd(t, func() { _, _, err = execCapture(t, "profile-delete", "mine") })
	if err != nil {
		t.Fatalf("declining profile-delete must exit 0; got %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("profile removed despite decline: %v", statErr)
	}
}

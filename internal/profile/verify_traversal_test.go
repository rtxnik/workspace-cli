package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// A traversal name must be rejected before any recursive removal, so a sibling
// of the profiles directory can never be reached.
func TestSEC4_ProfileDelete_TraversalNameRejected(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ProfilesDir: profilesDir}

	if err := Delete(cfg, "../victim"); err == nil {
		t.Fatal(`Delete("../victim") returned nil; a traversal name must be rejected`)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("sibling directory removed via traversal delete: %v", err)
	}
}

// An empty name joins to the profiles directory itself; Delete must reject it
// instead of recursively removing every profile.
func TestSEC4_ProfileDelete_EmptyNameRejected(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(filepath.Join(profilesDir, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ProfilesDir: profilesDir}

	if err := Delete(cfg, ""); err == nil {
		t.Fatal(`Delete("") returned nil; an empty name must be rejected`)
	}
	if _, err := os.Stat(filepath.Join(profilesDir, "keep")); err != nil {
		t.Fatalf("profiles directory wiped by empty-name delete: %v", err)
	}
}

// A valid custom profile still deletes cleanly.
func TestV2b_ProfileDelete_ValidNameDeletes(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	custom := filepath.Join(profilesDir, "mine")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ProfilesDir: profilesDir}

	if err := Delete(cfg, "mine"); err != nil {
		t.Fatalf(`Delete("mine") failed on a valid custom profile: %v`, err)
	}
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Fatalf("custom profile still present after delete: %v", err)
	}
}

// A built-in profile is still refused (guard preserved).
func TestV2b_ProfileDelete_BuiltinRefused(t *testing.T) {
	cfg := config.Config{ProfilesDir: t.TempDir()}
	if err := Delete(cfg, "default"); err == nil {
		t.Fatal(`Delete("default") must refuse a built-in profile`)
	}
}

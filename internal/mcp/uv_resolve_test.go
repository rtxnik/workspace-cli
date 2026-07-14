package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func makeExecFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantResolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolveUV_RejectsWorldWritableDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	decoy := makeExecFile(t, dir, "uv")
	orig := lookPathFn
	lookPathFn = func(string) (string, error) { return decoy, nil }
	t.Cleanup(func() { lookPathFn = orig })
	if _, err := resolveUV(Options{}); err == nil {
		t.Fatal("expected rejection of a uv in a world-writable directory, got nil")
	}
}

func TestResolveUV_AcceptsUserOwnedDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	uv := makeExecFile(t, dir, "uv")
	orig := lookPathFn
	lookPathFn = func(string) (string, error) { return uv, nil }
	t.Cleanup(func() { lookPathFn = orig })
	got, err := resolveUV(Options{})
	if err != nil {
		t.Fatalf("expected acceptance of a user-owned uv, got %v", err)
	}
	if want := wantResolved(t, uv); got != want {
		t.Fatalf("resolveUV = %q, want %q", got, want)
	}
}

func TestResolveUV_HonorsExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	uv := makeExecFile(t, dir, "uv-custom")
	orig := lookPathFn
	lookPathFn = func(string) (string, error) { t.Fatal("lookPathFn called despite override"); return "", nil }
	t.Cleanup(func() { lookPathFn = orig })
	got, err := resolveUV(Options{UVPath: uv})
	if err != nil {
		t.Fatalf("override rejected: %v", err)
	}
	if want := wantResolved(t, uv); got != want {
		t.Fatalf("resolveUV = %q, want %q", got, want)
	}
}

func TestResolveUV_RejectsNonRegularFinalPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	notRegular := filepath.Join(dir, "uv")
	if err := os.Mkdir(notRegular, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := lookPathFn
	lookPathFn = func(string) (string, error) { return notRegular, nil }
	t.Cleanup(func() { lookPathFn = orig })
	if _, err := resolveUV(Options{}); err == nil {
		t.Fatal("expected rejection of a non-regular uv path, got nil")
	}
}

func TestResolveUV_RejectsSymlinkToWorldWritableTarget(t *testing.T) {
	base := t.TempDir()
	evil := filepath.Join(base, "evil")
	if err := os.Mkdir(evil, 0o777); err != nil {
		t.Fatal(err)
	}
	// Chmod after Mkdir so the directory is truly world-writable regardless of
	// the process umask (Mkdir's mode is masked; Chmod's is not).
	if err := os.Chmod(evil, 0o777); err != nil {
		t.Fatal(err)
	}
	target := makeExecFile(t, evil, "uv")
	safe := filepath.Join(base, "safe")
	if err := os.Mkdir(safe, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(safe, "uv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	orig := lookPathFn
	lookPathFn = func(string) (string, error) { return link, nil }
	t.Cleanup(func() { lookPathFn = orig })
	if _, err := resolveUV(Options{}); err == nil {
		t.Fatal("expected rejection of a symlink whose target dir is world-writable, got nil")
	}
}

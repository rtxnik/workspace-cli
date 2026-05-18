package mcp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewTokenPipeWritesBytesAndClosesWriter verifies the fd-3 token-pipe
// helper: NewTokenPipe spawns a goroutine that writes the token bytes onto the
// writer end of an os.Pipe and closes the writer so the reader EOFs cleanly
// once the child has drained the bytes. This is the core of path-A token
// transport per CONTEXT D-09 + RESEARCH §Pattern 1 L186-192.
func TestNewTokenPipeWritesBytesAndClosesWriter(t *testing.T) {
	const want = "hello-token-bytes"
	tokenR, err := NewTokenPipe(want)
	if err != nil {
		t.Fatalf("NewTokenPipe: %v", err)
	}
	t.Cleanup(func() {
		_ = tokenR.Close()
	})

	got, err := io.ReadAll(tokenR)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if string(got) != want {
		t.Fatalf("token bytes mismatch: got %q want %q", string(got), want)
	}
}

// TestCheckUVPathRejectsNonRegular asserts the Pitfall 5 (golang/go #66654)
// guard: ExtraFiles[0] combined with /proc/self/fd/<exe> can collide when the
// uv binary lives at a non-regular path (symlink target swapped, directory
// path, etc.). CheckUVPath stat()s the path and rejects anything that is not
// a regular file, citing the "fd-3 collision risk" string the auth-rail
// integration test scans for.
func TestCheckUVPathRejectsNonRegular(t *testing.T) {
	// Directory is the simplest non-regular file to hand the check.
	dir := t.TempDir()
	err := CheckUVPath(dir)
	if err == nil {
		t.Fatalf("CheckUVPath(%q) returned nil; want error citing fd-3 collision risk", dir)
	}
	if !strings.Contains(err.Error(), "fd-3 collision risk") {
		t.Errorf("CheckUVPath error missing 'fd-3 collision risk' string; got %v", err)
	}
}

// TestCheckUVPathAcceptsRegular asserts the positive path: a real regular
// file at a known path passes the guard (no error).
func TestCheckUVPathAcceptsRegular(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "uv-fake")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake uv: %v", err)
	}
	if err := CheckUVPath(bin); err != nil {
		t.Errorf("CheckUVPath(%q) on regular file: %v", bin, err)
	}
}

// TestCheckUVPathMissingReturnsError asserts that a path that does not exist
// at all is also rejected (covers the missing-binary edge case so the operator
// sees a useful error rather than a stat() ENOENT surfacing from deep inside
// exec.Cmd).
func TestCheckUVPathMissingReturnsError(t *testing.T) {
	err := CheckUVPath("/definitely/does/not/exist/uv")
	if err == nil {
		t.Fatalf("CheckUVPath on missing path returned nil; want error")
	}
}

// TestErrMissingDependencyIsExported asserts the package-level sentinel
// ErrMissingDependency is available so client.go can wrap and callers can
// errors.Is() against it (e.g. cobra leaves mapping the auth-gate to exit 4).
func TestErrMissingDependencyIsExported(t *testing.T) {
	if ErrMissingDependency == nil {
		t.Fatal("ErrMissingDependency must be a non-nil package-level error")
	}
	wrapped := errMissingDepWrap("VAULT_AI_TOKEN unset")
	if !errors.Is(wrapped, ErrMissingDependency) {
		t.Errorf("wrapped error not errors.Is(ErrMissingDependency); got %v", wrapped)
	}
}

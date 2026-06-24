package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestWriteFileCreatesWithPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	want := []byte(`{"k":"v"}`)
	if err := WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", info.Mode().Perm())
	}
	// Only the target remains — no temp remnant.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1: %v", len(entries), names(entries))
	}
}

func TestWriteFileOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("OLD-CONTENT"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFile(path, []byte("NEW"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "NEW" {
		t.Errorf("content = %q, want NEW", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (no temp remnant): %v", len(entries), names(entries))
	}
}

func TestWriteFileFailureLeavesOriginalIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only dir is not enforced for root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Read-only dir: creating the temp file (or renaming) must fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := WriteFile(path, []byte("NEW"), 0o600); err == nil {
		t.Fatal("WriteFile succeeded on read-only dir; want error")
	}

	_ = os.Chmod(dir, 0o700) // restore to inspect
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("read original: %v", rerr)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("original corrupted: content = %q, want ORIGINAL", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (no temp remnant): %v", len(entries), names(entries))
	}
}

func TestWriteFileTempColocated(t *testing.T) {
	// A successful write into a nested dir proves the temp was created and
	// renamed within the target's own directory (same filesystem; no EXDEV).
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := WriteFile(path, []byte("X"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Errorf("unexpected entries in target dir: %v", names(entries))
	}
}

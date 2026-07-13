package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestV1_WriteFileReplacesSymlinkWithRegularFile documents the sharp edge that
// motivates resolving a config path one layer up before writing: WriteFile is a
// generic atomic writer, so aimed at a symlink PATH it renames a regular temp
// file over that path — replacing the symlink with a regular file. It does not
// write THROUGH the link. Callers that must keep an active-profile symlink
// intact resolve the path first (xrayconf.ResolveConfigTarget) and hand the
// resolved target to WriteFile. This test characterizes; it does not forbid.
func TestV1_WriteFileReplacesSymlinkWithRegularFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{"v":"old"}`), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink("target.json", link); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	if err := WriteFile(link, []byte(`{"v":"new"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected the symlink to be replaced by a regular file (WriteFile writes AT the path, not THROUGH it)")
	}
	if got, _ := os.ReadFile(target); string(got) != `{"v":"old"}` {
		t.Errorf("original target should be untouched, got %q", got)
	}
}

// Package fsutil provides atomic filesystem primitives (file write, symlink
// swap) shared across the codebase. It lives in its own leaf package with no
// internal imports so any consumer — xray, xrayconf, workspace, cmd — can use
// the same temp-then-rename discipline without import cycles.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// WriteFile atomically writes data to path with the given permission bits.
//
// It writes to a temp file in the SAME directory as path, fsyncs it, then
// os.Rename's it over path. On POSIX a same-filesystem rename is atomic: a
// concurrent reader observes either the previous content or the new content,
// never a torn or empty file. A crash mid-write orphans the temp file and
// leaves the target untouched.
//
// The explicit Chmod after create defends the perm contract against umask,
// which would otherwise mask the O_CREATE mode bits. Mirrors the temp-then-
// rename discipline of AtomicSymlink in this package (never remove-then-create).
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp := filepath.Join(dir, "."+base+".tmp."+strconv.FormatInt(time.Now().UnixNano(), 10))

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

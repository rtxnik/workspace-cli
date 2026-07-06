package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// symlinkTempSeq makes AtomicSymlink temp names unique per call. A plain
// timestamp collides when concurrent writers to the same linkPath read the
// same nanosecond; the monotonic counter breaks that tie deterministically.
var symlinkTempSeq atomic.Uint64

// AtomicSymlink replaces linkPath with a symlink to target atomically.
// Linux: create a temp symlink in the same directory then os.Rename it
// over linkPath. Observers see either the old target or the new target,
// never an in-between "missing" state.
//
// D-04 enforcement: NEVER shell out to `ln -sfn`; NEVER use a non-atomic
// remove-then-create sequence (opens a window where the symlink is missing).
func AtomicSymlink(target, linkPath string) error {
	dir := filepath.Dir(linkPath)
	base := filepath.Base(linkPath)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d.%d", base, time.Now().UnixNano(), symlinkTempSeq.Add(1)))
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("create temp symlink: %w", err)
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

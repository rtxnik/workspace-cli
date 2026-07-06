package fsutil

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestAtomicSwapSymlink asserts that observers of linkPath never see a
// missing or in-between state under concurrent AtomicSymlink writers and
// concurrent os.Readlink readers — the D-04 atomicity guarantee.
func TestAtomicSwapSymlink(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, "config.json")
	targetA := filepath.Join("profiles", "primary.json")
	targetB := filepath.Join("profiles", "backup.json")

	// Initial state: link -> targetA.
	if err := os.Symlink(targetA, linkPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 200 readers — every read MUST yield targetA or targetB; never empty,
	// never an error, never a third target value.
	wg.Add(200)
	for i := 0; i < 200; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				target, err := os.Readlink(linkPath)
				if err != nil {
					t.Errorf("Readlink failed: %v (no in-between state allowed)", err)
					return
				}
				if target != targetA && target != targetB {
					t.Errorf("Readlink returned unexpected target %q (must be A or B)", target)
					return
				}
			}
		}()
	}

	// 100 writers — alternating A and B.
	wg.Add(100)
	for i := 0; i < 100; i++ {
		i := i
		go func() {
			defer wg.Done()
			tgt := targetA
			if i%2 == 0 {
				tgt = targetB
			}
			if err := AtomicSymlink(tgt, linkPath); err != nil {
				t.Errorf("AtomicSymlink(%q): %v", tgt, err)
			}
		}()
	}

	// Give the readers time to run alongside the writers, then stop.
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestAtomicSymlinkConcurrentTempUnique stresses the temp-name uniqueness
// guarantee: many writers released simultaneously against the SAME linkPath
// must never collide on the intermediate temp symlink. A timestamp-only temp
// name lets two writers that read the same nanosecond generate the same path,
// so the second os.Symlink fails EEXIST. The per-call atomic counter must make
// every temp name unique regardless of clock resolution.
func TestAtomicSymlinkConcurrentTempUnique(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, "config.json")
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), linkPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	const writers = 500
	var wg sync.WaitGroup
	release := make(chan struct{})
	errs := make(chan error, writers)
	target := filepath.Join("profiles", "backup.json")
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release // barrier: fire all writers together to maximize collision pressure
			if err := AtomicSymlink(target, linkPath); err != nil {
				errs <- err
			}
		}()
	}
	close(release)
	wg.Wait()
	close(errs)

	collisions := 0
	var sample error
	for err := range errs {
		collisions++
		sample = err
	}
	if collisions > 0 {
		t.Fatalf("%d/%d concurrent AtomicSymlink calls collided on the temp name; sample: %v", collisions, writers, sample)
	}
}

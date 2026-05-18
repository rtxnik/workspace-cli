package cmd

// vault_backup_verify_test.go — unit tests for CLI-06 `ws vault backup-verify`.
//
// Coverage (per Plan 18-04 Task 2 behavior block):
//   - TestVaultBackupVerifyMissingLog — glob empty → exit 4 with Phase 21c diagnostic
//   - TestVaultBackupVerifyStaleLog — latest mtime 15d old → exit 4 with log-stale diagnostic
//   - TestVaultBackupVerifyGreenLog — latest line outcome "green" → exit 0
//   - TestVaultBackupVerifyRotLog — latest line outcome "rot" → exit 1
//   - TestVaultBackupVerifyRegistered — walker finds "backup-verify" under vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBackupVerifyFS swaps in a test-controlled in-memory filesystem for the
// backup-verify leaf. Returned by setupFakeBackupVerifyFS in each test.
type fakeBackupVerifyFS struct {
	globResult []string
	statResult map[string]os.FileInfo
	readResult map[string][]byte
}

type fakeFileInfo struct {
	name    string
	modTime time.Time
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

// installFakeBackupVerifyFS overrides the three filesystem seams and returns
// a cleanup func.
func installFakeBackupVerifyFS(fs fakeBackupVerifyFS) func() {
	origGlob := backupVerifyGlobFn
	origStat := backupVerifyStatFn
	origRead := backupVerifyReadFn
	backupVerifyGlobFn = func(_ string) ([]string, error) { return fs.globResult, nil }
	backupVerifyStatFn = func(name string) (os.FileInfo, error) {
		if fi, ok := fs.statResult[name]; ok {
			return fi, nil
		}
		return nil, errors.New("fake stat: not found")
	}
	backupVerifyReadFn = func(name string) ([]byte, error) {
		if body, ok := fs.readResult[name]; ok {
			return body, nil
		}
		return nil, errors.New("fake read: not found")
	}
	return func() {
		backupVerifyGlobFn = origGlob
		backupVerifyStatFn = origStat
		backupVerifyReadFn = origRead
	}
}

func TestVaultBackupVerifyMissingLog(t *testing.T) {
	cleanup := installFakeBackupVerifyFS(fakeBackupVerifyFS{globResult: nil})
	t.Cleanup(cleanup)

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "backup-verify"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on missing log")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 4 {
		t.Errorf("expected exit 4 (MISSING_DEPENDENCY); got %d", cerr.code)
	}
	if !strings.Contains(cerr.msg, "Phase 21c") {
		t.Errorf("expected error msg to cite Phase 21c; got %q", cerr.msg)
	}
}

func TestVaultBackupVerifyStaleLog(t *testing.T) {
	path := "/fake/log/backup-verify-2026-W18.jsonl"
	cleanup := installFakeBackupVerifyFS(fakeBackupVerifyFS{
		globResult: []string{path},
		statResult: map[string]os.FileInfo{
			path: fakeFileInfo{name: filepath.Base(path), modTime: time.Now().AddDate(0, 0, -15)},
		},
	})
	t.Cleanup(cleanup)

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "backup-verify"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on stale log")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 4 {
		t.Errorf("expected exit 4 on stale log; got %d", cerr.code)
	}
	if !strings.Contains(strings.ToLower(cerr.msg), "stale") {
		t.Errorf("expected 'stale' in msg; got %q", cerr.msg)
	}
}

func TestVaultBackupVerifyGreenLog(t *testing.T) {
	path := "/fake/log/backup-verify-2026-W20.jsonl"
	body := []byte(`{"ts":"2026-05-18T03:00:00Z","outcome":"green","details":"all 1234 notes verified"}` + "\n")
	cleanup := installFakeBackupVerifyFS(fakeBackupVerifyFS{
		globResult: []string{path},
		statResult: map[string]os.FileInfo{
			path: fakeFileInfo{name: filepath.Base(path), modTime: time.Now().Add(-2 * time.Hour)},
		},
		readResult: map[string][]byte{path: body},
	})
	t.Cleanup(cleanup)

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "backup-verify"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error on green log: %v (stderr=%q)", err, errOut.String())
	}
}

func TestVaultBackupVerifyRotLog(t *testing.T) {
	path := "/fake/log/backup-verify-2026-W20.jsonl"
	// Two lines — leaf MUST read the LAST line.
	body := []byte(
		`{"ts":"2026-05-17T03:00:00Z","outcome":"green","details":"prior good"}` + "\n" +
			`{"ts":"2026-05-18T03:00:00Z","outcome":"rot","details":"chunk hash mismatch on 4 notes"}` + "\n",
	)
	cleanup := installFakeBackupVerifyFS(fakeBackupVerifyFS{
		globResult: []string{path},
		statResult: map[string]os.FileInfo{
			path: fakeFileInfo{name: filepath.Base(path), modTime: time.Now().Add(-1 * time.Hour)},
		},
		readResult: map[string][]byte{path: body},
	})
	t.Cleanup(cleanup)

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "backup-verify"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on rot outcome")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 1 {
		t.Errorf("expected exit 1 on rot; got %d", cerr.code)
	}
	combined := cerr.msg + errOut.String()
	if !strings.Contains(combined, "rot") {
		t.Errorf("expected 'rot' in diagnostic output; got msg=%q stderr=%q", cerr.msg, errOut.String())
	}
}

func TestVaultBackupVerifyRegistered(t *testing.T) {
	if !findVaultLeaf(t, "backup-verify") {
		t.Fatal("`ws vault backup-verify` not registered as a subcommand of `ws vault`")
	}
}

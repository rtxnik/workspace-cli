package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/workspace"
)

// stubProbe returns a probeRepoFn replacement. Repos in overrides get
// canned values; the rest get a healthy default (exists, main, clean).
func stubProbe(overrides map[string]workspace.RepoStatus) func(string) workspace.RepoStatus {
	return func(path string) workspace.RepoStatus {
		name := filepath.Base(path)
		if rs, ok := overrides[name]; ok {
			return rs
		}
		return workspace.RepoStatus{
			Name:   name,
			Path:   path,
			Exists: true,
			Branch: "main",
			Clean:  true,
		}
	}
}

func TestWorkspaceStatusAllClean(t *testing.T) {
	orig := probeRepoFn
	defer func() { probeRepoFn = orig }()
	probeRepoFn = stubProbe(nil)

	cmd := newWorkspaceStatusCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	wantInTable(t, buf.String(), "workspace-cli", stateText(output.StateOK, "clean"), "3/3 repos healthy")
	if n := strings.Count(buf.String(), stateText(output.StateOK, "clean")); n != 3 {
		t.Errorf("%d repos read clean; want 3:\n%s", n, buf.String())
	}
}

// stateText is a state cell as stdout renders it: mark and word, in the glyph
// mode this process resolved for stdout.
func stateText(st output.State, word string) string { return output.Out().StateText(st, word) }

// wantInTable fails unless out, the table ws status wrote to its stdout,
// holds every one of want — the caption included, which travels with it.
func wantInTable(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
}

func TestWorkspaceStatusDirtyRepo(t *testing.T) {
	orig := probeRepoFn
	defer func() { probeRepoFn = orig }()
	probeRepoFn = stubProbe(map[string]workspace.RepoStatus{
		"vault-ai": {
			Name:   "vault-ai",
			Path:   "/tmp/vault-ai",
			Exists: true,
			Branch: "main",
			Clean:  false,
		},
	})

	cmd := newWorkspaceStatusCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	var cliErr *cliErrorWithExit
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected cliErrorWithExit, got: %v", err)
	}
	if cliErr.code != 1 {
		t.Errorf("expected exit code 1, got %d", cliErr.code)
	}
	wantInTable(t, buf.String(), stateText(output.StateBusy, "dirty"), "2/3 repos healthy")
}

func TestWorkspaceStatusMissingRepo(t *testing.T) {
	orig := probeRepoFn
	defer func() { probeRepoFn = orig }()
	probeRepoFn = stubProbe(map[string]workspace.RepoStatus{
		"dotfiles": {
			Name:  "dotfiles",
			Path:  "/tmp/dotfiles",
			Error: "not a git repository",
		},
	})

	cmd := newWorkspaceStatusCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	var cliErr *cliErrorWithExit
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected cliErrorWithExit, got: %v", err)
	}
	wantInTable(t, buf.String(), stateText(output.StateFail, "missing"), "2/3 repos healthy")
}

func TestWorkspaceStatusProbeError(t *testing.T) {
	orig := probeRepoFn
	defer func() { probeRepoFn = orig }()
	probeRepoFn = stubProbe(map[string]workspace.RepoStatus{
		"vault-ai": {Name: "vault-ai", Path: "/tmp/vault-ai", Exists: true, Error: "fatal: bad object HEAD"},
	})

	cmd := newWorkspaceStatusCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	var cliErr *cliErrorWithExit
	if err := cmd.Execute(); !errors.As(err, &cliErr) || cliErr.code != 1 {
		t.Fatalf("expected cliErrorWithExit with code 1, got: %v", err)
	}
	wantInTable(t, buf.String(), stateText(output.StateFail, "error"), "fatal: bad object HEAD", "2/3 repos healthy")
}

func TestWorkspaceStatusJSON(t *testing.T) {
	orig := probeRepoFn
	defer func() { probeRepoFn = orig }()
	probeRepoFn = stubProbe(nil)

	cmd := newWorkspaceStatusCmd()
	// --json is a persistent flag on rootCmd; since this command is not
	// attached to rootCmd in tests, register the flag locally so SetArgs
	// can parse it.
	cmd.Flags().Bool("json", false, "")
	cmd.SetArgs([]string{"--json"})
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var statuses []workspace.RepoStatus
	if err := json.Unmarshal(buf.Bytes(), &statuses); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(statuses) != 3 {
		t.Errorf("expected 3 repos, got %d", len(statuses))
	}
	for _, s := range statuses {
		if !s.Clean {
			t.Errorf("repo %s expected clean", s.Name)
		}
	}
}

func TestWorkspaceStatusOffMain(t *testing.T) {
	orig := probeRepoFn
	defer func() { probeRepoFn = orig }()
	probeRepoFn = stubProbe(map[string]workspace.RepoStatus{
		"workspace-cli": {
			Name:   "workspace-cli",
			Path:   "/tmp/workspace-cli",
			Exists: true,
			Branch: "feat/workspace-status",
			Clean:  true,
		},
	})

	cmd := newWorkspaceStatusCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	var cliErr *cliErrorWithExit
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected cliErrorWithExit, got: %v", err)
	}
	if cliErr.code != 1 {
		t.Errorf("expected exit code 1, got %d", cliErr.code)
	}
	wantInTable(t, buf.String(), "feat/workspace-status", "2/3 repos healthy")
}

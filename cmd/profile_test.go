package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/rtxnik/workspace-cli/internal/output"
)

// wizardStub counts the calls of ws profile-create's wizard seams.
type wizardStub struct{ forms, confirms int }

// stubWizard drives ws profile-create: the predicate answers ready, the
// form formErr, and the final question confirmed and confirmErr.
func stubWizard(t *testing.T, ready, formErr error, confirmed bool, confirmErr error) *wizardStub {
	t.Helper()
	s := &wizardStub{}
	origReady, origForm, origConfirm := promptReadyFn, wizardFormFn, wizardConfirmFn
	promptReadyFn = func() error { return ready }
	wizardFormFn = func(*huh.Form) error { s.forms++; return formErr }
	wizardConfirmFn = func(string, string) (bool, error) { s.confirms++; return confirmed, confirmErr }
	t.Cleanup(func() { promptReadyFn, wizardFormFn, wizardConfirmFn = origReady, origForm, origConfirm })
	return s
}

// Without a terminal to ask on, ws profile-create skips its wizard, not
// refuses: the profile is created from the flags, and the wizard's form and
// question are never run; --image skips it on a terminal too.
func TestProfileCreateWithoutATerminalSkipsTheWizard(t *testing.T) {
	for _, c := range []struct {
		name  string
		ready error
		args  []string
	}{
		{"no terminal", errNoTerminal, []string{"profile-create", "mine"}},
		{"--image on a terminal", nil, []string{"profile-create", "mine", "--image", "debian:bookworm-slim"}},
	} {
		profilesDir := t.TempDir()
		setProfilesDir(t, profilesDir)
		resetFlags(t, profileCreateCmd)
		s := stubWizard(t, c.ready, nil, true, nil)
		var err error
		_, stderr := captureStd(t, func() { _, _, err = execCapture(t, c.args...) })
		if err != nil || stderr != "✓ Profile \"mine\" created\n" {
			t.Errorf("%s: returned %v, stderr %q; want the profile created", c.name, err, stderr)
		}
		if _, statErr := os.Stat(filepath.Join(profilesDir, "mine")); statErr != nil {
			t.Errorf("%s: no profile: %v", c.name, statErr)
		}
		if s.forms != 0 || s.confirms != 0 {
			t.Errorf("%s: the wizard ran: %d form(s), %d question(s)", c.name, s.forms, s.confirms)
		}
	}
	resetFlags(t, profileCreateCmd)
}

// On a terminal, the wizard's outcomes: a form that does not answer —
// cancelled or failed — is returned as it is, nothing printed; after the
// summary, the operator's no prints Aborted and exits 0, and a cancelled
// question is returned as it is, the summary kept and nothing added to it.
// No outcome but a yes creates the profile.
func TestProfileCreateWizardOutcomes(t *testing.T) {
	failed := errors.New("prompt failed: raw mode refused")
	cancelled := &output.Cancelled{Signal: syscall.SIGINT}
	for _, c := range []struct {
		name       string
		formErr    error
		confirmed  bool
		confirmErr error
		wantErr    error
		stderr     func(string) bool
	}{
		{"form cancelled", cancelled, false, nil, cancelled, func(s string) bool { return s == "" }},
		{"form failed", failed, false, nil, failed, func(s string) bool { return s == "" }},
		{"the operator's no", nil, false, nil, nil, func(s string) bool {
			return strings.HasPrefix(s, "\nProfile summary\n") && strings.HasSuffix(s, "\n\nAborted\n")
		}},
		{"question cancelled", nil, false, cancelled, cancelled, func(s string) bool {
			return strings.HasPrefix(s, "\nProfile summary\n") && strings.HasSuffix(s, "  DinD   false\n\n")
		}},
	} {
		profilesDir := t.TempDir()
		setProfilesDir(t, profilesDir)
		resetFlags(t, profileCreateCmd)
		stubWizard(t, nil, c.formErr, c.confirmed, c.confirmErr)
		var err error
		stdout, stderr := captureStd(t, func() { _, _, err = execCapture(t, "profile-create", "mine") })
		if err != c.wantErr {
			t.Errorf("%s: returned %v; want %v", c.name, err, c.wantErr)
		}
		if stdout != "" || !c.stderr(stderr) {
			t.Errorf("%s: printed stdout %q, stderr %q", c.name, stdout, stderr)
		}
		if _, statErr := os.Stat(filepath.Join(profilesDir, "mine")); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s: the profile was created: %v", c.name, statErr)
		}
	}
	resetFlags(t, profileCreateCmd)
}

// TestProfileSummary pins the summary the profile-create wizard shows before
// its confirmation (phase-5 §3.10): the answers under their keys, aligned,
// Packages and Tools only when given.
func TestProfileSummary(t *testing.T) {
	errs := output.NewStreamAt(io.Discard, 80, false, output.ColourNone, false)
	for _, c := range []struct {
		name     string
		packages string
		tools    []string
		dind     bool
		want     string
	}{
		{"every answer", "curl git", []string{"go", "golangci-lint"}, false, "Profile summary\n" +
			"  Name      go-custom\n" +
			"  Image     mcr.microsoft.com/devcontainers/go:1.26\n" +
			"  Packages  curl git\n" +
			"  Tools     go, golangci-lint\n" +
			"  DinD      false"},
		{"no packages, no tools", "", nil, true, "Profile summary\n" +
			"  Name   go-custom\n" +
			"  Image  mcr.microsoft.com/devcontainers/go:1.26\n" +
			"  DinD   true"},
	} {
		got := profileSummary("go-custom", "mcr.microsoft.com/devcontainers/go:1.26", c.packages, c.tools, c.dind).Render(errs)
		if got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

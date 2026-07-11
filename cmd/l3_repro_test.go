package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

// quietStderr redirects os.Stderr for the duration of fn so a helper's
// warning line (e.g. MapErrorCodeToExitCode's XREPO-01 drift warning) never
// prints raw into passing-test output. Mirrors internal/mcp's test helper of
// the same name; duplicated here because that one is unexported and cmd is a
// separate package.
func quietStderr(t *testing.T, fn func()) {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
	})
	fn()
	os.Stderr = origStderr
	_ = w.Close()
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("drain stderr pipe: %v", err)
	}
	_ = r.Close()
}

// TestL3_07_VaultRenderResultTreatsOKFalseAsSuccess: a failure envelope
// without an error block ({"ok":false}) must surface as a non-zero-exit
// error, not render empty data and return nil (exit 0).
func TestL3_07_VaultRenderResultTreatsOKFalseAsSuccess(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := vaultRenderResult(cmd, "search", &mcp.Envelope{OK: false}, nil)
	if err == nil {
		t.Fatalf("ok=false envelope rendered as success; stdout=%q", out.String())
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T (%v); want *cliErrorWithExit", err, err)
	}
	if cerr.code == 0 {
		t.Fatalf("ok=false envelope mapped to exit code 0")
	}
}

// TestL3_06_VaultErrExitEmptyCodeYieldsExitZero: an envelope error with an
// empty code must not yield cliErrorWithExit{code:0}, which Execute() would
// pass to os.Exit(0) after printing the Error: line. Root cause shared with
// the envelope mapper's unknown-code guard; kept as the leaf-level
// regression guard.
func TestL3_06_VaultErrExitEmptyCodeYieldsExitZero(t *testing.T) {
	var err error
	quietStderr(t, func() {
		err = vaultErrExit("search", &mcp.EnvelopeError{Code: "", Message: "boom"})
	})
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T (%v); want *cliErrorWithExit", err, err)
	}
	if cerr.code == 0 {
		t.Fatalf("envelope FAILURE mapped to exit code 0 (msg=%q)", cerr.msg)
	}
}

// TestL3_07_IngestTreatsOKFalseAsSuccess: `ws vault ingest` has its own
// inline render tail (separate from vaultRenderResult) that must also fail
// closed on a failure envelope with no error block. Regression guard for the
// last unguarded envelope consumer on this branch.
func TestL3_07_IngestTreatsOKFalseAsSuccess(t *testing.T) {
	origCall := vaultIngestCallFn
	t.Cleanup(func() { vaultIngestCallFn = origCall })
	vaultIngestCallFn = func(_ context.Context, _ *cobra.Command, _ mcp.CreateNoteArgs) (*mcp.Envelope, error) {
		return &mcp.Envelope{OK: false}, nil
	}
	path := writeTempIngestFile(t, "")

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "ingest", path})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		resetVaultIngestFlags(t)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("ok=false envelope rendered as success; stdout=%q", out.String())
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T (%v); want *cliErrorWithExit", err, err)
	}
	if cerr.code == 0 {
		t.Fatalf("ok=false envelope mapped to exit code 0")
	}
}

// TestUpFailureDetail_RouteDegradedNamesWorkspaces: a *docker.RouteFixError
// from the up sequence renders as a degraded-but-running outcome (the proxy
// DID start) naming every failed workspace, with fix-routes as the
// remediation -- not as the generic "Failed to start proxy" screen.
func TestUpFailureDetail_RouteDegradedNamesWorkspaces(t *testing.T) {
	rep := docker.FixRoutesReport{Fixed: 1, Attempted: 2, Failures: []string{"my-workspace: exit status 1"}}
	d := upFailureDetail(rep.Err())
	if !strings.Contains(d.Title, "DEGRADED") {
		t.Errorf("degraded route-fix must not render as a start failure; got title %q", d.Title)
	}
	if !strings.Contains(d.Context["Failures"], "my-workspace") {
		t.Errorf("failing workspace must be named; got %v", d.Context)
	}
	if joined := strings.Join(d.Suggestions, " "); !strings.Contains(joined, "fix-routes") {
		t.Errorf("suggestions must include fix-routes; got %v", d.Suggestions)
	}
}

// TestUpFailureDetail_GenericFailureUnchanged: non-route errors keep the
// existing operator-facing rendering byte-for-byte.
func TestUpFailureDetail_GenericFailureUnchanged(t *testing.T) {
	d := upFailureDetail(errors.New("boom"))
	if d.Title != "Failed to start proxy" {
		t.Errorf("non-route errors must keep the existing title; got %q", d.Title)
	}
	if d.Context["Error"] != "boom" {
		t.Errorf("generic context must carry the error; got %v", d.Context)
	}
	if len(d.Suggestions) != 3 {
		t.Errorf("generic suggestions must be unchanged; got %v", d.Suggestions)
	}
}

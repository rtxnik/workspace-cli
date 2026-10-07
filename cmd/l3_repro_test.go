package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/rtxnik/workspace-cli/internal/output"
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
// empty code must not yield cliErrorWithExit{code:0}, for which run returns
// (msg, 0) and Execute prints msg as a Problem before exiting 0 —
// cobra itself prints nothing. Root cause shared with the envelope mapper's
// unknown-code guard; kept as the leaf-level regression guard.
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

// TestUpProblem_RouteDegradedNamesWorkspaces: a *docker.RouteFixError from
// the up sequence renders as a degraded-but-running outcome (the proxy DID
// start) with a fact for every failed workspace and fix-routes as the
// remediation -- not as the "Failed to start proxy" screen.
func TestUpProblem_RouteDegradedNamesWorkspaces(t *testing.T) {
	rep := docker.FixRoutesReport{Fixed: 1, Attempted: 3,
		Failures: []string{"my-workspace: exit status 1", "other: exec: no such container"}}
	err := upProblem(rep.Err())
	p, ok := output.ProblemOf(err)
	want := output.Problem{
		Title: "Proxy is up, but workspace routes are degraded (2 of 3 failed)",
		Facts: []output.Fact{{K: "my-workspace", V: "exit status 1"}, {K: "other", V: "exec: no such container"}},
		Steps: []output.Remedy{{Label: "Retry", Cmd: "ws proxy fix-routes"}, {Label: "Diagnose", Cmd: "ws proxy doctor"}},
	}
	if !ok || !reflect.DeepEqual(p, want) {
		t.Errorf("the root would print %+v; want %+v", p, want)
	}
	var rf *docker.RouteFixError
	if !errors.As(err, &rf) {
		t.Error("the route-fix error is hidden behind the Problem")
	}
}

// TestUpProblem_StartFailureCarriesTheCause: any other failure is a start
// failure whose cause is the error, then the failed step's last lines.
func TestUpProblem_StartFailureCarriesTheCause(t *testing.T) {
	steps := []output.Remedy{
		{Label: "Check config", Cmd: "ws proxy check"},
		{Label: "Initialize config", Cmd: "ws proxy init <proxy-uri>"},
		{Label: "Rebuild image", Cmd: "ws proxy rebuild"},
	}
	for _, c := range []struct {
		name  string
		err   error
		cause string
	}{
		{"a plain error", errors.New("boom"), "boom"},
		{"a failed step with a tail", &output.TaskError{Title: "Starting proxy", Err: errors.New("boom"),
			Tail: []string{"pulling image", "denied"}}, "boom\npulling image\ndenied"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, ok := output.ProblemOf(upProblem(c.err))
			want := output.Problem{Title: "Failed to start proxy", Cause: c.cause, Steps: steps}
			if !ok || !reflect.DeepEqual(p, want) {
				t.Errorf("the root would print %+v; want %+v", p, want)
			}
		})
	}
}

// TestProxyUpReturnsItsProblem: a failed ws proxy up returns the error that
// carries its Problem and leaves the printing to the root. In a pipe the
// bytes are the same whoever prints them; the returned error is not.
func TestProxyUpReturnsItsProblem(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/ws-test/docker.sock")
	err := proxyUpCmd.RunE(proxyUpCmd, nil)
	if p, ok := output.ProblemOf(err); !ok || p.Title != "Failed to start proxy" {
		t.Errorf("ws proxy up returned %v; want the error that carries its Problem, for the root to print", err)
	}
}

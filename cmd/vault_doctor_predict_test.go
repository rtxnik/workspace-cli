package cmd

// vault_doctor_predict_test.go -- unit tests for `ws vault doctor predict-bulk-load`.
//
// 5 test cases (Plan 23-07 Task 2):
//   1. TestPredictRegistered           -- walker finds predict-bulk-load under vault
//   2. TestPredictTableOutput          -- default table rendering with mocked response
//   3. TestPredictJSONOutput           -- --json flag outputs raw JSON
//   4. TestPredictReadOnly             -- no state mutations
//   5. TestPredictMCPError             -- graceful error when MCP call fails
//
// All tests inject mocked predictMCPCallFn via the package-level seam
// so no live MCP subprocess is spawned at unit test time.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/pflag"
)

// installPredictMocks installs a mocked predict MCP call function that returns
// a canned successful response. Returns a cleanup func that restores the
// production wire.
func installPredictMocks(t *testing.T) func() {
	t.Helper()
	origFn := predictMCPCallFn

	predictMCPCallFn = func(count int) (*predictResult, error) {
		return &predictResult{
			CurrentRowsPerStream: map[string]int{
				"mcp":        100,
				"search":     80,
				"visibility": 90,
				"cost":       50,
				"embedder":   100,
				"dedup":      100,
				"triage":     70,
				"watcher":    60,
			},
			ProjectedNewRows:      200,
			EstimatedDedupSeconds: 3.5,
			ProjectedSegmentCount: 1,
		}, nil
	}

	return func() {
		predictMCPCallFn = origFn
	}
}

// resetVaultDoctorPredictFlags walks rootCmd -> vault -> predict-bulk-load
// and resets flags to defaults.
func resetVaultDoctorPredictFlags(t *testing.T) {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() != "vault" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() != "predict-bulk-load" {
				continue
			}
			sub.Flags().VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
			return
		}
	}
}

func TestPredictRegistered(t *testing.T) {
	if !findVaultLeaf(t, "predict-bulk-load") {
		t.Fatal("`ws vault predict-bulk-load` not registered as a subcommand of `ws vault`")
	}
}

func TestPredictTableOutput(t *testing.T) {
	restore := installPredictMocks(t)
	t.Cleanup(restore)
	t.Cleanup(func() { resetVaultDoctorPredictFlags(t) })

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "predict-bulk-load", "40"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected exit 0; got err=%v (stderr=%q)", err, errOut.String())
	}

	output := out.String()
	// Table output should contain key metric labels
	if !strings.Contains(output, "Projected New Rows") {
		t.Errorf("expected table to contain 'Projected New Rows'; got: %q", output)
	}
	if !strings.Contains(output, "200") {
		t.Errorf("expected table to contain projected row count '200'; got: %q", output)
	}
	if !strings.Contains(output, "Estimated Dedup Time") {
		t.Errorf("expected table to contain 'Estimated Dedup Time'; got: %q", output)
	}
}

func TestPredictJSONOutput(t *testing.T) {
	restore := installPredictMocks(t)
	t.Cleanup(restore)
	t.Cleanup(func() { resetVaultDoctorPredictFlags(t) })

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "predict-bulk-load", "--json", "40"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected exit 0; got err=%v (stderr=%q)", err, errOut.String())
	}

	// JSON output should be valid JSON
	var result predictResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v (out=%q)", err, out.String())
	}
	if result.ProjectedNewRows != 200 {
		t.Errorf("expected projected_new_rows=200; got %d", result.ProjectedNewRows)
	}
	if result.EstimatedDedupSeconds != 3.5 {
		t.Errorf("expected estimated_dedup_seconds=3.5; got %f", result.EstimatedDedupSeconds)
	}
}

func TestPredictReadOnly(t *testing.T) {
	restore := installPredictMocks(t)
	t.Cleanup(restore)
	t.Cleanup(func() { resetVaultDoctorPredictFlags(t) })

	// Track if any mutation happened (predict should be pure read-only)
	callCount := 0
	predictMCPCallFn = func(count int) (*predictResult, error) {
		callCount++
		return &predictResult{
			CurrentRowsPerStream:  map[string]int{"mcp": 10},
			ProjectedNewRows:      50,
			EstimatedDedupSeconds: 1.0,
			ProjectedSegmentCount: 1,
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "predict-bulk-load", "10"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	_ = rootCmd.Execute()

	// Should have called the MCP exactly once (read-only query)
	if callCount != 1 {
		t.Errorf("expected exactly 1 MCP call (read-only); got %d", callCount)
	}
}

func TestPredictMCPError(t *testing.T) {
	restore := installPredictMocks(t)
	t.Cleanup(restore)
	t.Cleanup(func() { resetVaultDoctorPredictFlags(t) })

	// Mock MCP error
	predictMCPCallFn = func(count int) (*predictResult, error) {
		return nil, errors.New("MCP server not running")
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "predict-bulk-load", "10"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when MCP call fails")
	}

	combined := out.String() + errOut.String() + err.Error()
	if !strings.Contains(combined, "MCP server not running") {
		t.Errorf("expected error message about MCP failure; got: %q", combined)
	}
}

// TestPredictEnvelopeErrorCarriesMappedExit checks that a non-OK envelope
// with a known error code is turned into a *cliErrorWithExit carrying the
// mapped exit code, not flattened into a bare error (which would exit 1).
func TestPredictEnvelopeErrorCarriesMappedExit(t *testing.T) {
	orig := predictCallToolFn
	t.Cleanup(func() { predictCallToolFn = orig })
	predictCallToolFn = func(_ context.Context, _, _ string, _ any) (*mcp.Envelope, error) {
		return &mcp.Envelope{OK: false, Error: &mcp.EnvelopeError{Code: "DEDUP_BLOCKED", Message: "too similar"}}, nil
	}

	_, err := predictMCPCallImpl(5)
	if err == nil {
		t.Fatal("expected an error on a non-OK envelope")
	}
	var ce *cliErrorWithExit
	if !errors.As(err, &ce) {
		t.Fatalf("expected *cliErrorWithExit carrying the mapped code; got %T (%v)", err, err)
	}
	if want := mcp.MapErrorCodeToExitCode("DEDUP_BLOCKED"); ce.code != want {
		t.Errorf("envelope error exit code = %d; want %d (mapped), not a flat 1", ce.code, want)
	}
}

// TestPredictEnvelopeErrorRoutesExitCodeEndToEnd drives the whole command
// and asserts Execute() would route the mapped exit code rather than 1.
func TestPredictEnvelopeErrorRoutesExitCodeEndToEnd(t *testing.T) {
	origTool := predictCallToolFn
	t.Cleanup(func() { predictCallToolFn = origTool })
	predictCallToolFn = func(_ context.Context, _, _ string, _ any) (*mcp.Envelope, error) {
		return &mcp.Envelope{OK: false, Error: &mcp.EnvelopeError{Code: "BUDGET_EXCEEDED", Message: "over budget"}}, nil
	}
	// Pin the production impl so a mock leaked from another test cannot mask routing.
	origFn := predictMCPCallFn
	predictMCPCallFn = predictMCPCallImpl
	t.Cleanup(func() { predictMCPCallFn = origFn })
	t.Cleanup(func() { resetVaultDoctorPredictFlags(t) })

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "predict-bulk-load", "5"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil error on an envelope failure")
	}
	var ce *cliErrorWithExit
	if !errors.As(err, &ce) {
		t.Fatalf("expected *cliErrorWithExit; got %T (%v)", err, err)
	}
	if ce.code != 2 {
		t.Errorf("BUDGET_EXCEEDED must route to exit 2; got %d", ce.code)
	}
	// The operator must still SEE the failure reason: predict does not set
	// SilenceErrors, so cobra prints "Error: <msg>" to stderr before the exit
	// code is routed. Guard against a future regression that would swallow it.
	if !strings.Contains(errOut.String(), "BUDGET_EXCEEDED") {
		t.Errorf("failure reason must reach stderr; errOut=%q", errOut.String())
	}
}

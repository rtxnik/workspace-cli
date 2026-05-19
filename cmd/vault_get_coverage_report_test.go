package cmd

// vault_get_coverage_report_test.go — unit tests for the CLI-08 leaf.
//
// Coverage:
//   - happy path: --json mode emits raw envelope.Data
//   - happy path: human mode emits indented JSON
//   - failure path: envelope.Error → cliErrorWithExit with mapped exit code
//   - registration: walks rootCmd → vault → get-coverage-report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

func TestVaultGetCoverageReportHappyPathHuman(t *testing.T) {
	orig := vaultGetCoverageReportRunFn
	t.Cleanup(func() { vaultGetCoverageReportRunFn = orig })
	vaultGetCoverageReportRunFn = func(_ context.Context, _ *cobra.Command) (*mcp.Envelope, error) {
		return &mcp.Envelope{
			OK:   true,
			Data: json.RawMessage(`{"missing":["foo"],"covered":["bar"]}`),
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "get-coverage-report"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	stdout := out.String()
	// Indented JSON contains newlines + 2-space indent.
	if !strings.Contains(stdout, `"missing"`) || !strings.Contains(stdout, "  \"missing\"") {
		t.Errorf("expected indented JSON output; got %q", stdout)
	}
}

func TestVaultGetCoverageReportJSONMode(t *testing.T) {
	orig := vaultGetCoverageReportRunFn
	t.Cleanup(func() { vaultGetCoverageReportRunFn = orig })
	raw := `{"missing":["foo"],"covered":["bar"]}`
	vaultGetCoverageReportRunFn = func(_ context.Context, _ *cobra.Command) (*mcp.Envelope, error) {
		return &mcp.Envelope{OK: true, Data: json.RawMessage(raw)}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "get-coverage-report", "--json"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stdout := strings.TrimRight(out.String(), "\n")
	if stdout != raw {
		t.Errorf("--json should passthrough raw envelope.Data; got %q want %q", stdout, raw)
	}
}

func TestVaultGetCoverageReportEnvelopeError(t *testing.T) {
	orig := vaultGetCoverageReportRunFn
	t.Cleanup(func() { vaultGetCoverageReportRunFn = orig })
	vaultGetCoverageReportRunFn = func(_ context.Context, _ *cobra.Command) (*mcp.Envelope, error) {
		return &mcp.Envelope{
			OK: false,
			Error: &mcp.EnvelopeError{
				Code:    "VALIDATION_FAILED",
				Message: "missing query parameter",
			},
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "get-coverage-report"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on envelope.Error")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T (%v)", err, err)
	}
	if cerr.code != 1 {
		t.Errorf("expected exit code 1 (VALIDATION_FAILED); got %d", cerr.code)
	}
	if !strings.Contains(cerr.msg, "VALIDATION_FAILED") {
		t.Errorf("expected msg to cite error code; got %q", cerr.msg)
	}
	combined := out.String() + errOut.String() + err.Error()
	if strings.Contains(combined, "Usage:") {
		t.Errorf("SilenceUsage must suppress usage block; got %q", combined)
	}
}

func TestVaultGetCoverageReportRegistered(t *testing.T) {
	if !findVaultLeaf(t, "get-coverage-report") {
		t.Fatal("`ws vault get-coverage-report` not registered as a subcommand of `ws vault`")
	}
}

// findVaultLeaf walks rootCmd → vault → <leaf-name>. Shared helper used by
// every vault leaf's Registered test.
func findVaultLeaf(t *testing.T, name string) bool {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() != "vault" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == name {
				return true
			}
		}
	}
	return false
}

func TestVaultRootRegistered(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "vault" {
			if c.Annotations["group"] != "vault" {
				t.Errorf("vault root annotation expected group=vault; got %q", c.Annotations["group"])
			}
			return
		}
	}
	t.Fatal("`ws vault` not registered as a subcommand of root")
}

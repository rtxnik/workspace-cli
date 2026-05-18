package cmd

// vault_validate_test.go — unit tests for CLI-04 `ws vault validate`.

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

func TestVaultValidateHappyPath(t *testing.T) {
	orig := vaultValidateRunFn
	t.Cleanup(func() { vaultValidateRunFn = orig })
	var gotID string
	vaultValidateRunFn = func(_ context.Context, _ *cobra.Command, vargs mcp.ValidateNoteArgs) (*mcp.Envelope, error) {
		gotID = vargs.Id
		return &mcp.Envelope{
			OK:   true,
			Data: json.RawMessage(`{"valid":true,"errors":[],"sufficiency":"sufficient"}`),
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "validate", "00_MOC"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if gotID != "00_MOC" {
		t.Errorf("expected note id '00_MOC' plumbed through; got %q", gotID)
	}
	if !strings.Contains(out.String(), `"valid"`) {
		t.Errorf("expected stdout to contain validation envelope; got %q", out.String())
	}
}

func TestVaultValidateFindings(t *testing.T) {
	orig := vaultValidateRunFn
	t.Cleanup(func() { vaultValidateRunFn = orig })
	vaultValidateRunFn = func(_ context.Context, _ *cobra.Command, _ mcp.ValidateNoteArgs) (*mcp.Envelope, error) {
		return &mcp.Envelope{
			OK: false,
			Error: &mcp.EnvelopeError{
				Code:    "VALIDATION_FAILED",
				Message: "missing required frontmatter field 'type'",
			},
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "validate", "broken-note"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on VALIDATION_FAILED envelope")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 1 {
		t.Errorf("expected exit 1 (VALIDATION_FAILED → findings); got %d", cerr.code)
	}
}

func TestVaultValidateRegistered(t *testing.T) {
	if !findVaultLeaf(t, "validate") {
		t.Fatal("`ws vault validate` not registered as a subcommand of `ws vault`")
	}
}

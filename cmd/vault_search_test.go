package cmd

// vault_search_test.go — unit tests for CLI-02 `ws vault search`.

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

func TestVaultSearchHappyPath(t *testing.T) {
	orig := vaultSearchRunFn
	t.Cleanup(func() { vaultSearchRunFn = orig })
	var gotQuery string
	var gotLimit int
	vaultSearchRunFn = func(_ context.Context, _ *cobra.Command, sargs mcp.SearchNotesArgs) (*mcp.Envelope, error) {
		gotQuery = sargs.Query
		gotLimit = sargs.Limit
		return &mcp.Envelope{
			OK:   true,
			Data: json.RawMessage(`[{"id":"foo","title":"Foo"},{"id":"bar","title":"Bar"}]`),
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "search", "kubernetes", "operators"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errOut.String())
	}
	if gotQuery != "kubernetes operators" {
		t.Errorf("expected query 'kubernetes operators'; got %q", gotQuery)
	}
	if gotLimit != 10 {
		t.Errorf("expected default limit 10; got %d", gotLimit)
	}
	if !strings.Contains(out.String(), `"id"`) {
		t.Errorf("expected stdout to contain search rows; got %q", out.String())
	}
}

func TestVaultSearchValidationFailed(t *testing.T) {
	orig := vaultSearchRunFn
	t.Cleanup(func() { vaultSearchRunFn = orig })
	vaultSearchRunFn = func(_ context.Context, _ *cobra.Command, _ mcp.SearchNotesArgs) (*mcp.Envelope, error) {
		return &mcp.Envelope{
			OK: false,
			Error: &mcp.EnvelopeError{
				Code:    "VALIDATION_FAILED",
				Message: "query too short",
			},
		}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "search", "x"})
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
		t.Errorf("expected exit 1 (VALIDATION_FAILED); got %d", cerr.code)
	}
	if !strings.Contains(cerr.msg, "VALIDATION_FAILED") {
		t.Errorf("expected msg to cite code; got %q", cerr.msg)
	}
}

func TestVaultSearchCustomLimit(t *testing.T) {
	orig := vaultSearchRunFn
	t.Cleanup(func() { vaultSearchRunFn = orig })
	var gotLimit int
	vaultSearchRunFn = func(_ context.Context, _ *cobra.Command, sargs mcp.SearchNotesArgs) (*mcp.Envelope, error) {
		gotLimit = sargs.Limit
		return &mcp.Envelope{OK: true, Data: json.RawMessage(`[]`)}, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "search", "go", "--limit", "25"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotLimit != 25 {
		t.Errorf("expected --limit 25 plumbed through; got %d", gotLimit)
	}
}

func TestVaultSearchRegistered(t *testing.T) {
	if !findVaultLeaf(t, "search") {
		t.Fatal("`ws vault search` not registered as a subcommand of `ws vault`")
	}
}

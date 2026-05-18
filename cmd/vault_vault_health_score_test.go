package cmd

// vault_vault_health_score_test.go — unit tests for CLI-09
// `ws vault vault-health-score`. Asserts the band → exit-code mapping
// per CONTEXT D-28 (0=green, 1=yellow, 2=red) and that the integer
// score is printed verbatim to stdout (machine-parseable for cron).

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestVaultVaultHealthScoreHappyPathGreen(t *testing.T) {
	orig := vaultHealthScoreComputeFn
	t.Cleanup(func() { vaultHealthScoreComputeFn = orig })
	vaultHealthScoreComputeFn = func(_ context.Context, _ *cobra.Command) (int, error) {
		return 85, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "vault-health-score"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error for green band: %v", err)
	}
	got := strings.TrimSpace(out.String())
	if got != "85" {
		t.Errorf("expected stdout '85'; got %q", got)
	}
}

func TestVaultVaultHealthScoreYellowBandExit1(t *testing.T) {
	orig := vaultHealthScoreComputeFn
	t.Cleanup(func() { vaultHealthScoreComputeFn = orig })
	vaultHealthScoreComputeFn = func(_ context.Context, _ *cobra.Command) (int, error) {
		return 60, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "vault-health-score"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected non-nil error on yellow band (exit code propagation)")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T (%v)", err, err)
	}
	if cerr.code != 1 {
		t.Errorf("yellow band must map to exit 1; got %d", cerr.code)
	}
	if cerr.msg != "" {
		t.Errorf("yellow band exit MUST have empty msg (cron-friendly); got %q", cerr.msg)
	}
	got := strings.TrimSpace(out.String())
	if got != "60" {
		t.Errorf("expected stdout '60'; got %q", got)
	}
}

func TestVaultVaultHealthScoreRedBandExit2(t *testing.T) {
	orig := vaultHealthScoreComputeFn
	t.Cleanup(func() { vaultHealthScoreComputeFn = orig })
	vaultHealthScoreComputeFn = func(_ context.Context, _ *cobra.Command) (int, error) {
		return 40, nil
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "vault-health-score"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on red band")
	}
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 2 {
		t.Errorf("red band must map to exit 2; got %d", cerr.code)
	}
}

func TestVaultVaultHealthScoreComputationError(t *testing.T) {
	orig := vaultHealthScoreComputeFn
	t.Cleanup(func() { vaultHealthScoreComputeFn = orig })
	vaultHealthScoreComputeFn = func(_ context.Context, _ *cobra.Command) (int, error) {
		return 0, errors.New("MCP transport: connection refused")
	}

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"vault", "vault-health-score"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error on computation failure")
	}
	if !strings.Contains(err.Error(), "vault-health-score") {
		t.Errorf("expected wrapped error to cite 'vault-health-score'; got %q", err.Error())
	}
}

func TestVaultVaultHealthScoreStdoutParseable(t *testing.T) {
	// Cron consumers MUST be able to parse stdout as an integer in [0, 100].
	orig := vaultHealthScoreComputeFn
	t.Cleanup(func() { vaultHealthScoreComputeFn = orig })
	for _, expectedScore := range []int{0, 50, 70, 100} {
		score := expectedScore
		t.Run(strconv.Itoa(score), func(t *testing.T) {
			vaultHealthScoreComputeFn = func(_ context.Context, _ *cobra.Command) (int, error) {
				return score, nil
			}
			var out, errOut bytes.Buffer
			rootCmd.SetOut(&out)
			rootCmd.SetErr(&errOut)
			rootCmd.SetArgs([]string{"vault", "vault-health-score"})
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
			})
			_ = rootCmd.Execute() // we ignore err — band may exit non-zero
			got, err := strconv.Atoi(strings.TrimSpace(out.String()))
			if err != nil {
				t.Fatalf("stdout must be parseable as int; got %q (err=%v)", out.String(), err)
			}
			if got != score {
				t.Errorf("stdout score = %d; want %d", got, score)
			}
			if got < 0 || got > 100 {
				t.Errorf("score must be in [0,100]; got %d", got)
			}
		})
	}
}

func TestVaultVaultHealthScoreRegistered(t *testing.T) {
	if !findVaultLeaf(t, "vault-health-score") {
		t.Fatal("`ws vault vault-health-score` not registered")
	}
}

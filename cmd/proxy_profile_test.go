package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestProxyProfileCommand(t *testing.T) {
	var foundProxy bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "proxy" {
			continue
		}
		foundProxy = true
		var foundProfile bool
		for _, sub := range c.Commands() {
			if sub.Name() != "profile" {
				continue
			}
			foundProfile = true
			want := map[string]bool{
				"add": false, "list": false, "use": false, "rm": false,
				"show": false, "current": false, "regenerate": false,
			}
			for _, leaf := range sub.Commands() {
				if _, ok := want[leaf.Name()]; ok {
					want[leaf.Name()] = true
				}
			}
			for name, present := range want {
				if !present {
					t.Errorf("expected `ws proxy profile %s` leaf not registered", name)
				}
			}
			if flag := sub.PersistentFlags().Lookup("no-migrate"); flag == nil {
				t.Error("expected --no-migrate persistent flag on `ws proxy profile`")
			}
		}
		if !foundProfile {
			t.Error("`ws proxy profile` not registered as a subcommand of `ws proxy`")
		}
	}
	if !foundProxy {
		t.Fatal("`ws proxy` not registered on rootCmd")
	}
}

func TestProxyProfileHelpExits0(t *testing.T) {
	cmd := rootCmd
	origOut, origErr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(origOut)
		cmd.SetErr(origErr)
	})
	cmd.SetArgs([]string{"proxy", "profile", "--help"})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("`ws proxy profile --help` returned %v; stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	combined := out.String() + errOut.String()
	for _, leaf := range []string{"add", "list", "use", "rm", "show", "current", "regenerate"} {
		if !strings.Contains(combined, leaf) {
			t.Errorf("help output missing leaf %q; got: %s", leaf, combined)
		}
	}
}

// TestProfileUseRendersPreSwapError guards the 2026-05-13 hotfix: pre-swap
// errors from xray.SwitchTo (invalid profile name, legacy bind mount, missing
// target file) must reach the operator via Cobra's default error printer
// ("Error: <msg>") instead of being swallowed by os.Exit(1). The original
// profileUseCmd used `Run` and exited silently on error; this test pins the
// RunE contract.
//
// We use a slash-containing profile name to trip ValidateProfileName's regex
// (^[a-z0-9_-]{1,32}$) — same pre-swap error path as the bind-check failure,
// but reachable without overriding internal/xray package-private seams.
// --no-migrate avoids EnsureMigrated I/O during the test.
func TestProfileUseRendersPreSwapError(t *testing.T) {
	cmd := rootCmd
	origOut, origErr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	t.Cleanup(func() {
		cmd.SetArgs(nil)
		cmd.SetOut(origOut)
		cmd.SetErr(origErr)
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"proxy", "profile", "use", "bad/name", "--no-migrate"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected Execute() to return non-nil for invalid profile name (pre-swap error must propagate)")
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "invalid profile name") {
		t.Fatalf("expected error output to mention `invalid profile name`; out=%q err=%q execErr=%v",
			out.String(), errOut.String(), err)
	}
	// Cobra's default error printer renders returned errors as "Error: <msg>".
	if !strings.Contains(combined, "Error:") {
		t.Errorf("expected Cobra to render returned error with `Error:` prefix; got: %s", combined)
	}
	// SilenceUsage=true must suppress the usage block on a RunE error path.
	if strings.Contains(combined, "Usage:") {
		t.Errorf("expected SilenceUsage=true to suppress usage block; got: %s", combined)
	}
}

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

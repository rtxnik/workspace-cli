package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestInitDeprecationBannerStderr verifies D-02 + PROXY-PROFILE-08:
// `ws proxy init --add` emits its deprecation banner to STDERR ONLY (NOT
// stdout). RESEARCH §11 documents that Cobra's Deprecated field routes the
// banner to stdout (Cobra v1.10.1 source), which would break stdout-parsing
// automation; this plan uses manual fmt.Fprintln(os.Stderr, ...) in a
// PreRun on proxyInitCmd instead. The test exercises the PreRun in
// isolation so it does not depend on the live filesystem path inside Run.
func TestInitDeprecationBannerStderr(t *testing.T) {
	bannerSubstring := "ws proxy init --add' is deprecated"

	run := func(addSet bool) (stdout, stderr string) {
		t.Helper()
		// Minimal wrapper so cmd.Flags().GetBool("add") works without
		// pulling the full cobra tree into the test.
		c := &cobra.Command{Use: "init"}
		c.Flags().Bool("add", false, "")
		if addSet {
			if err := c.Flags().Set("add", "true"); err != nil {
				t.Fatalf("set --add: %v", err)
			}
		}

		// Capture os.Stderr and os.Stdout via pipes; the banner is emitted
		// via fmt.Fprintln(os.Stderr, ...) so we have to swap the global
		// rather than rely on cobra's SetOut/SetErr (which only redirect
		// cobra-internal writers).
		origStderr := os.Stderr
		origStdout := os.Stdout
		rE, wE, errE := os.Pipe()
		if errE != nil {
			t.Fatalf("pipe stderr: %v", errE)
		}
		rO, wO, errO := os.Pipe()
		if errO != nil {
			t.Fatalf("pipe stdout: %v", errO)
		}
		os.Stderr = wE
		os.Stdout = wO

		proxyInitCmd.PreRun(c, []string{})

		_ = wE.Close()
		_ = wO.Close()
		os.Stderr = origStderr
		os.Stdout = origStdout

		var bufE, bufO bytes.Buffer
		_, _ = io.Copy(&bufE, rE)
		_, _ = io.Copy(&bufO, rO)
		return bufO.String(), bufE.String()
	}

	t.Run("with --add: banner on stderr only", func(t *testing.T) {
		stdout, stderr := run(true)
		if !strings.Contains(stderr, bannerSubstring) {
			t.Errorf("expected banner on stderr; got stderr=%q", stderr)
		}
		if strings.Contains(stdout, bannerSubstring) {
			t.Errorf("banner leaked to stdout: %q (D-02 + RESEARCH §11)", stdout)
		}
	})

	t.Run("without --add: no banner", func(t *testing.T) {
		stdout, stderr := run(false)
		if strings.Contains(stdout, bannerSubstring) || strings.Contains(stderr, bannerSubstring) {
			t.Errorf("banner emitted without --add flag; stdout=%q stderr=%q", stdout, stderr)
		}
	})
}

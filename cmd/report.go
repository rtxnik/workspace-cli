package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/output"
)

// reportBlock is a render-layer block a report is made of: KV or Checks.
type reportBlock interface {
	Render(s *output.Stream) string
}

// writeReport renders a report's blocks for s, one blank line between two of
// them, and writes the whole with one Fprintln to w — the command's stdout
// writer for the report, rendered for Out(). Its write error is the
// command's to return to the root.
func writeReport(w io.Writer, s *output.Stream, blocks ...reportBlock) error {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Render(s))
	}
	_, err := fmt.Fprintln(w, strings.Join(parts, "\n\n"))
	return err
}

// checksCaption is the caption a Checks report closes with, counted from the
// states it renders: "N of M checks passed", N counting the ok lines only,
// then the failed, degraded and unknown counts in that order, each only when
// it is not zero. An unknown — a check that was not run, or ran without
// reaching a verdict — is never counted as passed.
func checksCaption(items []output.Check) string {
	var ok, failed, degraded, unknown int
	for _, it := range items {
		switch it.State {
		case output.StateOK:
			ok++
		case output.StateFail:
			failed++
		case output.StateAdvisory:
			degraded++
		case output.StateUnknown:
			unknown++
		}
	}
	caption := fmt.Sprintf("%d of %d checks passed", ok, len(items))
	for _, n := range []struct {
		count int
		word  string
	}{{failed, "failed"}, {degraded, "degraded"}, {unknown, "unknown"}} {
		if n.count > 0 {
			caption += fmt.Sprintf(", %d %s", n.count, n.word)
		}
	}
	return caption
}

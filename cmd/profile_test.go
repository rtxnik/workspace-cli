package cmd

import (
	"io"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/output"
)

// TestProfileSummary pins the summary the profile-create wizard shows before
// its confirmation (phase-5 §3.10): the answers under their keys, aligned,
// Packages and Tools only when given.
func TestProfileSummary(t *testing.T) {
	errs := output.NewStreamAt(io.Discard, 80, false, output.ColourNone, false)
	for _, c := range []struct {
		name     string
		packages string
		tools    []string
		dind     bool
		want     string
	}{
		{"every answer", "curl git", []string{"go", "golangci-lint"}, false, "Profile summary\n" +
			"  Name      go-custom\n" +
			"  Image     mcr.microsoft.com/devcontainers/go:1.26\n" +
			"  Packages  curl git\n" +
			"  Tools     go, golangci-lint\n" +
			"  DinD      false"},
		{"no packages, no tools", "", nil, true, "Profile summary\n" +
			"  Name   go-custom\n" +
			"  Image  mcr.microsoft.com/devcontainers/go:1.26\n" +
			"  DinD   true"},
	} {
		got := profileSummary("go-custom", "mcr.microsoft.com/devcontainers/go:1.26", c.packages, c.tools, c.dind).Render(errs)
		if got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

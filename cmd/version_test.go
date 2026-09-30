package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

// TestVersionIsTheVersionWhenPiped runs --version through the real Execute
// in a child whose stdout is a pipe: exactly "ws dev" and a newline, and
// nothing on stderr, so `ws --version | head -1` is the version and not a
// line of the logo.
func TestVersionIsTheVersionWhenPiped(t *testing.T) {
	code, stdout, stderr := runExecuteChild(t, errorCase{name: "--version", args: []string{"--version"}})
	if code != 0 || stdout != "ws dev\n" || stderr != "" {
		t.Errorf("ws --version into a pipe: exit %d, stdout %q, stderr %q; want exit 0, %q and nothing on stderr",
			code, stdout, stderr, "ws dev\n")
	}
}

// TestVersionTemplateFollowsTheStream: on a UTF-8 terminal the logo comes
// first, every line painted RoleAccent, or unpainted without colour; in the
// ASCII glyph mode, and on a stream that is not a terminal, the version is
// alone. Each template is executed by cobra, so what is checked is what
// --version prints, not only the template string.
func TestVersionTemplateFollowsTheStream(t *testing.T) {
	coloured := output.NewStreamAt(io.Discard, 80, true, output.ColourTrue, false)
	logo := func(paint func(string) string) string {
		var b strings.Builder
		for _, line := range []string{"╦ ╦╔═╗", "║║║╚═╗", "╚╩╝╚═╝"} {
			b.WriteString(paint(line) + "\n")
		}
		return b.String()
	}
	accent := func(line string) string { return coloured.Style(output.RoleAccent).Render(line) }
	plain := func(line string) string { return line }

	for _, c := range []struct {
		name string
		s    *output.Stream
		want string
	}{
		{"UTF-8 terminal with colour", coloured, logo(accent) + "ws dev\n"},
		{"UTF-8 terminal under NO_COLOR", output.NewStreamAt(io.Discard, 80, true, output.ColourNone, false), logo(plain) + "ws dev\n"},
		{"ASCII terminal", output.NewStreamAt(io.Discard, 80, true, output.ColourTrue, true), "ws dev\n"},
		{"not a terminal", output.NewStreamAt(io.Discard, 80, false, output.ColourNone, false), "ws dev\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			root := &cobra.Command{Use: "ws", Version: "dev", RunE: func(*cobra.Command, []string) error { return nil }}
			root.SetVersionTemplate(versionTemplate(c.s))
			root.SetOut(&out)
			root.SetArgs([]string{"--version"})
			if err := root.Execute(); err != nil {
				t.Fatalf("--version: %v", err)
			}
			if out.String() != c.want {
				t.Errorf("--version printed %q, want %q", out.String(), c.want)
			}
		})
	}
}

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A ratchet on direct styles.
//
// The render layer owns colour: a command asks for a role on a stream, and
// the stream decides what, if anything, to emit for that fd. A style built
// outside internal/output — lipgloss.NewStyle, a raw palette colour, the
// output.Style* values and SectionStyle — is chosen before any stream
// exists. The lines that still build one are the sites later phases migrate,
// and directStyleInventory is their exact count per file.
//
// It holds in both directions. A new file or a higher count fails; so does a
// lower one, until the map is lowered with it, so the map is always the true
// inventory. When it is empty, it is the render layer's guard that no such
// line exists outside internal/output.

// directStylePattern matches a line that builds a style outside the layer.
var directStylePattern = regexp.MustCompile(
	`lipgloss\.NewStyle|lipgloss\.Color\(|output\.Style[A-Z]|output\.SectionStyle|` +
		`output\.(Red|Green|Yellow|Blue|Purple|Aqua|Orange|Gray)\b`)

// directStyleInventory is the count of matching lines per non-test file
// outside internal/output, as of this commit.
var directStyleInventory = map[string]int{
	"cmd/profile.go":      1,
	"cmd/vault_status.go": 8,
}

// directStyleLines counts, per non-test Go file under root outside
// internal/output, the lines that match directStylePattern. Paths are
// relative to root, with forward slashes.
func directStyleLines(root string) (map[string]int, error) {
	counts := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "vendor" || rel == "internal/output" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(body), "\n") {
			if directStylePattern.MatchString(line) {
				counts[rel]++
			}
		}
		return nil
	})
	return counts, err
}

// ratchetDiff lists every file whose count differs from the inventory.
func ratchetDiff(counts, inventory map[string]int) []string {
	files := map[string]bool{}
	for f := range counts {
		files[f] = true
	}
	for f := range inventory {
		files[f] = true
	}
	var diff []string
	for f := range files {
		switch got, want := counts[f], inventory[f]; {
		case got > want:
			diff = append(diff, fmt.Sprintf("%s: %d direct-style lines, the inventory allows %d; style through a Stream role instead", f, got, want))
		case got < want:
			diff = append(diff, fmt.Sprintf("%s: %d direct-style lines, the inventory says %d; lower directStyleInventory with the migration", f, got, want))
		}
	}
	sort.Strings(diff)
	return diff
}

// TestDirectStyleRatchet holds the repository to the inventory. The test
// runs in cmd/, so the repository is one level up; that is checked rather
// than assumed, because a scan of the wrong directory would find nothing
// and report every file as migrated.
func TestDirectStyleRatchet(t *testing.T) {
	root := ".."
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repository root: %v", root, err)
	}
	counts, err := directStyleLines(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ratchetDiff(counts, directStyleInventory) {
		t.Error(d)
	}
}

// TestDirectStyleRatchetCanFail is the ratchet's own control, over a planted
// repository: every alternative of the pattern is counted, once per line;
// test files, internal/output and vendor are not read; and a count above or
// below the inventory is reported.
func TestDirectStyleRatchetCanFail(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.invalid/x\n")
	write("cmd/a.go", `package cmd
var a = lipgloss.NewStyle()
var b = lipgloss.Color("#fb4934")
var c = output.StyleDim.Render("x")
var d = output.SectionStyle.Render("x")
var e = lipgloss.NewStyle().Foreground(output.Orange)
var f = output.RedDim
var g = output.Stylesheet
`)
	write("tools/b.go", "package tools\nvar h = output.Gray\n")
	write("cmd/a_test.go", "package cmd\nvar i = lipgloss.NewStyle()\n")
	write("internal/output/theme.go", "package output\nvar j = lipgloss.NewStyle()\n")
	write("vendor/v.go", "package v\nvar k = lipgloss.NewStyle()\n")

	counts, err := directStyleLines(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"cmd/a.go": 5, "tools/b.go": 1}; fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("counted %v, want %v", counts, want)
	}

	diff := ratchetDiff(counts, map[string]int{"cmd/a.go": 6})
	want := []string{
		"cmd/a.go: 5 direct-style lines, the inventory says 6; lower directStyleInventory with the migration",
		"tools/b.go: 1 direct-style lines, the inventory allows 0; style through a Stream role instead",
	}
	if strings.Join(diff, "\n") != strings.Join(want, "\n") {
		t.Errorf("reported\n%s\nwant\n%s", strings.Join(diff, "\n"), strings.Join(want, "\n"))
	}
}

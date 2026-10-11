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
// exists. The phases of the render layer migrated the lines that built one,
// lowering directStyleInventory, their count per file, with each.
//
// It holds in both directions. A new file or a higher count fails; so does a
// lower one, until the map is lowered with it, so the map is always the true
// inventory. It is empty: it is now the render layer's standing guard that
// no such line exists outside internal/output.
//
// The pattern counts the import of lipgloss too, any of its packages, under
// its v1 path and its v2 one (charm.land/lipgloss), in double quotes or back
// quotes. No file outside the layer needs it once none builds a style, and a
// ban on the import closes the routes the other alternatives cannot see — a
// border, a renderer, a style reached through an alias.

// directStylePattern matches a line that builds a style outside the layer.
var directStylePattern = regexp.MustCompile(
	`lipgloss\.NewStyle|lipgloss\.Color\(|output\.Style[A-Z]|output\.SectionStyle|` +
		`output\.(Red|Green|Yellow|Blue|Purple|Aqua|Orange|Gray)\b|` +
		"[\"`](github\\.com/charmbracelet|charm\\.land)/lipgloss[/\"`]")

// directStyleInventory is the count of matching lines per non-test file
// outside internal/output: none, since phase 5 migrated the last.
var directStyleInventory = map[string]int{}

// sourcesOutsideLayer calls visit with every non-test Go file under root
// outside internal/output, vendor and .git: its path relative to root, with
// forward slashes, and its body.
func sourcesOutsideLayer(root string, visit func(rel, body string)) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
		visit(rel, string(body))
		return nil
	})
}

// directStyleLines counts, per non-test Go file under root outside
// internal/output, the lines that match directStylePattern.
func directStyleLines(root string) (map[string]int, error) {
	counts := map[string]int{}
	err := sourcesOutsideLayer(root, func(rel, body string) {
		for _, line := range strings.Split(body, "\n") {
			if directStylePattern.MatchString(line) {
				counts[rel]++
			}
		}
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

// plantedRepository is an empty directory for a control to plant a
// repository in, and the function that writes one file of it.
func plantedRepository(t *testing.T) (string, func(name, body string)) {
	t.Helper()
	dir := t.TempDir()
	return dir, func(name, body string) {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDirectStyleRatchetCanFail is the ratchet's own control, over a planted
// repository: every alternative of the pattern is counted, once per line —
// the lipgloss import under an alias, from a subpackage, in back quotes and
// under its v2 path included, a module whose path merely starts like
// lipgloss's not; test files, internal/output
// and vendor are not read; and a count above or below the inventory is
// reported.
func TestDirectStyleRatchetCanFail(t *testing.T) {
	dir, write := plantedRepository(t)
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
	write("cmd/c.go", `package cmd
import (
	lg "github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/lipglossx"
)
var border = lg.RoundedBorder()
`)
	write("cmd/d.go", "package cmd\nimport (\n\tlg `github.com/charmbracelet/lipgloss`\n\tl2 \"charm.land/lipgloss/v2\"\n\t\"charm.land/lipglossy\"\n)\n")
	write("cmd/a_test.go", "package cmd\nvar i = lipgloss.NewStyle()\n")
	write("internal/output/theme.go", "package output\nvar j = lipgloss.NewStyle()\n")
	write("vendor/v.go", "package v\nvar k = lipgloss.NewStyle()\n")

	counts, err := directStyleLines(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"cmd/a.go": 5, "cmd/c.go": 2, "cmd/d.go": 2, "tools/b.go": 1}; fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("counted %v, want %v", counts, want)
	}

	diff := ratchetDiff(counts, map[string]int{"cmd/a.go": 6})
	want := []string{
		"cmd/a.go: 5 direct-style lines, the inventory says 6; lower directStyleInventory with the migration",
		"cmd/c.go: 2 direct-style lines, the inventory allows 0; style through a Stream role instead",
		"cmd/d.go: 2 direct-style lines, the inventory allows 0; style through a Stream role instead",
		"tools/b.go: 1 direct-style lines, the inventory allows 0; style through a Stream role instead",
	}
	if strings.Join(diff, "\n") != strings.Join(want, "\n") {
		t.Errorf("reported\n%s\nwant\n%s", strings.Join(diff, "\n"), strings.Join(want, "\n"))
	}
}

// A guard on the prompt core.
//
// Every prompt runs through the core in internal/output, which decides
// whether one may run at all and how its run ended. A file outside the layer
// may build a huh form — the profile wizard does — but hands it to
// output.RunForm and runs none itself. A line does not say whose Run it
// calls, so in a file that imports huh, any of its packages, under its v1
// path or its v2 one, every call named Run, RunWithContext or RunAccessible
// counts — huh.Run and the last line of a chain included — and such a file
// calls nothing of those names. The guard reads lines, not types: a form run
// in a file that does not import huh is not seen.

// huhImportPattern matches a line that imports huh, any of its packages,
// under its v1 path or its v2 one, in double quotes or back quotes.
var huhImportPattern = regexp.MustCompile("[\"`](github\\.com/charmbracelet|charm\\.land)/huh[/\"`]")

// huhRunPattern matches, in a file that imports huh, a line that runs a huh
// form or field.
var huhRunPattern = regexp.MustCompile(`\b(Run|RunWithContext|RunAccessible)\(`)

// huhRunLines counts, per non-test Go file under root outside
// internal/output that imports huh, the lines that match huhRunPattern.
func huhRunLines(root string) (map[string]int, error) {
	counts := map[string]int{}
	err := sourcesOutsideLayer(root, func(rel, body string) {
		if !huhImportPattern.MatchString(body) {
			return
		}
		for _, line := range strings.Split(body, "\n") {
			if huhRunPattern.MatchString(line) {
				counts[rel]++
			}
		}
	})
	return counts, err
}

// huhRunReport lists, sorted, every file that runs a huh form or field.
func huhRunReport(counts map[string]int) []string {
	report := make([]string, 0, len(counts))
	for f, n := range counts {
		report = append(report, fmt.Sprintf("%s: %d huh-run lines; ask through output.Confirm, output.Select or output.RunForm instead", f, n))
	}
	sort.Strings(report)
	return report
}

// TestHuhRunGuard holds the repository to no line that runs a huh form or
// field outside the prompt core.
func TestHuhRunGuard(t *testing.T) {
	root := ".."
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repository root: %v", root, err)
	}
	counts, err := huhRunLines(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range huhRunReport(counts) {
		t.Error(r)
	}
}

// TestHuhRunGuardCanFail is the guard's own control, over a planted
// repository: a form.Run() in cmd/ is counted, and so is every other way to
// run huh — the last line of a chain, RunWithContext, RunAccessible,
// huh.Run, under an alias, from a subpackage and under the v2 path — once per
// line; a name that merely ends in Run, output.RunForm, cobra's RunE, and a
// Run in a file that does not import huh, or imports a module whose path
// merely starts like huh's, are not; test files, internal/output and vendor
// are not read; and every file counted is reported.
func TestHuhRunGuardCanFail(t *testing.T) {
	dir, write := plantedRepository(t)
	write("go.mod", "module example.invalid/x\n")
	write("cmd/a.go", `package cmd
import "github.com/charmbracelet/huh"
var c = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error {
	if err := form.Run(); err != nil {
		return err
	}
	_ = huh.NewConfirm().
		Title("Delete?").
		Run()
	_ = form.RunWithContext(ctx)
	_ = field.RunAccessible(w, r)
	_ = huh.Run(field)
	_ = preRun(form)
	return output.RunForm(form)
}}
`)
	write("cmd/b.go", "package cmd\nimport h \"github.com/charmbracelet/huh\"\nvar _, _ = h.Run(field), h.Run(field)\n")
	write("cmd/c.go", "package cmd\nimport \"github.com/charmbracelet/huh/spinner\"\nvar _ = spinner.New().Run()\n")
	write("cmd/d.go", "package cmd\nimport `charm.land/huh/v2`\nvar _ = form.Run()\n")
	write("cmd/e.go", "package cmd\nimport \"os/exec\"\nvar _ = exec.Command(\"true\").Run()\n")
	write("cmd/f.go", "package cmd\nimport \"github.com/charmbracelet/huhx\"\nvar _ = huhx.Run()\n")
	write("cmd/a_test.go", "package cmd\nimport \"github.com/charmbracelet/huh\"\nvar _ = form.Run()\n")
	write("internal/output/prompt.go", "package output\nimport \"github.com/charmbracelet/huh\"\nvar _ = form.Run()\n")
	write("vendor/v.go", "package v\nimport \"github.com/charmbracelet/huh\"\nvar _ = form.Run()\n")

	counts, err := huhRunLines(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"cmd/a.go": 5, "cmd/b.go": 1, "cmd/c.go": 1, "cmd/d.go": 1}; fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("counted %v, want %v", counts, want)
	}

	report := huhRunReport(counts)
	want := []string{
		"cmd/a.go: 5 huh-run lines; ask through output.Confirm, output.Select or output.RunForm instead",
		"cmd/b.go: 1 huh-run lines; ask through output.Confirm, output.Select or output.RunForm instead",
		"cmd/c.go: 1 huh-run lines; ask through output.Confirm, output.Select or output.RunForm instead",
		"cmd/d.go: 1 huh-run lines; ask through output.Confirm, output.Select or output.RunForm instead",
	}
	if strings.Join(report, "\n") != strings.Join(want, "\n") {
		t.Errorf("reported\n%s\nwant\n%s", strings.Join(report, "\n"), strings.Join(want, "\n"))
	}
}

package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// §6.8 anti-drift: no `lipgloss.Color("#` outside theme.go.
//
// The guard exists because the palette is the one part of the layer that
// cannot be made correct by construction — §4.6 keeps colour on roles only so
// that the light-background case needs no second palette, and that only holds
// while every colour value is declared in one file. A style built at a call
// site is also chosen BEFORE any Stream exists, which is what makes
// cmd/root.go's package-init usage template unfixable by a runtime probe.

const colourLiteral = `lipgloss.Color("#`

// scanColourLiterals reports every non-test Go file under root, other than the
// permitted one, that names a colour literal. The permitted file's count is
// returned separately so the caller can assert the scan is looking at real
// content: a guard that finds nothing because the pattern is nowhere at all
// would pass over a package that had lost its palette entirely.
//
// ROOT IS THE REPOSITORY, NOT THIS PACKAGE, and `permitted` is a repository-
// relative PATH, not a basename. §6.8 is a repo-wide clause ("no
// lipgloss.Color(\"# outside internal/output/theme.go") and the drift it
// exists to catch lives outside this package by construction: §7 names ten
// direct-style sites under cmd/, with cmd/root.go baking styles into the usage
// template at package-init time. A walk rooted at "." inside a Go test is
// rooted at internal/output and is structurally blind to every one of them; a
// permit matched by filepath.Base would also accept any file called theme.go
// anywhere in the tree.
//
// The named return is `found` rather than `violations`, which is a type this
// package already declares in text_invariants_test.go. Shadowing it here would
// be legal Go and a trap for the next reader.
func scanColourLiterals(root, permitted string) (found []string, inPermitted int, err error) {
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// .git holds packed objects that are not source, and a vendor
			// tree is not this repository's code.
			//
			// testdata is NOT skipped. The exclusion was in an earlier draft
			// and bought nothing: measured, this repository's three testdata
			// directories — internal/xrayconf, internal/hysteria2 and
			// scripts/ci — contain no lipgloss reference at all, and every
			// exclusion a guard carries is a place drift can hide.
			if name := d.Name(); name == ".git" || name == "vendor" {
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
		n := strings.Count(string(body), colourLiteral)
		if n == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == permitted {
			inPermitted += n
			return nil
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, colourLiteral) {
				found = append(found, rel+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	return found, inPermitted, err
}

// permittedThemeFile is §6.8's one exemption, spelled as the repository-
// relative path the clause names — not as a basename, which would exempt any
// file called theme.go anywhere in the tree.
const permittedThemeFile = "internal/output/theme.go"

// repoRoot is the repository root as seen from this package's test binary,
// whose working directory is internal/output. It is two levels up, and it is
// CHECKED rather than assumed: a package moved one level deeper would silently
// narrow the guard back to this package, which is exactly the defect this
// function exists to prevent, and it would narrow it while staying green.
func repoRoot() (string, error) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("%s is not the repository root (no go.mod there): %w", root, err)
	}
	return root, nil
}

// ------------------------------------------------ §6.1 / §8: no width pinning
//
// "If the renderer passes its computed total to lipgloss as `.Width(total)`,
// lipgloss re-fits the grid to whatever number it is given, absorbing any
// arithmetic error as silent content loss instead of overflow."
//
// §6.1's paired assertion exists because of that call, and it is NOT a defence
// against the call coming back: with pinning in place the sweep loses its
// chrome-defect kills and the grid-total half of the paired assertion becomes a
// tautology, since lipgloss has been TOLD the number the assertion then reads
// back. Measured by TestPairedAssertionCatchesWhatTheSweepCannot on this tree:
// the chrome off-by-one alone reddens width_budget 14220 times, and with the
// pinning switched on as well it reddens it 0 times. The only protection is
// that the call is not there, which is a property of the source and so is
// asserted over the source.
//
// The one permitted occurrence is the mutation switch itself, permitted by
// SHAPE rather than by file: the call must sit directly under
// `if mutants.PinTableWidth {`.

const (
	widthPinCall  = ".Width("
	widthPinGuard = "if mutants.PinTableWidth {"
)

// scanWidthPinning reports every non-test Go file under root that hands a
// width to a renderer outside the mutation switch, and counts the guarded
// occurrences so the caller can see the switch is still wired.
//
// Occurrences inside a line comment are not calls; the file that declares the
// switch and the file that explains why the call is absent both name it in
// prose — measured, blocks.go:96 and mutants.go:74 — and a guard that could
// not tell those apart would be unusable.
//
// Named return `found`, for the same reason scanColourLiterals has one.
func scanWidthPinning(root string) (found []string, guarded int, err error) {
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			at := strings.Index(line, widthPinCall)
			if at < 0 {
				continue
			}
			if c := strings.Index(line, "//"); c >= 0 && c < at {
				continue // prose about the call, not the call
			}
			if i > 0 && strings.Contains(lines[i-1], widthPinGuard) {
				guarded++
				continue
			}
			found = append(found, filepath.ToSlash(path)+":"+strconv.Itoa(i+1))
		}
		return nil
	})
	return found, guarded, err
}

// assertAntiDrift is §6.8 and §6.1's source-level half.
//
// Goes red when: any file other than theme.go names a colour value — and, in
// the OTHER direction, when theme.go stops naming any, which would make the
// guard pass for the wrong reason; or when a width is handed to a renderer
// anywhere outside the mutation switch.
//
// Every one of those four reds was planted against the scanners in
// TestAntiDriftGuardCanFail and TestWidthPinningGuardCanFail rather than
// assumed: no runtime mutant can redden an assertion about source text, so the
// planted-source controls are the only way this body is shown able to fail.
var assertAntiDrift = globalAssertion{
	name: "anti_drift",
	spec: "§6.8 / §6.1",
	what: `lipgloss.Color("#` + " appears in no non-test file outside theme.go and does appear in theme.go; " +
		".Width( appears in no non-test file outside the mutants.PinTableWidth branch",
	check: func(r *results) {
		root, err := repoRoot()
		if err != nil {
			r.fail("anti_drift", "§6.8's guard is repo-wide and cannot locate the repository root: %v", err)
			return
		}
		violations, inPermitted, err := scanColourLiterals(root, permittedThemeFile)
		if err != nil {
			r.fail("anti_drift", "scan failed: %v", err)
			return
		}
		for _, v := range violations {
			r.fail("anti_drift", "colour literal outside %s: %s", permittedThemeFile, v)
		}
		if inPermitted == 0 {
			r.fail("anti_drift", "%s names no colour literal; the guard would pass vacuously", permittedThemeFile)
		}

		// The WIDTH half stays package-scoped, deliberately. `.Width(` is an
		// ordinary lipgloss call that any other package may legitimately make;
		// what §6.1 forbids is handing the ALLOCATOR'S computed total to the
		// renderer, and that only happens here. A repo-wide `.Width(` scan
		// would be a different, much noisier rule than the one §6.1 argues
		// for. The COLOUR half above is repo-wide, because §6.8 is.
		pinned, guarded, err := scanWidthPinning(".")
		if err != nil {
			r.fail("anti_drift", "width scan failed: %v", err)
			return
		}
		for _, v := range pinned {
			r.fail("anti_drift", "a width is handed to the renderer outside the mutation switch at %s; "+
				"with that call in place the chrome off-by-one stops reddening width_budget entirely "+
				"(14220 violations without it, 0 with it)", v)
		}
		if guarded != 1 {
			r.fail("anti_drift", "%d guarded .Width( call(s) in the package; the mutation switch that "+
				"proves the paired assertion is expected to be the one and only", guarded)
		}
	},
}

// TestAntiDriftGuardCanFail is the guard's own control. No runtime mutant can
// redden a source-level assertion, so the assertion is proved able to fail the
// only way it can be: by planting the drift it exists to catch and showing the
// scanner reports it — at the right file and the right line.
//
// Measured on this tree, REPO-WIDE and not merely in this package:
//
//	$ grep -rn 'lipgloss.Color("#' --include='*.go' . | grep -v _test.go | sed 's/:[0-9]*:.*//' | sort | uniq -c
//	     19 ./internal/output/theme.go
//	$ find . -name theme.go -not -path './.git/*'
//	./internal/output/theme.go
//
// 19 matches, all in the permitted file, zero outside it, and exactly one file
// in the tree called theme.go. (An earlier draft pinned 18; the palette gained
// a value since.) Five further matches are in TEST files — one in
// mutation_test.go and four in this one — and the scanner skips every one of
// them by the `_test.go` suffix. The assertion is therefore GREEN before any
// of this work happens. Without this control it would be an assertion that
// cannot fail — and while it was rooted at "." (the package directory inside a
// Go test) it was also an assertion that could not SEE the drift, because §6.8
// and §7 both speak about the repository, where the ten direct-style sites
// under cmd/ live.
func TestAntiDriftGuardCanFail(t *testing.T) {
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
	// A REPOSITORY shape, not a package one. The guard is repo-wide, the
	// permit is one exact path, and the drift §7 measures lives under cmd/ —
	// a control built out of a flat directory could not tell a repo-rooted
	// scan from the package-rooted one this replaced.
	write("go.mod", "module example.invalid/x\n")
	write("internal/output/theme.go", "package output\n\nvar ok = lipgloss.Color(\"#b8bb26\")\n")
	write("internal/output/clean.go", "package output\n\nvar x = 1\n")
	write("internal/output/drift_test.go", "package output\n\nvar t = lipgloss.Color(\"#fabd2f\")\n")
	write("cmd/clean.go", "package cmd\n\nvar y = 2\n")
	// testdata is walked, not skipped (see scanColourLiterals). A clean file
	// there proves only that the walk does not choke on it; the planted one
	// below proves the walk reaches it.
	write("internal/output/testdata/clean.go", "package testdata\n\nvar z = 3\n")

	violations, inPermitted, err := scanColourLiterals(dir, permittedThemeFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("clean tree reported %v", violations)
	}
	// The other direction: theme.go's own literal must still be SEEN, or the
	// guard could pass because the palette vanished.
	if inPermitted != 1 {
		t.Fatalf("theme.go literal not seen: %d", inPermitted)
	}

	// Plant the drift the guard exists to catch, OUTSIDE internal/output.
	// This is the case the package-rooted scan could not see at all.
	write("cmd/root.go", "package cmd\n\nvar drift = lipgloss.Color(\"#fb4934\")\n")
	violations, _, err = scanColourLiterals(dir, permittedThemeFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0] != "cmd/root.go:3" {
		t.Fatalf("planted drift under cmd/ not reported at cmd/root.go:3: %v", violations)
	}
	t.Logf("planted drift reported at %s", violations[0])

	// And a decoy named theme.go somewhere else must NOT be permitted: the
	// permit is one path, not a basename.
	write("cmd/theme.go", "package cmd\n\nvar decoy = lipgloss.Color(\"#83a598\")\n")
	violations, _, err = scanColourLiterals(dir, permittedThemeFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 2 {
		t.Fatalf("a second file named theme.go was exempted by basename: %v", violations)
	}
	t.Logf("a decoy theme.go outside the permitted path is reported: %v", violations)

	// And a colour literal inside testdata IS reported, which is what dropping
	// testdata from the skip list buys. Measured, not argued: putting
	// `|| name == "testdata"` back into scanColourLiterals makes this very
	// assertion fail with "a colour literal under testdata was skipped:
	// [cmd/root.go:3 cmd/theme.go:3]" — two where three are required.
	write("internal/output/testdata/drift.go", "package testdata\n\nvar d = lipgloss.Color(\"#d3869b\")\n")
	violations, _, err = scanColourLiterals(dir, permittedThemeFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 3 {
		t.Fatalf("a colour literal under testdata was skipped: %v", violations)
	}
	t.Logf("a colour literal under testdata is reported: %v", violations)
}

// TestWidthPinningGuardCanFail is the width guard's control, proved the only
// way a source-level assertion can be: by planting the call it exists to
// catch; by planting the GUARDED shape and showing the scanner accepts that
// one, so the guard is not simply refusing every file it is shown; and by
// planting the NEAR MISS — guarded by the right condition but one line too far
// away, which is how a re-introduction would most plausibly look.
//
// THE PLANTED CALL GOES IN A NON-TEST FILE, and that is the whole point.
// scanWidthPinning skips `_test.go`, so a control planted in a test file could
// never be reported however wrong it was, and this test would be an assertion
// that cannot fail. The test-file plant below is here for the opposite reason:
// this package really has 7 `.Width(` calls in stream_contract_test.go, every
// one of them `(*Stream).Width()` — the layer's own accessor, nothing to do
// with lipgloss — and the guard must go on ignoring them.
//
// Measured over the real tree, as the two halves of the guard define them:
// non-test `.Width(` = 3 raw matches over 2 files, of which 1 is the guarded
// call at blocks.go:122 and 2 are prose (blocks.go:96, mutants.go:74), so the
// scanner reports no violations and guarded == 1. Test-file `.Width(` = 29
// matches over 5 files, every one skipped by the `_test.go` suffix: 7 in
// stream_contract_test.go, 16 in this file's own planted fixtures and prose,
// and 6 across acceptance_test.go, disclosure_test.go and mutation_test.go.
// That test-side total moves whenever this file is edited, which is the point
// of writing the breakdown down rather than the sum alone.
func TestWidthPinningGuardCanFail(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The shape the package really has: one guarded call, one comment about
	// it, and a test file that may say anything at all.
	write("blocks.go", "package output\n\n"+
		"// Nothing is handed to lipgloss as `.Width(total)`: it re-fits the grid.\n"+
		"func render() {\n\tif mutants.PinTableWidth {\n\t\tgrid = grid.Width(a.Total)\n\t}\n}\n")
	write("pinning_test.go", "package output\n\nvar x = grid.Width(80)\n")

	violations, guarded, err := scanWidthPinning(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("the guarded call and the comment were reported: %v", violations)
	}
	if guarded != 1 {
		t.Fatalf("the guarded call was not counted: %d", guarded)
	}

	// Plant the pinning the guard exists to catch, in a NON-TEST file.
	write("table.go", "package output\n\nfunc draw() {\n\tgrid = grid.Width(a.Total)\n}\n")
	violations, _, err = scanWidthPinning(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.HasSuffix(violations[0], "table.go:4") {
		t.Fatalf("planted width pinning not reported: %v", violations)
	}
	t.Logf("planted width pinning reported at %s", violations[0])

	// And the near miss.
	write("table.go", "package output\n\nfunc draw() {\n\tif mutants.PinTableWidth {\n\t\tx := 1\n\t\tgrid = grid.Width(a.Total)\n\t}\n}\n")
	violations, _, err = scanWidthPinning(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("a call two lines below the guard was accepted: %v", violations)
	}
	t.Logf("a .Width( call not directly under the switch is reported at %s", violations[0])
}

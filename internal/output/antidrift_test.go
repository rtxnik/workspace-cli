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

// ------------------------------------------- §4.1 / §6.6: no parallel tests
//
// stream_contract_test.go writes the invariant down: no test in this package
// may call t.Parallel(). withStdStreams reassigns os.Stdout and os.Stderr, and
// this package carries three more process globals its own tests mutate —
// `mutants` (mutants.go), where a leaked plant makes a deliberate defect the
// shipped behaviour of every later test in the run; `allocStats`
// (alloc_policy_test.go), which TestAcceptanceSweep zeroes and then reads back
// as a non-zero count; and `fxCorpusOnce` (corpus_test.go), an unsynchronised
// memo.
//
// HALF THE HAZARD IS CAUGHT FOR FREE AND HALF IS NOT, which is what makes a
// source scan the only guard available. Measured in stream_contract_test.go: a
// t.Parallel() in a subtest that also calls t.Setenv panics on the Go
// runtime's own rule, while one in a test that swaps the same globals and
// calls no t.Setenv — TestStreamMemoisationIsRaceFree is the live example — is
// accepted silently and the package stays green under -race. So the failure a
// guard has to catch is the one that arrives with no message naming its cause.
//
// parallelCall is spelled in two pieces deliberately. This scanner reads the
// _test.go files of THIS package and antidrift_test.go is one of them, so a
// single literal in the code below would make the guard report its own source.
// Prose is free to spell it out: the comment skip covers that, and the four
// mentions in stream_contract_test.go are the reason the skip exists.
const parallelCall = "." + "Parallel("

// scanParallelTests reports every _test.go file under root that calls
// t.Parallel().
//
// IT IS THE MIRROR OF scanWidthPinning, NOT A COPY. That scanner reports
// non-test files and skips `_test.go`; this one reports `_test.go` and skips
// everything else, because the rule each enforces lives on the opposite side
// of that line. A guard that read the whole tree would report nothing extra —
// production code has no reason to name the call — and would lose the one
// property that makes this cheap: the file set it reads is the file set the
// rule is about.
//
// Occurrences inside a line comment are not calls, and the skip is not
// optional: this package states the invariant in prose twice over — in
// stream_contract_test.go, which writes it down, and in this file, which
// enforces it — so a comment-blind scan reports the documentation and nothing
// else. Measured at this commit, every `.Parallel(` match in the package's
// test files sits behind a `//`, the scanner reports no violations, and that
// zero is the only number assertAntiDrift reads back. No tally of the prose
// matches is pinned here: it moves whenever one of those comments is edited
// and nothing consumes it. (stream_contract_test.go:329 names t.Parallel
// without its parenthesis and is not matched by the call shape at all.)
//
// Named return `found`, for the same reason the two scanners above have one.
func scanParallelTests(root string) (found []string, err error) {
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			at := strings.Index(line, parallelCall)
			if at < 0 {
				continue
			}
			if c := strings.Index(line, "//"); c >= 0 && c < at {
				continue // prose about the call, not the call
			}
			found = append(found, filepath.ToSlash(path)+":"+strconv.Itoa(i+1))
		}
		return nil
	})
	return found, err
}

// assertAntiDrift is §6.8 and §6.1's source-level half.
//
// Goes red when: any file other than theme.go names a colour value — and, in
// the OTHER direction, when theme.go stops naming any, which would make the
// guard pass for the wrong reason; or when a width is handed to a renderer
// anywhere outside the mutation switch; or when a test file in this package
// calls t.Parallel().
//
// That is FIVE reds, and every one of them is planted against the scanners in
// TestAntiDriftGuardCanFail, TestWidthPinningGuardCanFail and
// TestParallelCallGuardCanFail rather than assumed — a colour literal outside
// theme.go, a theme.go that names none, an unguarded .Width(, a .Width( one
// line too far below its guard, and a _test.go file that calls t.Parallel().
// No runtime mutant can redden an assertion about source text, so the
// planted-source controls are the only way this body is shown able to fail.
var assertAntiDrift = globalAssertion{
	name: "anti_drift",
	spec: "§6.8 / §6.1",
	what: `lipgloss.Color("#` + " appears in no non-test file outside theme.go and does appear in theme.go; " +
		".Width( appears in no non-test file outside the mutants.PinTableWidth branch; " +
		"no _test.go file in this package calls t" + parallelCall + ")",
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

		// The PARALLEL half is package-scoped and reads the _test.go files —
		// the opposite of the width half on both axes. The invariant is about
		// what this package's own tests do to process globals, so the file set
		// it reads is the file set the rule is about.
		racy, err := scanParallelTests(".")
		if err != nil {
			r.fail("anti_drift", "parallel scan failed: %v", err)
			return
		}
		for _, v := range racy {
			r.fail("anti_drift", "a test calls t%s) at %s; os.Stdout, os.Stderr, mutants, allocStats "+
				"and fxCorpusOnce are process globals this package's tests swap, and a concurrent "+
				"test corrupts them intermittently, under another test's name, with no message "+
				"naming this cause", parallelCall, v)
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

	// THE OTHER DIRECTION, PLANTED RATHER THAN REASONED ABOUT. assertAntiDrift
	// fails when theme.go stops naming any colour at all, because a guard that
	// found nothing because the pattern was nowhere in the tree would pass
	// over a package that had lost its palette entirely. Nothing above this
	// line exercises that clause: every stage so far hands the scanner a
	// theme.go that HAS a literal, so `inPermitted == 0` was an assertion no
	// stage of this control could reach.
	//
	// The violations count is re-asserted alongside it, because emptying the
	// permitted file must not change what is reported outside it.
	write("internal/output/theme.go", "package output\n\nvar ok = 1\n")
	violations, inPermitted, err = scanColourLiterals(dir, permittedThemeFile)
	if err != nil {
		t.Fatal(err)
	}
	if inPermitted != 0 {
		t.Fatalf("a palette-less theme.go was still counted as naming %d colour literal(s)", inPermitted)
	}
	if len(violations) != 3 {
		t.Fatalf("emptying the permitted file changed what is reported outside it: %v", violations)
	}
	t.Logf("a theme.go naming no colour literal reports inPermitted=0, which is the input " +
		"assertAntiDrift's vacuity clause reads")
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
// this package's test files call `(*Stream).Width()` — the layer's own
// accessor, nothing to do with lipgloss — and the guard must go on ignoring
// them however many of them there are.
//
// Measured over the real tree, as the guard's two ASSERTED numbers define
// them: non-test `.Width(` = 3 raw matches over 2 files, of which 1 is the
// guarded call at blocks.go:122 and 2 are prose (blocks.go:96, mutants.go:74)
// — so scanWidthPinning returns len(violations) == 0 and guarded == 1, which
// is exactly the pair assertAntiDrift reads back.
//
// NO TEST-SIDE TALLY IS PINNED HERE, and that is a correction rather than an
// omission. An earlier draft carried a per-file count of `.Width(` over the
// test files; it was re-pinned two commits before the end of this phase and
// falsified in the same file by the next commit, which added two more matches
// to THIS ONE. The scanner drops every `_test.go` by suffix before the line
// scan runs, so no assertion in this package consumes that number — and a
// figure nothing reads is a figure that can only rot.
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

// TestParallelCallGuardCanFail is the parallel guard's control, and it mirrors
// TestWidthPinningGuardCanFail on the axis that decides whether a control is a
// control: THE PLANTED CALL GOES IN A _test.go FILE, because that is the only
// kind scanParallelTests reads. A plant in a non-test file could never be
// reported however wrong it was, and this test would be an assertion that
// cannot fail — the exact mistake the width control avoids by planting in the
// other direction.
//
// The live baseline is ZERO: measured on this tree, no file in this repository
// calls t.Parallel(). So the clause assertAntiDrift runs has nothing real to
// go red on, and a planted tree is the only way it is ever exercised. The
// plants go in a t.TempDir(); the real tree is never written to.
//
// Four shapes are planted, and the pair that is NOT reported matters as much
// as the pair that is: prose naming the call, a non-test file making it, the
// call itself, and the call followed by a trailing comment — which is the
// shape a skip keyed on "does this line have a //" would wave through.
func TestParallelCallGuardCanFail(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	call := "\tt" + parallelCall + ")"

	// The shape the package really has: the invariant stated in prose in a
	// test file, and a non-test file this scanner does not read at all.
	write("stream_contract_test.go", "package output\n\n// NO TEST IN THIS PACKAGE MAY CALL t"+parallelCall+").\n")
	write("stream.go", "package output\n\nfunc f(t *testing.T) {\n"+call+"\n}\n")

	violations, err := scanParallelTests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("prose in a test file, or a non-test file, was reported: %v", violations)
	}

	// Plant the call the guard exists to catch, in a _test.go file.
	write("racy_test.go", "package output\n\nfunc TestRacy(t *testing.T) {\n"+call+"\n}\n")
	violations, err = scanParallelTests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.HasSuffix(violations[0], "racy_test.go:4") {
		t.Fatalf("planted parallel call not reported at racy_test.go:4: %v", violations)
	}
	t.Logf("planted parallel call reported at %s", violations[0])

	// And the same call with a comment BEHIND it. The skip tests where the
	// "//" sits, not whether the line carries one; keyed the other way, this
	// is how the call comes back.
	write("racy_test.go", "package output\n\nfunc TestRacy(t *testing.T) {\n"+call+" // the globals are fine\n}\n")
	violations, err = scanParallelTests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.HasSuffix(violations[0], "racy_test.go:4") {
		t.Fatalf("a call with a trailing comment was waved through: %v", violations)
	}
	t.Logf("a call with a comment behind it is still reported at %s", violations[0])
}

//go:build mutation

package output

import (
	"sort"
	"strconv"
	"strings"
	"testing"
)

// §6.1's mutation harness.
//
// It plants one defect at a time, re-runs every assertion in the acceptance
// registry — not a second, weaker copy of them — and produces the coverage
// table in BOTH directions: for every mutant, which assertions killed it; and
// for every assertion, which mutants reddened it. An assertion no mutant can
// redden is reported BY NAME, because an assertion that cannot fail is worse
// than no assertion.
//
// The file carries `//go:build mutation` (decision D-11) and mutants.go never
// does: production code branches on the switches, so tagging the DECLARATION
// out breaks the ordinary build, while tagging the HARNESS out keeps a sweep
// per mutant out of `go test -race ./...`. The `test-mutation` target in the
// Makefile and the `mutation` job in .github/workflows/ci.yml are what keep it
// a hard gate. That target carries NO `-run` filter, on purpose: the tag is
// what brings this file into the run at all and should be the only thing
// deciding what runs there. Measured — the filter an earlier draft used runs
// exactly three tests, and TestMutantSwitchesAreRestored below is not one of
// them.
//
// THE TAG ALSO HIDES THIS FILE FROM THE LINTER, AND THE CI JOB UNDOES THAT.
// `golangci-lint run` never compiles a build-tagged file, so the repository's
// ordinary lint job says nothing about anything below this line. The
// `lint-mutation` Makefile target is this file's only lint, and the `mutation`
// job runs that exact target before it runs the harness.
//
// "0 issues" from the tagged run does NOT on its own prove the flag reached
// the analyser — it is equally consistent with the flag being dropped — so it
// was planted. With an ineffectual assignment added to this file, plain
// `golangci-lint run` stays at `0 issues` and exits 0, while
// `golangci-lint run --build-tags mutation` reports `ineffectual assignment to
// n (ineffassign)` and `func zzLintCanary is unused (unused)` and exits 1.
// Clean, the tagged run reports 0 issues.

// ------------------------------------------------------- rule 3's enforcement

// withMutant applies one switch set for the duration of fn and RESTORES THE
// ZERO VALUE afterwards, through t.Cleanup as well as on the normal return, so
// that an fn abandoned by t.Fatal still cannot leave a defect switched on for
// the rest of the run. EVERY PLANT IN BOTH HARNESSES GOES THROUGH HERE, and a
// switch turned on anywhere else in this file is the thing mutants.go's rule 3
// forbids. The only other assignments to `mutants` here are the four entry
// guards' `t.Cleanup(func() { mutants = mutantSwitches{} })`, which restore
// rather than plant.
//
// The two restores are not redundant and TestMutantSwitchesAreRestored proves
// each separately. The TRAILING one is what lets a harness plant in a loop: a
// t.Cleanup only runs when the test ends, so without it the second iteration
// would start from the first iteration's switches. The CLEANUP is what covers
// the path the trailing one cannot reach, an fn that calls runtime.Goexit.
//
// It also forces the corpus to be BUILT before the first plant. fxCorpus is
// memoised and fxCorpusBuild panics when it is reached with a switch on — a
// guard written for exactly this caller, because `go test -run <one mutation
// test>` reaches the builder for the first time from inside a mutant window.
// Measured with the fxCorpus line removed: `go test -tags mutation -run
// TestPairedAssertionCatchesWhatTheSweepCannot` panics with "the corpus is
// being built with mutation switches on".
//
// THE ORDER OF THE FIRST THREE STATEMENTS IS THE POINT. The Cleanup is
// registered before anything can plant; the zero value is restored before the
// corpus is built, so the build is guaranteed clean rather than merely clean
// in every caller that exists today; and only then is the switch set. Building
// the corpus before that reset would leave the panic reachable from any future
// caller that arrived dirty, and in CI that panic is a build failure rather
// than a test failure.
func withMutant(t *testing.T, set func(m *mutantSwitches), fn func()) {
	t.Helper()
	t.Cleanup(func() { mutants = mutantSwitches{} })
	mutants = mutantSwitches{}
	_ = fxCorpus()
	set(&mutants)
	fn()
	mutants = mutantSwitches{}
}

// TestMutantSwitchesAreRestored is rule 3's detector. It is not decorative:
// without it each restore is a line of code nothing exercises, and the failure
// it prevents — a leaked switch making a deliberate defect the shipped
// behaviour of every later test in the run — is invisible, because every
// assertion downstream simply agrees with the mutated code.
//
// Both halves were planted rather than argued:
//
//	trailing restore deleted -> "not restored after a plant that returned:
//	                             {ChromeOff:0 … NoStreamMemo:true …}"
//	t.Cleanup deleted        -> "not restored after a plant that was abandoned:
//	                             {ChromeOff:0 … NoStreamMemo:true …}"
func TestMutantSwitchesAreRestored(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("the mutation switches were not at their zero value on entry: %+v", mutants)
	}
	t.Cleanup(func() { mutants = mutantSwitches{} })

	// (1) the trailing restore. A plant that returns normally is clean again
	//     before withMutant returns, which is what the loops below depend on;
	//     the t.Cleanup registered by this call cannot have run yet, because
	//     this test has not ended.
	applied := false
	withMutant(t, func(m *mutantSwitches) { m.NoStreamMemo = true }, func() {
		applied = mutants.NoStreamMemo
	})
	if !applied {
		t.Fatal("the switch was not applied; this test cannot prove either restore")
	}
	if mutants != (mutantSwitches{}) {
		t.Fatalf("not restored after a plant that returned: %+v", mutants)
	}

	// (2) the t.Cleanup restore. The plant is abandoned mid-fn, so withMutant's
	//     trailing restore never runs and only the Cleanup can put the zero
	//     value back. t.SkipNow rather than t.Fatal: both mark the test and
	//     then call runtime.Goexit, which is the property under test, but a
	//     skipped subtest does not fail its parent and a failed one does.
	subApplied := false
	t.Run("abandoned plant", func(t *testing.T) {
		withMutant(t, func(m *mutantSwitches) { m.NoStreamMemo = true }, func() {
			subApplied = mutants.NoStreamMemo
			t.SkipNow()
		})
		t.Error("unreachable: t.SkipNow returned")
	})
	if !subApplied {
		t.Fatal("the switch was not applied in the subtest; this test cannot prove the Cleanup")
	}
	if mutants != (mutantSwitches{}) {
		t.Fatalf("not restored after a plant that was abandoned: %+v", mutants)
	}
}

// ------------------------------------------------------------- the roster

type mutant struct {
	name string
	spec string
	// defect names the change to internal/output the switch stands in for.
	defect string
	apply  func(m *mutantSwitches)

	// vacuousBecause is set only for a switch MEASURED not to change the
	// render on its own. Declaring it is not a way to excuse a mutant that
	// nothing catches: the harness asserts such a switch really is vacuous and
	// goes red if it ever starts perturbing the output, and it is never
	// counted as killed. Any mutant WITHOUT this field that fails to perturb
	// the render fails the harness outright.
	vacuousBecause string
}

func corpusMutants() []mutant {
	return []mutant{
		{
			name:   "rune_vs_cell",
			spec:   "§6.1 control",
			defect: "W() counts runes instead of display cells (utf8.RuneCountInString), so every CJK cell is measured at half its width",
			apply:  func(m *mutantSwitches) { m.RuneWidth = true },
		},
		{
			name: "truncation_counts_runes_not_clusters",
			spec: "§6.1 control / §4.3 budget contract",
			defect: "the cut primitives step by rune while the budget contract measures display cells of grapheme " +
				"clusters, so an emoji-presentation sequence (base + U+FE0F: 1 + 0 per rune, 2 as a cluster) is " +
				"cut at one cell of budget and drawn at two. Measured on U+26A0 U+FE0F: firstCell returns " +
				"(\"⚠\", 1) with the switch on against (\"⚠️\", 2) clean. Distinct from rune_vs_cell, " +
				"which mutates W itself: " +
				"here the measure is correct and only the stepper is wrong, so it is invisible on every fixture " +
				"whose runes are its cells",
			apply: func(m *mutantSwitches) { m.RuneSegmentation = true },
		},
		{
			name:   "chrome_off_by_one",
			spec:   "§6.1 control",
			defect: "chromeFor undercounts the border and padding by one cell (3n+1 becomes 3n)",
			apply:  func(m *mutantSwitches) { m.ChromeOff = 1 },
		},
		{
			name:   "marker_not_reserved",
			spec:   "§6.1 control",
			defect: "truncation reserves no room for the marker: markerWidth returns 0, so the marker is appended past the allocation",
			apply:  func(m *mutantSwitches) { m.NoEllipsisReserve = true },
		},
		{
			name:   "ascii_marker_mismeasured",
			spec:   "§6.1 control / §4.5",
			defect: `the three-cell ASCII marker "..." is emitted while one cell is reserved for it`,
			apply:  func(m *mutantSwitches) { m.ASCIIMarkerMismeasure = true },
		},
		{
			name:   "indent_ignored_when_wrapping_values",
			spec:   "§6.1 control / §4.4",
			defect: "KV and Problem.Facts values are wrapped at the full budget, ignoring the value column they are printed at",
			apply:  func(m *mutantSwitches) { m.PairsIndent = true },
		},
		{
			name:   "caption_wrapped_too_wide",
			spec:   "§6.1 control / §4.3",
			defect: "the caption is wrapped two cells wider than the budget",
			apply:  func(m *mutantSwitches) { m.CaptionWidth = 2 },
		},
		{
			name:   "message_wrapping_disabled",
			spec:   "§6.1 (the message call sites)",
			defect: "the message helpers emit one unwrapped line, which is what they did before this layer existed",
			apply:  func(m *mutantSwitches) { m.NoMessageWrap = true },
		},
		{
			name:   "lipgloss_width_pinning",
			spec:   "§6.1 paired assertion / §8",
			defect: "the allocator's total is handed to lipgloss as .Width(total), which re-fits the grid and absorbs arithmetic errors as content loss",
			apply:  func(m *mutantSwitches) { m.PinTableWidth = true },
			vacuousBecause: "measured byte-identical to the clean corpus over the whole sweep: while the " +
				"allocator's arithmetic is right, re-fitting the grid to the width it already has changes " +
				"nothing. That is precisely why pinning is dangerous — it is invisible until it is hiding " +
				"another defect, which is what the combined mutants below demonstrate",
		},
		{
			name: "chrome_over_by_one",
			spec: "§6.1 paired assertion",
			defect: "chromeFor OVERcounts the border and padding by one cell (3n+1 becomes 3n+2), so the " +
				"allocator reserves a cell the renderer never draws — a defect that makes the render NARROWER " +
				"than the budget, which the overflow sweep cannot see at all",
			apply: func(m *mutantSwitches) { m.ChromeOff = -1 },
		},
		{
			name: "chrome_over_by_one+lipgloss_width_pinning",
			spec: "§6.1 paired assertion / §8",
			defect: "the over-count WITH lipgloss pinning: lipgloss stretches the grid to the width it is " +
				"handed, so the columns no longer have the widths the allocator assigned them",
			apply: func(m *mutantSwitches) { m.ChromeOff, m.PinTableWidth = -1, true },
		},
		{
			name: "relax_skips_atomic_squeeze",
			spec: "§4.3 step 5(a)",
			defect: "step 5(a) is removed: Atomic columns keep their natural width and the deficit is taken " +
				"out of a column's Min instead. Byte-identical to the clean render on every fixture that " +
				"never reaches step 5",
			apply: func(m *mutantSwitches) { m.NoAtomicSqueeze = true },
		},
		{
			name:   "relax_order_swapped",
			spec:   "§4.3 step 5",
			defect: "step 5(b) runs before step 5(a), so a column goes below its Min while an Atomic column still has slack above its own",
			apply:  func(m *mutantSwitches) { m.RelaxOrderSwapped = true },
		},
		{
			name: "relax_floor_is_one",
			spec: "§4.3 step 5(b) / accepted review finding #4",
			defect: `step 5(b)'s floor drops from max(1, markerWidth(mode)) back to 1, so in ASCII mode a ` +
				`column can be allocated one cell for a three-cell "..." marker`,
			apply: func(m *mutantSwitches) { m.RelaxFloorOne = true },
		},
		{
			name: "state_column_not_exempt_from_5b",
			spec: "§4.3 step 5(b) / §4.5",
			defect: "the ColState exemption is removed from step 5(b): a state column is shaved below its Min " +
				"instead of being dropped. Measured first violation: `table/degenerate-state-floor @ 29 " +
				"(mode 0): state column \"STATUS\" allocated 11, below its Min 12` — the Min §4.5's " +
				"vocabulary sets so that a state's mark AND its word both fit",
			apply: func(m *mutantSwitches) { m.NoStateExemption = true },
		},
		{
			name: "drop_against_natural_sum",
			spec: "§4.3 step 2",
			defect: "step 2 is worded against the natural sum instead of the Min sum — the wording §4.3 records " +
				"as implemented and measured wrong: a table sheds a whole column where shaving a column's " +
				"slack down to its Min would have kept it",
			apply: func(m *mutantSwitches) { m.DropAgainstNatural = true },
		},
		{
			name:   "caption_hides_what_it_dropped",
			spec:   "§4.3 disclosure",
			defect: "the caption keeps the caller's text and drops the generated Hidden/Narrowed clause, so a column vanishes silently",
			apply:  func(m *mutantSwitches) { m.NoCaptionDisclosure = true },
		},
		{
			name: "hardcoded_wide_flag",
			spec: "§4.4 / accepted review finding #12",
			defect: `" (--wide)" is written into the Hidden clause regardless of Table.WideFlag, so the caption ` +
				`advertises a flag the command does not have — the defect accepted review finding #12 exists ` +
				`to prevent. Killed by caption_discloses. The table/no-wide-flag fixture is NOT what produces ` +
				`that kill, and tableFixtures() in acceptance_test.go carries the measurement: 10 violations ` +
				`without the fixture, 28 with it, so the mutant dies on the degenerate unflagged literals ` +
				`alone. What the fixture adds is the honest case — a table a command could actually build, ` +
				`dropping a column, with no flag to name, of which the corpus had none before it`,
			apply: func(m *mutantSwitches) { m.HardcodedWideFlag = true },
		},
		{
			name: "style_painted_before_fit",
			spec: "§4.3 style ordering",
			defect: `the cell's Role is applied BEFORE truncation and padding, so a cut can land inside an ` +
				`SGR sequence and the padding falls outside the painted run. The corpus is not blind to it: ` +
				`sweepStream renders at ColourTrue deliberately, and a painted cell that has to be cut comes ` +
				`back SHORT OF ITS ALLOCATION. Measured first violations, planted over the whole sweep: ` +
				`grid_pairing "table/list @ 29 (mode 0): \"STATUS\" allocated 9 cells but rendered 1 (\"…\")" ` +
				`and state_mark_and_word "table/list @ 29 (mode 0): squeezed state cell \"…\" lost its mark ` +
				`\"~\"". TestStyleIsAppliedAfterAllocation in disclosure_test.go is the assertion that reads ` +
				`WHERE the escapes fall rather than how wide the line is, which is the half of the defect no ` +
				`width assertion can see`,
			apply: func(m *mutantSwitches) { m.PaintBeforeFit = true },
		},
		{
			name:   "truncate_instead_of_wrap",
			spec:   "§4.4",
			defect: "Wrap emits one truncated line, so every block outside the grid loses the characters past the budget instead of wrapping them",
			apply:  func(m *mutantSwitches) { m.TruncateInsteadOfWrap = true },
		},
		{
			name: "chrome_off_by_one+lipgloss_width_pinning",
			spec: "§6.1 paired assertion / §8",
			defect: "the chrome defect WITH lipgloss pinning: the measured case where the bare width sweep sees " +
				"nothing because lipgloss silently shrinks a column instead of overflowing",
			apply: func(m *mutantSwitches) { m.ChromeOff, m.PinTableWidth = 1, true },
		},
	}
}

// mutationRun is what one mutant did.
type mutationRun struct {
	mutant    mutant
	perturbed bool
	digest    string
	red       []string
	counts    map[string]int
}

func (run mutationRun) vacuous() bool { return !run.perturbed }

// runCorpusWithAssertions runs the full sweep and the global assertions and
// returns both the assertion outcome and a digest over every rendered byte.
//
// IT IS CALLED ONCE PER MUTANT, AND runSweep CALLS fxCorpus() EVERY TIME. That
// is only safe because corpus_test.go MEMOISES fxCorpus: the corpus builder
// goes through NewTableBlock -> validateCols, whose chrome is
// chromeFor(n) = 3n+1-mutants.ChromeOff, so a corpus rebuilt inside the mutant
// window would be built against a mutated constant. fxCorpusBuild refuses that
// outright — it panics when the switches are dirty — and withMutant forces the
// build before the first plant so the refusal is never reached.
//
// THE WIDTH RANGE IS THE ACCEPTANCE SWEEP'S, AND NARROWING IT IS A SAVING THAT
// HAS ALREADY BEEN MEASURED AND REJECTED. The cost is linear in the width
// count and nothing else: over the complete roster, MinWidth..200 (172 widths)
// runs in 93.06s and MinWidth..114 (86 widths) in 47.67s — 4.23s against
// 2.15s per mutant. BOTH DIRECTIONS OF THE COVERAGE TABLE CAME BACK IDENTICAL,
// diffed with the per-assertion counts stripped: the same mutants killed, by
// the same assertions, with the same one declared vacuous and none surviving.
// So the saving is real, and the null result is recorded here rather than left
// to be re-derived by the next reader who notices the runtime. It is declined
// because the harness's claim is that it re-runs the registry over THE SWEEP
// THAT SHIPS: TestAcceptanceSweep renders MinWidth..sweepMaxWidth, and a
// harness grading a narrower sweep would print a coverage table describing a
// sweep nothing runs.
func runCorpusWithAssertions() (*results, sweepStats) {
	r := newResults()
	st := runSweep(MinWidth, sweepMaxWidth, sweepAssertions(), r)
	for _, a := range globalAssertions() {
		a.check(r)
	}
	return r, st
}

func TestMutationHarness(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("the mutation switches were not at their zero value on entry: %+v", mutants)
	}
	t.Cleanup(func() { mutants = mutantSwitches{} })

	// The clean baseline. Both the digest to compare against and the proof
	// that the assertions are green on the shipped behaviour.
	clean, cleanStats := runCorpusWithAssertions()
	if clean.any() {
		t.Fatalf("the clean corpus is already red (%v); the mutation results below would be noise", clean.redAssertions())
	}
	t.Logf("clean baseline: %d renders, %d lines, %d fixtures, digest %s",
		cleanStats.renders, cleanStats.lines, len(fxCorpus()), cleanStats.digest[:16])

	var runs []mutationRun
	for _, m := range corpusMutants() {
		var r *results
		var st sweepStats
		withMutant(t, m.apply, func() { r, st = runCorpusWithAssertions() })

		run := mutationRun{
			mutant:    m,
			perturbed: st.digest != cleanStats.digest,
			digest:    st.digest,
			red:       r.redAssertions(),
			counts:    map[string]int{},
		}
		for _, name := range run.red {
			run.counts[name] = r.fails[name]
		}
		runs = append(runs, run)

		switch {
		case !run.perturbed && m.vacuousBecause == "":
			// A mutation that changes nothing cannot be killed by anything.
			// Reporting it as killed would be the harness certifying coverage
			// it does not have.
			t.Errorf("VACUOUS MUTANT %s (%s): the render is byte-identical to the clean corpus over "+
				"%d renders — digest %s, the clean one — so no assertion could distinguish it and none "+
				"of the %d assertions below is evidence about it. %s", m.name, m.spec, st.renders,
				st.digest[:16], len(sweepAssertions())+len(globalAssertions()), m.defect)
		case run.perturbed && m.vacuousBecause != "":
			t.Errorf("mutant %s (%s) was declared vacuous but now perturbs the render (digest %s against "+
				"the clean %s); the declaration is stale and the mutant must be re-classified",
				m.name, m.spec, st.digest[:16], cleanStats.digest[:16])
		case run.perturbed && len(run.red) == 0:
			t.Errorf("SURVIVING MUTANT %s (%s): the corpus renders differently (digest %s) but every "+
				"assertion stayed green. %s", m.name, m.spec, st.digest[:16], m.defect)
		}
		if !run.perturbed && m.vacuousBecause != "" {
			t.Logf("VACUOUS (declared, and re-measured here) %s: digest %s, the clean one. %s",
				m.name, run.digest[:16], m.vacuousBecause)
		}
	}

	// The report, both ways round.
	t.Log("")
	t.Log("mutant                                     perturbed  killed by")
	for _, run := range runs {
		killed := "— SURVIVED —"
		if len(run.red) > 0 {
			parts := make([]string, 0, len(run.red))
			for _, name := range run.red {
				parts = append(parts, name+"("+strconv.Itoa(run.counts[name])+")")
			}
			killed = strings.Join(parts, " ")
		}
		if run.vacuous() {
			killed = "— VACUOUS: no output change, not counted as killed —"
		}
		t.Logf("%-42s %-10t %s", run.mutant.name, run.perturbed, killed)
	}

	byAssertion := map[string][]string{}
	for _, run := range runs {
		if run.vacuous() {
			continue
		}
		for _, name := range run.red {
			byAssertion[name] = append(byAssertion[name], run.mutant.name)
		}
	}
	var names []string
	for _, a := range sweepAssertions() {
		names = append(names, a.name)
	}
	for _, a := range globalAssertions() {
		names = append(names, a.name)
	}
	sort.Strings(names)

	t.Log("")
	t.Log("assertion                reddened by")
	var unkilled []string
	for _, name := range names {
		killers := byAssertion[name]
		if len(killers) == 0 {
			unkilled = append(unkilled, name)
			t.Logf("%-24s NO MUTANT — see \"Assertions no runtime switch can redden\" below", name)
			continue
		}
		t.Logf("%-24s %s", name, strings.Join(killers, ", "))
	}
	if len(unkilled) > 0 {
		// Not a failure: some assertions guard defect classes that no runtime
		// switch in mutants.go can express. The table immediately below this
		// function gives each one the source change that would fail it and the
		// control that proves the detector discriminates. They are named here
		// so the gap is REPORTED rather than hidden.
		t.Logf("assertions no runtime mutant reddens: %s", strings.Join(unkilled, ", "))
	}
}

// Assertions no runtime switch can redden, and what does cover them.
//
// The harness prints this set by name at the end of every run rather than
// letting it sit in the suite looking like coverage. Re-measure the set from
// the run; this table explains the ones that have been in it.
//
//	valid_utf8 (§6.3)
//	  Why no switch expresses it: every cut in text.go lands on a whole
//	  grapheme cluster, and RuneSegmentation only weakens that to a whole RUNE.
//	  No flag makes the layer emit a broken rune.
//	  The source change that would fail it: a cut taken in BYTES — s[:n] at an
//	  arbitrary offset, which is literally `tools[:maxTools-1]` at
//	  cmd/profile.go:63 and `func truncate(s string, maxLen int) string` at
//	  cmd/vault_status.go:450, the two measured defects this layer replaces.
//	  Its control: TestDetectorsCanFail — utf8.ValidString accepts valid CJK
//	  and rejects a mid-rune slice.
//
//	esc_containment (§6.7)
//	  Why no switch expresses it: Sanitise has no off switch, and removing its
//	  call from Problem.Cause or from renderMessage is a deletion, not a branch.
//	  The source change that would fail it: dropping Sanitise(...) from
//	  blocks.go's Cause wrap or from renderMessage, or narrowing Sanitise to
//	  strip SGR only.
//	  Its control: TestDetectorsCanFail — escCount and nonSGRSequences report
//	  the ESC-laden fixture and stay silent on plain SGR.
//
//	anti_drift (§6.8 / §6.1)
//	  Why no switch expresses it: it is an assertion about the SOURCE TEXT,
//	  which no runtime value can change.
//	  The source change that would fail it: a lipgloss.Color("#…") added
//	  outside theme.go, theme.go losing its palette, or .Width( re-introduced
//	  on the grid outside the mutants.PinTableWidth branch. Each was planted
//	  against the real tree and observed red, the last in both of its forms —
//	  an unguarded call, and one a line too far below the guard.
//	  Its control: TestAntiDriftGuardCanFail and TestWidthPinningGuardCanFail
//	  in antidrift_test.go, which point the two scanners at planted trees and
//	  require the drift back at the right file and line.
//
// A fourth gap is reported by assertGridPairing's own comment rather than by
// the harness: the clauses that detect a cell count disagreeing with
// Alloc.Kept, and an abbreviation that is neither a head nor a tail of its
// source. No switch in mutants.go produces those shapes. They stay, because
// they catch a future renderer that RE-FLOWS cells rather than re-fitting the
// grid — and they are described as uncovered, not as proven.

// TestPairedAssertionCatchesWhatTheSweepCannot is §6.1's central claim, stated
// as its own test because it is the reason the paired assertion exists:
//
//	"If the renderer passes its computed total to lipgloss as .Width(total),
//	lipgloss re-fits the grid to whatever number it is given, absorbing any
//	arithmetic error as silent content loss instead of overflow."
//
// With the chrome defect and the pinning both in place, the width sweep is
// SILENT — that silence is the claim, so it is asserted here rather than
// logged. A run in which the bare sweep started seeing the pinned defect would
// mean the premise had stopped holding, and this test is the thing that would
// say so.
func TestPairedAssertionCatchesWhatTheSweepCannot(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	t.Cleanup(func() { mutants = mutantSwitches{} })

	// The grid_pairing counts here are LOWER than TestMutationHarness reports
	// for the same two mutants, and the difference is not a discrepancy: two
	// assertion bodies in the full registry report under that one name,
	// because assertStateStructure calls gridFields as well as
	// assertGridPairing does. Measured over this corpus with chrome
	// off-by-one AND pinning planted: assertGridPairing alone 6192,
	// assertStateStructure alone 3096, the whole registry 9288. With the
	// chrome defect alone, assertStateStructure contributes 0 and both
	// numbers are 6192.
	measure := func(apply func(m *mutantSwitches)) (width, pairing int) {
		r := newResults()
		withMutant(t, apply, func() {
			runSweep(MinWidth, sweepMaxWidth, []sweepAssertion{assertWidthBudget, assertGridPairing}, r)
		})
		return r.fails["width_budget"], r.fails["grid_pairing"]
	}

	bareWidth, barePairing := measure(func(m *mutantSwitches) { m.ChromeOff = 1 })
	if bareWidth == 0 {
		t.Errorf("the chrome defect alone produced no overflow; the sweep is not measuring the grid")
	}
	if barePairing == 0 {
		t.Errorf("the chrome defect alone did not break the grid pairing; the paired assertion is not measuring the grid")
	}
	t.Logf("chrome off-by-one, no pinning:   width_budget %d, grid_pairing %d", bareWidth, barePairing)

	pinWidth, pinPairing := measure(func(m *mutantSwitches) { m.ChromeOff, m.PinTableWidth = 1, true })
	if pinPairing == 0 {
		t.Fatalf("with lipgloss pinning the paired assertion saw nothing: the defect is absorbed and undetected, "+
			"which is exactly what §6.1 says the sweep alone cannot catch (sweep saw %d)", pinWidth)
	}
	t.Logf("chrome off-by-one, WITH pinning: width_budget %d, grid_pairing %d", pinWidth, pinPairing)
	if pinWidth != 0 {
		t.Fatalf("the bare width sweep saw %d overflow(s) through lipgloss pinning; §6.1's argument for the "+
			"paired assertion rests on that number being zero, and it is not. Re-measure the argument "+
			"before changing this expectation", pinWidth)
	}
}

// ============================================ the contract mutation harness
//
// The corpus harness above plants a defect and re-renders the corpus. That
// cannot reach §4.7's stream routing, §4.2's width resolution, §4.5's
// glyph-mode selection or §4.8's Die, because none of them appears in a
// render: the sweep builds its streams with NewStreamAt over io.Discard and
// never resolves a file descriptor. A mutant in any of them is VACUOUS against
// the corpus — the bytes do not change — and a harness that reported such a
// mutant as killed would be certifying coverage it does not have.
//
// So those defects are planted against the PROBES of stream_contract_test.go
// and message_test.go instead, on the same three rules: the probe must be
// green clean, the mutant must CHANGE WHAT THE PROBE OBSERVES, and the probe
// must go red.

type contractMutant struct {
	name   string
	spec   string
	defect string
	apply  func(m *mutantSwitches)
	probe  contractProbe
}

func contractMutants() []contractMutant {
	return []contractMutant{
		{
			name: "messages_routed_to_stdout",
			spec: "§4.7",
			defect: "the message helpers write to stdout instead of stderr, so `ws list > out.json` " +
				"interleaves progress chatter with the artifact a script consumes",
			apply: func(m *mutantSwitches) { m.MessagesToStdout = true },
			probe: probeMessageRouting,
		},
		{
			name: "colour_probed_on_stdout",
			spec: "§4.1 / §4.6",
			defect: "every stream's colour capability is resolved from stdout whatever fd it writes to — " +
				"the measured defect that writes raw SGR into a redirected err.log while the message " +
				"helpers go to stderr",
			apply: func(m *mutantSwitches) { m.ColourProbedOnStdout = true },
			probe: probeStreamIdentity,
		},
		{
			name: "streams_not_memoised",
			spec: "§4.1",
			defect: "Out() and Err() are rebuilt on every call instead of being resolved once per process, " +
				"so two call sites hold two streams over one fd and each re-probes it",
			apply: func(m *mutantSwitches) { m.NoStreamMemo = true },
			probe: probeStreamIdentity,
		},
		{
			name: "die_stops_wrapping",
			spec: "§6.1 / §4.8",
			defect: "Die alone stops wrapping — the four helpers the sweep reaches through renderMessage are " +
				"untouched, and so are Die's own mark, role and sanitising: the switch moves the wrap and " +
				"nothing else, so a kill cannot be attributed to a second change",
			apply: func(m *mutantSwitches) { m.DieUnwrapped = true },
			probe: probeDie,
		},
		{
			name: "columns_below_minwidth_rejected",
			spec: "§4.2 / accepted review finding #13",
			defect: "a COLUMNS below MinWidth is rejected and falls through to the probe instead of being clamped, " +
				"so COLUMNS=28 in a pipe renders an unbounded table while COLUMNS=29 renders at 29",
			apply: func(m *mutantSwitches) { m.ColumnsRejectBelowMin = true },
			probe: probeResolveWidth,
		},
		{
			name:   "width_probed_on_fd_zero",
			spec:   "§4.2",
			defect: "the width probe is pointed at fd 0 — stdin — rather than at the stream's own fd",
			apply:  func(m *mutantSwitches) { m.ProbeWrongFd = true },
			probe:  probeResolveWidth,
		},
		{
			name: "cjk_locale_ignored",
			spec: "§4.5",
			defect: "the CJK language tag is ignored, so a ja_JP.UTF-8 or zh_CN.UTF-8 terminal keeps the UTF-8 " +
				"glyph set whose marker and eleven border glyphs it draws at two cells",
			apply: func(m *mutantSwitches) { m.IgnoreCJKTag = true },
			probe: probeGlyphMode,
		},
	}
}

func TestContractMutationHarness(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("the mutation switches were not at their zero value on entry: %+v", mutants)
	}
	t.Cleanup(func() { mutants = mutantSwitches{} })

	// One clean run per probe: the baseline observation, and the proof that
	// the probe is green on the shipped behaviour before anything is planted.
	//
	// Both registries: contractProbes() in stream_contract_test.go carries
	// stream_identity, resolve_width and glyph_mode_selection, and
	// messageProbes() in message_test.go carries message_routing and
	// die_contract. They are separate because the message helpers did not have
	// their §4.7 behaviour when the first registry landed, so a probe over
	// them would have been red at that point's own acceptance gate.
	clean := map[string]string{}
	for _, p := range append(contractProbes(), messageProbes()...) {
		r := newResults()
		clean[p.name] = p.run(t, r)
		if r.any() {
			t.Fatalf("probe %s is already red on the clean package (%v); the results below would be noise",
				p.name, r.redAssertions())
		}
	}

	t.Log("")
	t.Log("contract mutant                            perturbed  killed by")
	for _, m := range contractMutants() {
		var observed string
		r := newResults()
		withMutant(t, m.apply, func() { observed = m.probe.run(t, r) })

		perturbed := observed != clean[m.probe.name]
		switch {
		case !perturbed:
			t.Errorf("VACUOUS MUTANT %s (%s): probe %s observed exactly what it observes clean (%q), "+
				"so nothing could distinguish it. %s", m.name, m.spec, m.probe.name, observed, m.defect)
		case len(r.redAssertions()) == 0:
			t.Errorf("SURVIVING MUTANT %s (%s): probe %s observed %q against a clean %q and stayed green. %s",
				m.name, m.spec, m.probe.name, observed, clean[m.probe.name], m.defect)
		}
		killed := "— SURVIVED —"
		if len(r.redAssertions()) > 0 {
			parts := make([]string, 0, len(r.redAssertions()))
			for _, name := range r.redAssertions() {
				parts = append(parts, name+"("+strconv.Itoa(r.fails[name])+")")
			}
			killed = strings.Join(parts, " ")
		}
		t.Logf("%-42s %-10t %s", m.name, perturbed, killed)
		if len(r.redAssertions()) > 0 {
			t.Logf("    first violation: %s", r.first[r.redAssertions()[0]])
		}
	}
}

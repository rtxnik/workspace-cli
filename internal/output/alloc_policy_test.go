package output

import (
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// §4.3 held against itself is not enough.
//
// The §6.1 paired assertion (assertGridPairing, acceptance_test.go) derives
// its expectation by calling
// Allocate() and then checking the render against what Allocate said. That is
// exactly right for catching a renderer that disagrees with the allocator, and
// it is blind by construction to an allocator that is WRONG BUT CONSISTENT: a
// policy change — dropping a column that did not need dropping, relaxing one
// that had slack left, shaving a column past the marker's width — moves the
// expectation and the render together and nothing goes red. Accepted review
// finding #14's stated purpose was that §6.1 be able to ADJUDICATE between two
// implementations of §4.3; a self-oracle cannot.
//
// So this file holds the allocator against §4.3 read as arithmetic. Two kinds
// of check:
//
//   - INVARIANTS, applied at every width from MinWidth to 200 in both glyph
//     modes. None of them calls Allocate to compute what it expects; they read
//     the Alloc that comes back and re-derive the arithmetic from the Col set,
//     the budget and the spec — the natural widths from the test's own
//     composition, chrome from 3(n−1)+4, the step-5(b) floor from the test's
//     own truncation markers, the drop rule from the Min sum.
//
//   - GOLDENS, hand-written, with the arithmetic spelled out in a workings
//     string so a reviewer can check the number rather than the code. An
//     invariant says a drop was permissible; only a golden says WHICH column
//     went and which of two tied columns paid the odd cell.

// ================================================== THE SHARED HARNESS CORE
//
// Everything between here and "the test's own §4.3" is DECLARED ONLY HERE and
// consumed by Tasks 8, 9, 10, 11 and 12. Go has one package scope across every
// _test.go file in a package, so a second declaration is a compile error — and
// the failure reads like a merge accident rather than the contract breach it
// is. Before adding any of these names in a later task, run:
//
//	grep -n 'type fixture\|type renderCase\|type sweepAssertion\|func newRenderCase' internal/output/*_test.go
//
// `results` is NOT here. Task 4's stream_contract_test.go owns it, because the
// contract probes needed a results sink before the allocator existed. Same for
// `squash` (Task 5) and `fxStateVocabulary`/`fxMark`/`fxBadge` (Task 2).

// fxState is a state occurrence a render must be shown to carry in full
// (§6.5: every rendered state carries its mark AND its word).
type fxState struct {
	st    State
	label string // "" means the state's own default word
}

// fixture is one thing the harness can render, plus everything the assertions
// need to know about it.
//
// It carries cols/rows/wideFlag rather than a *Table on purpose: Table is
// Task 8's type and this task must be provable without a renderer. §4.3's
// policy assertion and §6.1's paired assertion both need only the allocator's
// INPUT — the same Cols and Rows the render used — to re-run Allocate over it.
type fixture struct {
	name string
	kind string // table | problem | empty | kv | checks | message | alloc
	spec string // the §6 or §4 clause that puts this fixture in the corpus

	// The allocator's input, and the two table fields the assertions read
	// back. Set for table fixtures only.
	isTable  bool
	cols     []Col
	rows     [][]Cell
	caption  string
	wideFlag string

	// states must appear in full in the render. Only blocks that never
	// abbreviate a state badge declare them (§6.5); table state cells are held
	// to the weaker, truncation-aware rule inside the paired assertion.
	states []fxState

	// prefixState is declared by the message helpers, whose shape is a mark
	// followed by prose rather than a mark followed by a state word.
	prefixState    State
	hasPrefixState bool

	// fidelity lists the source strings this block WRAPS (§4.4): every one must
	// survive the render whole once line breaks and indents are collapsed.
	// Table fixtures declare none — inside the grid, content is legitimately
	// truncated, and that half is the paired assertion's job.
	fidelity []string

	// render is nil for a fixture that has no renderer attached, which is how
	// the allocator's own fixtures drive assertAllocPolicy: it reads the
	// allocator's input and nothing else.
	render func(s *Stream) string
}

// renderCase is one fixture rendered once, at one width, in one glyph mode.
type renderCase struct {
	fx     fixture
	width  int
	budget int // the width the render is actually held to: §4.2's clamp applied
	mode   GlyphMode
	stream *Stream
	out    string
	lines  []string // as rendered, SGR included
	plain  []string // SGR stripped — what the terminal shows
}

// sweepAssertion is one property, checked on every render of the sweep.
type sweepAssertion struct {
	name  string
	spec  string
	what  string
	check func(rc renderCase, r *results)
}

// fxBudget is the harness's OWN copy of §4.2's clamp: a width below MinWidth is
// clamped up to MinWidth, never rejected. It is not read from the package for
// the same reason fxStateVocabulary is not — an expectation computed by the
// code under test cannot disagree with it.
func fxBudget(w int) int {
	if w < MinWidth {
		return MinWidth
	}
	return w
}

// sweepStream is the widest-open configuration: a TTY at truecolour. Colour is
// ON so that every assertion is forced to strip SGR before measuring, which is
// the same arithmetic the terminal does; running at ColourNone would let a
// render that mismeasures its own escapes pass. It is the single owner of the
// sweep's stream construction: later tasks call it rather than building
// their own stream, so the whole corpus renders in one configuration, and that
// configuration having colour on is what makes a paint-order defect observable
// at all. Do not simplify the level to ColourNone.
func sweepStream(w int, mode GlyphMode) *Stream {
	return NewStreamAt(io.Discard, w, true, ColourTrue, mode == GlyphASCII)
}

// newRenderCase renders one fixture. A fixture with no render function is
// carried through with empty output, so an assertion that reads only the
// allocator's input still works — which is how the allocator's own fixtures
// drive assertAllocPolicy with no renderer attached.
func newRenderCase(fx fixture, w int, mode GlyphMode) renderCase {
	s := sweepStream(w, mode)
	rc := renderCase{fx: fx, width: w, budget: fxBudget(w), mode: mode, stream: s}
	if fx.render == nil {
		return rc
	}
	rc.out = fx.render(s)
	rc.lines = strings.Split(rc.out, "\n")
	rc.plain = make([]string, len(rc.lines))
	for i, l := range rc.lines {
		rc.plain[i] = ansi.Strip(l)
	}
	return rc
}

// ------------------------------------------------------- the test's own §4.3

// fxMarkerUTF8 and fxMarkerASCII are the truncation markers, declared HERE and
// not read back from marker(): the step-5(b) floor IS the marker's width in
// the active mode, and an assertion that asked the package what that width is
// could not fail when the package changes it.
const (
	fxMarkerUTF8  = "…"
	fxMarkerASCII = "..."
)

func fxMarkerCells(mode GlyphMode) int {
	if mode == GlyphASCII {
		return ansi.StringWidth(fxMarkerASCII) // "..." — three cells
	}
	return ansi.StringWidth(fxMarkerUTF8) // "…" — one cell under the narrow convention
}

// fxChrome is §4.3's chrome formula written out: 3(n−1)+4 for n kept columns,
// undefined — and therefore zero — at n = 0.
func fxChrome(n int) int {
	if n <= 0 {
		return 0
	}
	return 3*(n-1) + 4
}

// fxNatural is the test's own natural-width vector, and it exists because
// Alloc.Natural is a product of the code under test. The invariant families
// below are written against the natural widths — the step-1 fit and the
// no-stretch rule, the Min floor of step 3 / 5(b), the step-2 drop-necessity
// reconstruction, the step-5(c) necessity check, the tightness rule and the
// shrunk/squeezed reach counters — and no mutant guards naturalWidths, so
// reading a.Natural there would let a mis-measuring naturalWidths move the
// allocation and the expectation together.
//
// What it re-derives is the COMPOSITION, not the measurement: the widest of a
// column's heading and its cells, with a state cell composed through §4.5's
// test-side vocabulary (fxBadge) rather than through stateText. The width
// primitive is not re-derived and is not claimed to be — W(s) is
// ansi.StringWidth(s) on tab-free text, which is every fixture in the corpus
// but one, so calling one in place of the other proves nothing about it. §6.4's TestGlyphWidths pins the width of the glyphs THIS LAYER
// emits; the width of arbitrary cell content is x/ansi's to get right and
// nothing here re-derives it. The sanitiser is consumed rather than
// re-derived for the same reason, and is pinned by text_invariants_test.go.
//
// THE TAB RULE IS THE ONE THING HERE THAT IS RE-DERIVED. W expands tabs before
// it measures, so naturalWidths does too; fxExpandTabs is the harness's own
// second implementation of that rule, in corpus_test.go, for the same reason
// fxBadge is a second implementation of §4.5. Consuming expandTabs instead
// would let a wrong tab stop move the allocation and the expectation together.
// Measured on table/tab-in-cell with the layer fixed and this line absent:
// 1032 alloc_policy violations, the first reading `natural widths [4 28]; the
// widest of each column's heading and its cells is [4 15]`.
func fxNatural(cols []Col, rows [][]Cell, mode GlyphMode) []int {
	natural := make([]int, len(cols))
	for i, c := range cols {
		natural[i] = ansi.StringWidth(fxExpandTabs(SanitiseInline(c.Title)))
		for _, row := range rows {
			if i >= len(row) {
				continue
			}
			text := SanitiseInline(row[i].Text)
			if row[i].isState {
				text = fxBadge(row[i].state, text, mode)
			}
			text = fxExpandTabs(text)
			if w := ansi.StringWidth(text); w > natural[i] {
				natural[i] = w
			}
		}
	}
	return natural
}

// fxTitle is the heading as the layer DISCLOSES it. §4.3's Dropped and
// Relaxed lists carry the sanitised heading (D-13), so an expectation built
// from the raw Col.Title would disagree with them for any title that needed
// scrubbing — the invariants below would then report a column as dropped
// without being listed, or as relaxed without being named. Like fxNatural,
// this consumes the sanitiser rather than re-deriving it: text_invariants_test.go
// is what pins SanitiseInline.
func fxTitle(c Col) string { return SanitiseInline(c.Title) }

// fxStepTwoFloor is the width step 2 tests a column against: its Min, except
// for an Atomic column, which cannot be squeezed at all and so counts at its
// natural width, and except where the column is naturally narrower than its
// own Min.
func fxStepTwoFloor(c Col, natural int) int {
	if c.Atomic {
		return natural
	}
	if c.Min < natural {
		return c.Min
	}
	return natural
}

func fxRow(cells ...Cell) []Cell { return cells }

// ---------------------------------------------------------------- fixtures
//
// Fixtures are returned as (cols, rows) rather than as a Table: the allocator
// must be provable without a renderer. tableFixtures in acceptance_test.go
// wraps these same pairs into Table values.

type allocFixture struct {
	name string
	cols []Col
	rows [][]Cell
	// degenerate marks a Col set built as a LITERAL on purpose, to exercise
	// §4.3 step 5(b)/(c) — the ladder validateCols exists to make unreachable
	// through the constructor. The test asserts that validateCols really does
	// refuse these, so a fixture cannot quietly stop being degenerate.
	degenerate bool
}

// fxProfilesCols is `ws profiles`: three columns of strictly ordered Prio, the
// table the step-2 wording measurement is taken on.
func fxProfilesCols() ([]Col, [][]Cell) {
	const base = "mcr.microsoft.com/devcontainers/base:ubuntu-24.04"
	cols := []Col{
		{Title: "NAME", Prio: 1, Min: 8, Trunc: TruncMid},
		{Title: "BASE IMAGE", Prio: 3, Min: 12, Trunc: TruncHead},
		{Title: "TOOLS", Prio: 2, Min: 16, Trunc: TruncTail},
	}
	var rows [][]Cell
	for _, x := range [][2]string{
		{"default", "node, jq, yq, fzf, ripgrep, fd, bat, lsd, delta"},
		{"devops", "opentofu, k9s, jq, yq"},
		{"go", "go, golangci-lint, node, jq, yq, fzf, ripgrep, fd"},
		{"k8s", "kubectl, helm, k9s, kind, stern, flux2, argocd, jq, yq"},
		{"meta", "go, golangci-lint, goreleaser, node, pnpm, shfmt, jq, yq, fzf"},
		{"python", "python, uv, ruff, jq, yq, fzf, ripgrep, fd"},
		{"rust", "rust, cargo-binstall, jq, yq, fzf, ripgrep, fd"},
		{"web", "node, bun, deno, pnpm"},
	} {
		rows = append(rows, fxRow(Text(x[0]), Text(base), Text(x[1])))
	}
	return cols, rows
}

// fxListCols is `ws list`: carries state cells, so the ColState exemption has
// a valid Col set to appear in as well as a degenerate one.
func fxListCols() ([]Col, [][]Cell) {
	cols := []Col{
		{Title: "NAME", Prio: 1, Min: 12, Trunc: TruncMid},
		{Title: "STATUS", Prio: 1, Min: 6, Kind: ColState},
		{Title: "PROFILE", Prio: 3, Min: 8, Trunc: TruncTail},
		{Title: "PROXY", Prio: 2, Min: 6, Atomic: true},
	}
	rows := [][]Cell{
		fxRow(Text("api"), Mark(StateOK, "running"), Text("go"), Text("via proxy")),
		fxRow(Text("web-frontend"), Mark(StateOK, "running"), Text("web"), Text("direct")),
		fxRow(Text("ml-training"), Mark(StateBusy, "starting"), Text("python-datascience-cuda"), Text("via proxy")),
		fxRow(Text("ops"), Mark(StateIdle, "stopped"), Text("devops"), Text("direct")),
		fxRow(Text("legacy-billing"), Mark(StateIdle, "not created"), Text("default"), Text("direct")),
	}
	return cols, rows
}

// fxEqualPrioCols exercises BOTH tie-breaks: two columns at the same Prio, so
// step 2's rightmost-first rule is observable, and — at the right budget —
// two equal remainders, so step 3's leftmost-first rule is too. Every other
// fixture has a strict Prio order, under which any tie-break at all renders
// them identically.
func fxEqualPrioCols() ([]Col, [][]Cell) {
	return []Col{
			{Title: "NAME", Prio: 1, Min: 8, Trunc: TruncMid},
			{Title: "LEFT", Prio: 3, Min: 10, Trunc: TruncTail},
			{Title: "RIGHT", Prio: 3, Min: 10, Trunc: TruncTail},
		}, [][]Cell{
			fxRow(Text("workspace-01"), Text("left-value-00000000"), Text("right-value-0000000")),
			fxRow(Text("api"), Text("left"), Text("right")),
		}
}

// fxTieCols is the minimal leftmost-first fixture: two columns, identical
// slack, one cell of deficit. The single cell is placed by the tie-break ALONE
// — nothing else in the arithmetic can decide it.
func fxTieCols() ([]Col, [][]Cell) {
	return []Col{
			{Title: "A", Prio: 1, Min: 5, Trunc: TruncTail},
			{Title: "B", Prio: 1, Min: 5, Trunc: TruncTail},
		}, [][]Cell{
			fxRow(Text(strings.Repeat("a", 15)), Text(strings.Repeat("b", 15))),
		}
}

// fxAtomicSqueezeCols reaches step 5(a) — and only 5(a) — through a Col set
// that PASSES validateCols. ID is Prio 1, Atomic and 12 cells wide against a
// Min of 8, so step 3 cannot touch it: at MinWidth the row still needs 33
// cells after every non-atomic column is at its Min, and 5(a) squeezes the
// atomic column the four cells that makes it fit.
//
// This is also the fixture that demonstrates accepted finding #5: Σ Min +
// chrome is 14+8+7 = 29 and FITS, so the narrower step-5 trigger would never
// have fired here while step 4 was handed a four-cell deficit.
func fxAtomicSqueezeCols() ([]Col, [][]Cell) {
	return []Col{
			{Title: "NAME", Prio: 1, Min: 14, Trunc: TruncMid},
			{Title: "ID", Prio: 1, Min: 8, Atomic: true},
		}, [][]Cell{
			fxRow(Text("ml-training-datascience-cuda-experimental-01"), Text("b3f1a20c9d4e")),
			fxRow(Text("api"), Text("7c2e04ab15ff")),
		}
}

// fxStateFloorCols is DEGENERATE input that reaches step 5(b) with a state
// column present, which is the only shape that exercises §4.3's ColState
// exemption. Measured at budget 29: widths [10 12], Relaxed [NAME] — NAME is
// below its Min of 20 and STATUS stays at its Min of 12. With the exemption
// removed the same input allocates [11 11] and Relaxed becomes [NAME STATUS],
// leaving 11 cells for a state column whose widest cell, `- not created`,
// needs 13 — a bare mark is what §4.5 exists to prevent.
func fxStateFloorCols() ([]Col, [][]Cell) {
	return []Col{
			{Title: "NAME", Prio: 1, Min: 20, Trunc: TruncMid},
			{Title: "STATUS", Prio: 1, Min: 12, Kind: ColState},
		}, [][]Cell{
			fxRow(Text("ml-training-datascience-cuda-experimental-01"), Mark(StateIdle, "not created")),
			fxRow(Text("api"), Mark(StateOK, "running")),
		}
}

// fxMarkerFloorCols is DEGENERATE input that drives step 5(b) all the way to
// its floor and then into 5(c). Six un-droppable columns cost 3(6−1)+4 = 19
// cells of chrome on their own, so at MinWidth there are 10 cells left for six
// columns.
//
// Measured: in UTF-8 mode, where the marker is one cell, 5(b) takes them to
// 1,1,2,2,2,2 and every column survives. In ASCII mode, where the marker is
// "..." and the floor is therefore 3, 5(b) stops with all six at 3 — 37 cells,
// still over 29 — and 5(c) drops the two rightmost. That difference IS the
// floor doing its job, and it is what mutants.RelaxFloorOne erases.
func fxMarkerFloorCols() ([]Col, [][]Cell) {
	var cols []Col
	var row []Cell
	for i := 0; i < 6; i++ {
		n := string(rune('0' + i))
		cols = append(cols, Col{Title: "COL-" + n, Prio: 1, Min: 12, Trunc: TruncTail})
		row = append(row, Text("value-"+n+"-xx"))
	}
	return cols, [][]Cell{row}
}

// fxCaptionOnlyCols is §4.3's termination at n = 0: a single un-droppable
// state column too wide for MinWidth, exempt from 5(b), so 5(c) drops it and
// n reaches 0, where the chrome formula 3(n−1)+4 is undefined. Measured: the
// allocation is chrome 0 / total 0 at budget 29 and chrome 4 / total 30 at 30.
func fxCaptionOnlyCols() ([]Col, [][]Cell) {
	return []Col{{Title: "STATUS", Prio: 1, Min: 26, Kind: ColState}},
		[][]Cell{fxRow(Mark(StateIdle, "not created in this workspace"))}
}

func allocFixtures() []allocFixture {
	pc, pr := fxProfilesCols()
	lc, lr := fxListCols()
	ec, er := fxEqualPrioCols()
	tc, tr := fxTieCols()
	ac, ar := fxAtomicSqueezeCols()
	sc, sr := fxStateFloorCols()
	mc, mr := fxMarkerFloorCols()
	cc, cr := fxCaptionOnlyCols()
	return []allocFixture{
		{name: "profiles", cols: pc, rows: pr},
		{name: "list", cols: lc, rows: lr},
		{name: "equal-prio", cols: ec, rows: er},
		{name: "tie", cols: tc, rows: tr},
		{name: "atomic-squeeze", cols: ac, rows: ar},
		{name: "degenerate/state-floor", cols: sc, rows: sr, degenerate: true},
		{name: "degenerate/marker-floor", cols: mc, rows: mr, degenerate: true},
		{name: "degenerate/caption-only", cols: cc, rows: cr, degenerate: true},
	}
}

// -------------------------------------------------------------- invariants

// allocPolicyStats records which branches the invariant pass actually
// exercised, so the pass can refuse to report success over a corpus in which
// nothing was ever dropped or relaxed.
//
// THE SHAPE IS PART OF THE CONTRACT WITH TestAcceptanceSweep in
// acceptance_test.go, which zeroes allocStats before the sweep and reads
// allocations/dropped/relaxed/squeezed/captionOnly back out of it afterwards,
// and errors if any of them is zero. Whichever test drives the assertion zeroes the counter first; the
// counter is package-global because a sweepAssertion's check signature carries
// only a renderCase and a results sink, and threading a counter through it
// would change a shape every later harness file depends on.
type allocPolicyStats struct {
	allocations, naturalFit, shrunk, dropped, relaxed, squeezed, captionOnly int
}

// allocStats is the live counter.
var allocStats allocPolicyStats

// assertAllocPolicy is §4.3 read as arithmetic, in the sweepAssertion shape
// sweepAssertions() in acceptance_test.go consumes.
//
// It is registered in sweepAssertions(). What it contributes today is
// measured AND asserted: TestAllocatorMutantsRedenTheDirectChecks plants each
// §4.3 switch in turn and holds the set of assertion bodies that went red
// against a per-plant lower bound, so this split cannot drift out from under
// the comment. Over its seven plants:
//
//	alloc_golden   red on 7 of 7
//	alloc_policy   red on 6 of 7 — it misses state_column_not_exempt_from_5b
//	relax_clauses  red on 6 of 7 — it misses drop_against_natural_sum
//
// Those two misses are the two wantRed sets in that test that are not all
// three bodies. If a change makes a body stop catching a plant, the control
// fails and names it rather than staying green on the strength of another
// body's catch — and if a change makes a body start catching one, the control
// stays green and these three lines are what needs re-measuring.
//
// So no §4.3 switch is caught by alloc_policy alone among those three bodies,
// and the value it adds over the goldens is reach: it holds every
// fixture at every width in both glyph modes rather than seven hand-picked
// points — and that reach is now the corpus's: sweepAssertions() runs this
// same body over fxCorpus()'s 49 fixtures as well as over the eight below.
// Whether it becomes the sole killer of any mutant in the finished suite is a
// Task 12 measurement, over the full switch roster rather than the §4.3 seven.
//
// Goes red when: a column is dropped while shrinking the kept columns to their
// Min would still have fitted (the step-2 wording §4.3 rejected); a column is
// taken below its Min while an Atomic column still has slack above its own
// (the step-5 order); a relaxed column is shaved below the active truncation
// marker's width (finding #4's floor); the chrome formula drifts; the
// allocator stretches a column past its natural width; the natural widths
// disagree with the test's own composition; or the allocation leaves the row
// narrower than the budget while still truncating content.
var assertAllocPolicy = sweepAssertion{
	name: "alloc_policy",
	spec: "§4.3",
	what: "the allocation obeys §4.3 read as arithmetic — drop necessity, relaxation order, the 5(b) floor, chrome, no stretch",
	check: func(rc renderCase, r *results) {
		if !rc.fx.isTable {
			return
		}
		a := Allocate(rc.fx.cols, rc.fx.rows, rc.budget, rc.mode)
		checkAllocInvariants(rc.fx.name, rc.fx.cols, rc.fx.rows, rc.budget, rc.mode, a, r)
	},
}

// checkAllocInvariants holds one allocation against §4.3 read as arithmetic.
// Nothing here calls Allocate to decide what it expects, and nothing here
// reads a.Natural as an expectation: the natural widths come from fxNatural,
// the test's own composition.
func checkAllocInvariants(name string, cols []Col, rows [][]Cell, budget int, mode GlyphMode, a Alloc, r *results) {
	fail := func(format string, args ...any) {
		r.fail("alloc_policy", "%s @ %d (mode %d): "+format,
			append([]any{name, budget, mode}, args...)...)
	}
	allocStats.allocations++
	if len(a.Dropped) > 0 {
		allocStats.dropped++
	}
	if len(a.Relaxed) > 0 {
		allocStats.relaxed++
	}
	if len(a.Kept) == 0 {
		allocStats.captionOnly++
	}

	// -- the natural widths the rest of this function is written against are
	//    the test's own, and the allocator's copy must agree with them.
	nat := fxNatural(cols, rows, mode)
	if !equalInts(a.Natural, nat) {
		fail("natural widths %v; the widest of each column's heading and its cells is %v", a.Natural, nat)
	}

	// -- chrome and total, from the test's own formula.
	if want := fxChrome(len(a.Kept)); a.Chrome != want {
		fail("chrome is %d for %d kept columns; 3(n−1)+4 is %d", a.Chrome, len(a.Kept), want)
	}
	sum := 0
	for _, i := range a.Kept {
		sum += a.Widths[i]
	}
	if a.Chrome+sum != a.Total {
		fail("Total %d is not chrome %d + Σ widths %d", a.Total, a.Chrome, sum)
	}
	if a.Total > budget {
		fail("Total %d exceeds the budget %d", a.Total, budget)
	}

	// -- the dropped set and the width vector must agree.
	var droppedTitles []string
	for i := range cols {
		if a.Widths[i] < 0 {
			droppedTitles = append(droppedTitles, fxTitle(cols[i]))
		}
	}
	if strings.Join(droppedTitles, ",") != strings.Join(a.Dropped, ",") {
		fail("Dropped is %v but the width vector marks %v as dropped", a.Dropped, droppedTitles)
	}

	// -- the two disclosure lists are disjoint: step 5(b) may narrow a column
	//    that step 5(c) then drops, and a caption naming it under both
	//    "Narrowed:" and "Hidden:" states one fact twice and contradicts it
	//    once.
	for _, title := range a.Relaxed {
		for _, gone := range a.Dropped {
			if title == gone {
				fail("%q is named in both Relaxed %v and Dropped %v", title, a.Relaxed, a.Dropped)
			}
		}
	}

	// -- step 1: the table does not stretch to fill, and a column that fits
	//    naturally is never widened.
	for _, i := range a.Kept {
		if a.Widths[i] > nat[i] {
			fail("%q allocated %d, wider than its natural %d", cols[i].Title, a.Widths[i], nat[i])
		}
		if a.Widths[i] < 1 {
			fail("%q allocated %d cells", cols[i].Title, a.Widths[i])
		}
	}
	naturalTotal := fxChrome(len(cols))
	for i := range cols {
		naturalTotal += nat[i]
	}
	if naturalTotal <= budget {
		allocStats.naturalFit++
		for i := range cols {
			if a.Widths[i] != nat[i] {
				fail("everything fits naturally (%d <= %d) but %q was allocated %d of %d",
					naturalTotal, budget, cols[i].Title, a.Widths[i], nat[i])
			}
		}
		if len(a.Dropped)+len(a.Relaxed) > 0 {
			fail("everything fits naturally (%d <= %d) but dropped %v / relaxed %v",
				naturalTotal, budget, a.Dropped, a.Relaxed)
		}
	}

	// -- step 3 / step 5(b): Min is the floor unless the column is named in
	//    Relaxed, a relaxed column IS below its Min, and no relaxed column is
	//    shaved below the active glyph mode's truncation marker (finding #4).
	relaxed := map[string]bool{}
	for _, title := range a.Relaxed {
		relaxed[title] = true
	}
	for _, i := range a.Kept {
		c := cols[i]
		floor := c.Min
		if nat[i] < floor {
			floor = nat[i]
		}
		switch {
		case relaxed[fxTitle(c)]:
			if a.Widths[i] >= c.Min {
				fail("%q is named in Relaxed but was allocated %d, which is not below its Min %d",
					c.Title, a.Widths[i], c.Min)
			}
			if cells := fxMarkerCells(mode); a.Widths[i] < cells {
				fail("%q was relaxed to %d cells, below the %d-cell truncation marker of this glyph mode",
					c.Title, a.Widths[i], cells)
			}
		default:
			if a.Widths[i] < floor {
				fail("%q allocated %d, below its Min %d, without being named in Relaxed",
					c.Title, a.Widths[i], c.Min)
			}
		}
	}

	// -- step 5(a) before step 5(b): nothing goes below its Min while an
	//    Atomic column still has slack above its own.
	if len(a.Relaxed) > 0 {
		for _, i := range a.Kept {
			if cols[i].Atomic && a.Widths[i] > cols[i].Min {
				fail("%q was taken below its Min while the Atomic column %q still sat at %d against a Min of %d",
					a.Relaxed[0], cols[i].Title, a.Widths[i], cols[i].Min)
			}
		}
	}
	for _, i := range a.Kept {
		if a.Widths[i] < nat[i] {
			allocStats.shrunk++
			break
		}
	}
	for _, i := range a.Kept {
		if cols[i].Atomic && a.Widths[i] < nat[i] {
			allocStats.squeezed++
			break
		}
	}

	// -- step 2: dropping is the LAST resort before shrinking, and the test is
	//    against the remaining columns' MIN widths. §4.3's drop loop is
	//    order-driven, so necessity has to be checked at the moment of each
	//    drop rather than against the final kept set: at 29 the profiles table
	//    legitimately ends up narrower than it had to be, because BASE IMAGE is
	//    Prio 3 and goes before the Prio 2 TOOLS whose departure is what freed
	//    the room.
	//
	//    The order is reconstructed here from the spec, not read from the
	//    allocator: highest Prio first, rightmost among equals. A column of
	//    Prio <= 1 can only have been dropped by step 5(c), because step 2
	//    never touches one and step 5 is unreachable unless step 2 stopped with
	//    nothing droppable left.
	live := map[int]bool{}
	for i := range cols {
		live[i] = true
	}
	liveNeed := func() int {
		need := fxChrome(len(live))
		for i := range live {
			need += fxStepTwoFloor(cols[i], nat[i])
		}
		return need
	}
	var stepTwo, stepFiveC []int
	for i := range cols {
		switch {
		case a.Widths[i] >= 0:
		case cols[i].Prio > 1:
			stepTwo = append(stepTwo, i)
		default:
			stepFiveC = append(stepFiveC, i)
		}
	}
	sort.SliceStable(stepTwo, func(x, y int) bool {
		if cols[stepTwo[x]].Prio != cols[stepTwo[y]].Prio {
			return cols[stepTwo[x]].Prio > cols[stepTwo[y]].Prio
		}
		return stepTwo[x] > stepTwo[y] // rightmost of equal Prio first
	})
	for _, d := range stepTwo {
		if need := liveNeed(); need <= budget {
			fail("%q was dropped although the columns still standing at that point fit at their Min widths in %d of %d",
				cols[d].Title, need, budget)
		}
		delete(live, d)
	}
	// …and the loop must not stop early either: if what is left still does not
	// fit at its Min widths, something droppable was kept.
	if need := liveNeed(); need > budget {
		for i := range live {
			if cols[i].Prio > 1 {
				fail("dropping stopped with %q (Prio %d) still kept, while the remaining columns need %d of %d at their Min widths",
					cols[i].Title, cols[i].Prio, need, budget)
				break
			}
		}
	}
	// -- step 5(c) drops right to left and stops the moment the row fits, so
	//    only the LAST of them can be checked for necessity — and that is
	//    sufficient, because every earlier state carried strictly more columns
	//    and was therefore also over budget. The last one is the leftmost index.
	if len(stepFiveC) > 0 {
		d := stepFiveC[0]
		irreducible := cols[d].Min
		if nat[d] < irreducible {
			irreducible = nat[d]
		}
		// A state column is exempt from 5(b), so its Min is as narrow as it
		// goes; anything else could have been relaxed to the marker's width.
		if cols[d].Kind != ColState && fxMarkerCells(mode) < irreducible {
			irreducible = fxMarkerCells(mode)
		}
		need := fxChrome(len(a.Kept)+1) + irreducible
		for _, k := range a.Kept {
			need += a.Widths[k]
		}
		if need <= budget {
			fail("step 5(c) dropped %q although it would have fitted beside the kept columns in %d of %d",
				cols[d].Title, need, budget)
		}
	}

	// -- tightness. NOT "Total == budget whenever anything happened": measured,
	//    profiles at budget 29 allocates Total 11 after two drops and the ASCII
	//    marker-floor fixture allocates 25 at budget 29 after 5(c), because a
	//    whole column is a coarse unit. The invariant that IS true is the
	//    conditional one: when content was truncated and nothing was dropped,
	//    the allocator must have used the whole budget — shrinking by more than
	//    the deficit is content thrown away for nothing, shrinking by less
	//    overflows.
	truncating := false
	for _, i := range a.Kept {
		if a.Widths[i] < nat[i] {
			truncating = true
		}
	}
	if truncating && len(a.Dropped) == 0 && a.Total != budget {
		fail("content is truncated and nothing was dropped, but Total is %d against a budget of %d",
			a.Total, budget)
	}
}

// allocFixtureCases wraps this file's (cols, rows) pairs into the shared
// `fixture` shape, with no renderer: assertAllocPolicy reads only the
// allocator's input. tableFixtures in acceptance_test.go declares the
// rendering table fixtures over the SAME pairs.
func allocFixtureCases() []fixture {
	var out []fixture
	for _, fx := range allocFixtures() {
		out = append(out, fixture{
			name:    "alloc/" + fx.name,
			kind:    "alloc",
			spec:    "§4.3 allocator policy",
			isTable: true,
			cols:    fx.cols,
			rows:    fx.rows,
		})
	}
	return out
}

// checkAllocPolicy drives assertAllocPolicy — the SAME assertion body
// sweepAssertions() registers — over every fixture at every width in [MinWidth, 200]
// in both glyph modes. It zeroes allocStats first, exactly as
// TestAcceptanceSweep does.
func checkAllocPolicy(r *results) allocPolicyStats {
	allocStats = allocPolicyStats{}
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for w := MinWidth; w <= 200; w++ {
			for _, fx := range allocFixtureCases() {
				assertAllocPolicy.check(newRenderCase(fx, w, mode), r)
			}
		}
	}
	return allocStats
}

// TestAllocPolicy is §4.3 as arithmetic, over 8 fixtures × 172 widths × 2
// glyph modes. It is the standalone entry point for the assertion
// sweepAssertions() also registers; both drive the same body.
//
// The reach counts are asserted non-zero because every invariant above is
// trivially green on a corpus that never drops, never relaxes and never
// squeezes — which is precisely the state the reference implementation was in
// when relax() had 0.0% coverage and four planted defects inside it rendered
// byte-identically to the clean output.
func TestAllocPolicy(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	r := newResults()
	reach := checkAllocPolicy(r)
	r.report(t)

	for _, c := range []struct {
		name string
		n    int
	}{
		{"allocations", reach.allocations},
		{"natural fit (step 1)", reach.naturalFit},
		{"shrunk (step 3)", reach.shrunk},
		{"dropped (step 2 or 5c)", reach.dropped},
		{"relaxed below Min (step 5b)", reach.relaxed},
		{"Atomic squeezed (step 5a)", reach.squeezed},
		{"caption only (n = 0)", reach.captionOnly},
	} {
		if c.n == 0 {
			t.Errorf("no allocation in the corpus reached %q: the invariants that guard it cannot fail", c.name)
		}
		t.Logf("%-28s %d", c.name, c.n)
	}
}

// TestHarnessFixtureFieldsAreRead gives every field of `fixture` a reader in
// the task that DECLARES it rather than only in the task that first fills it
// in, which is what keeps `golangci-lint`'s `unused` quiet: it counts _test.go
// files, and the acceptance gate runs it before every commit.
//
// It is named for what it does. It runs over allocFixtureCases() and nothing
// else, and every fixture there is a table with no renderer, no states, no
// prefix state and no fidelity list — so most of its clauses cannot run, and
// the budget clause compares newRenderCase's output against the same fxBudget
// the constructor called. The corpus that fills those fields is fxCorpus() in
// corpus_test.go, and the assertions that read them back are
// assertStateStructure and assertContentFidelity. Reading THIS test as a shape
// assertion over the corpus would overstate it.
func TestHarnessFixtureFieldsAreRead(t *testing.T) {
	for _, fx := range allocFixtureCases() {
		if fx.name == "" || fx.spec == "" || fx.kind == "" {
			t.Errorf("fixture %+v does not declare name, kind and the clause it discharges", fx)
		}
		if fx.isTable == (fx.cols == nil) {
			t.Errorf("%s: isTable is %t but cols is %v; a table fixture carries the allocator's input and a non-table one carries none",
				fx.name, fx.isTable, fx.cols)
		}
		if !fx.isTable && (len(fx.rows) > 0 || fx.wideFlag != "" || fx.caption != "") {
			t.Errorf("%s: a non-table fixture declares rows, a caption or a WideFlag", fx.name)
		}
		for _, st := range fx.states {
			if _, ok := fxStateVocabulary[st.st]; !ok {
				t.Errorf("%s: declares state %d, which is not in §4.5's table", fx.name, st.st)
			}
			// Reads fxState.label, which assertStateStructure in
			// acceptance_test.go now does too — and an unread struct field is
			// `field label is unused` at the acceptance gate. It is a real
			// guard as well: a label with surrounding whitespace could never
			// match the badge the render emits.
			if strings.TrimSpace(st.label) != st.label {
				t.Errorf("%s: state label %q carries surrounding whitespace; no render can match it", fx.name, st.label)
			}
		}
		if fx.hasPrefixState {
			if _, ok := fxStateVocabulary[fx.prefixState]; !ok {
				t.Errorf("%s: declares prefix state %d, which is not in §4.5's table", fx.name, fx.prefixState)
			}
		}
		for _, src := range fx.fidelity {
			if src == "" {
				t.Errorf("%s: declares an empty fidelity source, which every render satisfies", fx.name)
			}
		}
		// A fixture with no renderer is legal HERE and nowhere else: from Task
		// 8 on, every fixture in the corpus is rendered.
		rc := newRenderCase(fx, MinWidth, GlyphUTF8)
		if fx.render == nil && rc.out != "" {
			t.Errorf("%s: a fixture with no renderer produced output", fx.name)
		}
		if rc.width != MinWidth || rc.budget != fxBudget(MinWidth) || rc.mode != GlyphUTF8 || rc.stream == nil {
			t.Errorf("%s: newRenderCase did not carry the width, mode and stream it was given", fx.name)
		}
		if len(rc.lines) != 0 || len(rc.plain) != 0 {
			t.Errorf("%s: a fixture with no renderer produced %d lines", fx.name, len(rc.lines))
		}
	}
}

// ---------------------------------------------------------------- goldens

// allocGolden is one hand-computed allocation. The arithmetic is written out
// in workings so a reviewer can check the number rather than the code.
type allocGolden struct {
	name     string
	cols     []Col
	rows     [][]Cell
	budget   int
	mode     GlyphMode
	natural  []int
	widths   []int // -1 for a dropped column
	dropped  []string
	relaxed  []string
	total    int
	workings string
	// rejected is a width vector the golden must NOT produce: the same
	// allocation under the opposite tie-break. Asserting only the expected
	// vector leaves a reader unable to tell a tie-break assertion from an
	// arithmetic one, and leaves the test silent about which rule it pins.
	rejected []int
}

func allocGoldens() []allocGolden {
	ec, er := fxEqualPrioCols()
	tc, tr := fxTieCols()
	ac, ar := fxAtomicSqueezeCols()
	mc, mr := fxMarkerFloorCols()
	cc, cr := fxCaptionOnlyCols()

	return []allocGolden{
		{
			name: "natural-fit/equal-prio@80", cols: ec, rows: er, budget: 80,
			natural: []int{12, 19, 19}, widths: []int{12, 19, 19}, total: 60,
			workings: "12+19+19 = 50 plus chrome 3(3−1)+4 = 10 is 60, which fits 80: §4.3 step 1, " +
				"and the table does not stretch to fill",
		},
		{
			name: "shrink/equal-prio@50", cols: ec, rows: er, budget: 50,
			natural: []int{12, 19, 19}, widths: []int{10, 15, 15}, total: 50,
			workings: "Σ Min 8+10+10 plus chrome 10 is 38, inside 50, so step 2 drops nothing. " +
				"Deficit 60−50 = 10 over slacks 4/9/9 (total 22): bases 40/22=1, 90/22=4, 90/22=4 " +
				"leave one cell to place, and the largest remainder is NAME's 18, so NAME takes it: 10/15/15",
		},
		{
			name: "drop/equal-prio@34", cols: ec, rows: er, budget: 34,
			natural: []int{12, 19, 19}, widths: []int{11, 16, -1}, dropped: []string{"RIGHT"}, total: 34,
			workings: "Σ Min 8+10+10 plus chrome 10 is 38 against 34, so step 2 drops — Prio 3 ties, " +
				"and the RIGHTMOST of equal Prio goes first. The remainder then fits at Min " +
				"(8+10+7 = 25), so step 3 takes the deficit 38−34 = 4 over slacks 4/9: bases 16/13=1 " +
				"and 36/13=2 leave one cell, LEFT's remainder 10 beats NAME's 3: 11/16",
			rejected: []int{11, -1, 16}, // the rule inverted: LEFTMOST of equal Prio dropped first
		},
		{
			name: "tie-break/leftmost@36", cols: tc, rows: tr, budget: 36,
			natural: []int{15, 15}, widths: []int{14, 15}, total: 36,
			workings: "15+15 plus chrome 7 is 37, one cell over. Both columns have slack 10, so both " +
				"bases are 1*10/20 = 0 with the SAME remainder 10, and the single cell of deficit is " +
				"placed by the tie-break alone: §4.3 step 3 breaks ties LEFTMOST-first, so A pays it. " +
				"Measured: a rightmost-first tie-break returns [15 14] on this exact input",
			rejected: []int{15, 14},
		},
		{
			name: "step5a/atomic-squeeze@29", cols: ac, rows: ar, budget: 29,
			natural: []int{44, 12}, widths: []int{14, 8}, total: 29,
			workings: "Nothing is droppable (both Prio 1). Step 3 has 30 cells of slack on NAME and " +
				"needs 44+12+7−29 = 34, so it stops with NAME at its Min: 14+12+7 = 33, still over. " +
				"Step 5(a) squeezes the Atomic ID from 12 to its Min 8 — exactly the four cells " +
				"needed — and stops there, so nothing is relaxed below a Min and nothing is dropped. " +
				"Measured: with 5(a) removed the same input allocates [11 11] and relaxes BOTH columns",
			rejected: []int{11, 11},
		},
		{
			name: "step5b+c/marker-floor@29/ascii", cols: mc, rows: mr, budget: 29, mode: GlyphASCII,
			natural: []int{10, 10, 10, 10, 10, 10}, widths: []int{3, 3, 3, 3, -1, -1},
			dropped: []string{"COL-4", "COL-5"},
			relaxed: []string{"COL-0", "COL-1", "COL-2", "COL-3"},
			total:   25,
			workings: "Six un-droppable columns cost 3(6−1)+4 = 19 cells of chrome, leaving 10 for six " +
				"columns. Step 5(b) shaves them below their Min of 12 but stops at the ASCII " +
				`marker's width — "..." is three cells — at 6*3+19 = 37, still over 29. Step 5(c) ` +
				"then drops right to left: five columns need 15+16 = 31, four need 12+13 = 25, which fits. " +
				"Relaxed names the four SURVIVORS: 5(b) narrowed all six, and the two 5(c) then hid are " +
				"struck from the narrowed list, because a caption cannot tell the operator that one " +
				"column was both narrowed and hidden. " +
				"Measured: with the floor back at 1 the same input allocates [1 1 2 2 2 2] and drops nothing",
			rejected: []int{1, 1, 2, 2, 2, 2},
		},
		{
			name: "step5c/termination-at-zero@29", cols: cc, rows: cr, budget: 29,
			natural: []int{31}, widths: []int{-1}, dropped: []string{"STATUS"}, total: 0,
			workings: "One un-droppable state column 31 cells wide with a Min of 26. Step 3 has no " +
				"slack below the Min, 26+4 = 30 is still over 29, and §4.3 exempts a state column " +
				"from 5(b) — so 5(c) drops it and n reaches 0, where the chrome formula is undefined " +
				"and Chrome and Total are both 0. Measured: without the exemption it is shaved to 25 instead",
			rejected: []int{25},
		},
	}
}

// checkAllocGoldens reports every golden that disagrees with the allocator.
func checkAllocGoldens(r *results) {
	for _, g := range allocGoldens() {
		a := Allocate(g.cols, g.rows, g.budget, g.mode)
		if !equalInts(a.Natural, g.natural) {
			r.fail("alloc_golden", "%s: natural widths %v, hand-computed %v", g.name, a.Natural, g.natural)
		}
		if !equalInts(a.Widths, g.widths) {
			r.fail("alloc_golden", "%s: widths %v, hand-computed %v (%s)", g.name, a.Widths, g.widths, g.workings)
		}
		if g.rejected != nil && equalInts(a.Widths, g.rejected) {
			r.fail("alloc_golden", "%s: widths %v are the REJECTED rule's answer, not §4.3's (%s)",
				g.name, a.Widths, g.workings)
		}
		if strings.Join(a.Dropped, ",") != strings.Join(g.dropped, ",") {
			r.fail("alloc_golden", "%s: dropped %v, hand-computed %v", g.name, a.Dropped, g.dropped)
		}
		if strings.Join(a.Relaxed, ",") != strings.Join(g.relaxed, ",") {
			r.fail("alloc_golden", "%s: relaxed %v, hand-computed %v", g.name, a.Relaxed, g.relaxed)
		}
		if a.Total != g.total {
			r.fail("alloc_golden", "%s: total %d, hand-computed %d", g.name, a.Total, g.total)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAllocationGoldens adjudicates the two tie-breaks and the step-5 order
// against hand-computed allocations. An invariant can say a drop was
// permissible; only a golden can say WHICH column went.
//
// The tie-break needs a golden for a reason worth recording, and the
// measurement was re-run against this package rather than carried over. A
// plain sort.Slice in place of sort.SliceStable, with nothing else changed,
// produces the same width vector on 4,095 of 4,095 equal-slack column sets
// (n = 2..40, deficit 1..5n) and on 200,000 of 200,000 random column sets
// (n = 2..11, random Min/natural/deficit): Go's pdqsort falls back to
// insertion sort below 12 elements and detects an already-ordered run above
// it. Planted into shrinkToFit directly, the swap leaves the WHOLE package
// suite green.
//
// So the sort call does not carry the guarantee at the sizes a CLI table
// reaches. What carries it is `parts` being BUILT in a.Kept order — and
// planting that instead, with the slice built right to left, reddens
// alloc_golden and nothing else. That is the claim this test exists to make
// good.
func TestAllocationGoldens(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	r := newResults()
	checkAllocGoldens(r)
	r.report(t)
	for _, g := range allocGoldens() {
		t.Logf("%-34s %s", g.name, g.workings)
	}
}

// ------------------------------------------------------- §4.3 step 5 direct

// checkRelaxClauses asserts what each clause of step 5 must do, on the four
// fixtures built to reach them — and asserts which of those fixtures the
// CONSTRUCTOR accepts, because that distinction is itself part of the claim.
//
// validateCols guarantees that Σ Min of the un-droppable columns plus their
// chrome fits MinWidth, which makes 5(b) and 5(c) MATHEMATICALLY UNREACHABLE
// for any Col set built through it. 5(a) is the only rung a valid Col set can
// reach. The other three fixtures are literals on purpose — §4.3's
// programming-error path, "covered by the degenerate suite" — and the checks
// below assert that validateCols really does refuse them, so a fixture cannot
// quietly stop being degenerate and take the whole ladder back out of test.
func checkRelaxClauses(r *results) {
	// The degenerate/valid split, asserted rather than assumed.
	for _, fx := range allocFixtures() {
		err := validateCols(fx.cols)
		switch {
		case fx.degenerate && err == nil:
			r.fail("relax_clauses",
				"%s is declared degenerate — it exists to exercise §4.3 step 5(b)/(c) — but validateCols accepts it, "+
					"so it no longer tests the ladder the constructor exists to prevent", fx.name)
		case !fx.degenerate && err != nil:
			r.fail("relax_clauses", "%s is a Col set the constructor must accept: %v", fx.name, err)
		}
	}

	// (a) An Atomic column absorbs the deficit before anything goes below its
	//     Min, and on this fixture that is enough on its own.
	ac, ar := fxAtomicSqueezeCols()
	a := Allocate(ac, ar, MinWidth, GlyphUTF8)
	if a.Widths[1] != ac[1].Min {
		r.fail("relax_clauses", "step 5(a): the Atomic column was allocated %d, not squeezed to its Min %d",
			a.Widths[1], ac[1].Min)
	}
	if len(a.Relaxed) != 0 || len(a.Dropped) != 0 {
		r.fail("relax_clauses", "step 5(a) alone should have sufficed; relaxed %v dropped %v", a.Relaxed, a.Dropped)
	}

	// (b) The widest kept column goes below its Min; a state column does not,
	//     and is dropped by (c) instead.
	sc, sr := fxStateFloorCols()
	b := Allocate(sc, sr, MinWidth, GlyphUTF8)
	if b.Widths[0] >= sc[0].Min {
		r.fail("relax_clauses", "step 5(b): NAME was allocated %d, never taken below its Min %d",
			b.Widths[0], sc[0].Min)
	}
	if b.Widths[1] < sc[1].Min {
		r.fail("relax_clauses", "step 5(b): the state column was allocated %d, below its Min %d — §4.3 exempts it",
			b.Widths[1], sc[1].Min)
	}
	for _, title := range b.Relaxed {
		if title == fxTitle(sc[1]) {
			r.fail("relax_clauses", "step 5(b): the state column %q was named in Relaxed", title)
		}
	}

	// (c) Kept columns drop right to left, Prio 1 included.
	mc, mr := fxMarkerFloorCols()
	c := Allocate(mc, mr, MinWidth, GlyphASCII)
	switch {
	case len(c.Dropped) == 0:
		r.fail("relax_clauses", "step 5(c): nothing was dropped by the fixture built to reach it")
	case c.Dropped[len(c.Dropped)-1] != fxTitle(mc[len(mc)-1]):
		r.fail("relax_clauses", "step 5(c) drops right to left; dropped %v of %d columns", c.Dropped, len(mc))
	}

	// The floor of (b) is the truncation marker's width in the ACTIVE glyph
	// mode — accepted finding #4 — asserted in both directions: no column below
	// it, and, in ASCII mode where the two numbers differ, at least one column
	// standing ON it, so the check cannot pass by never reaching it.
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		floor := fxMarkerCells(mode)
		al := Allocate(mc, mr, MinWidth, mode)
		narrowest, onFloor := 1<<30, 0
		for _, i := range al.Kept {
			if al.Widths[i] < narrowest {
				narrowest = al.Widths[i]
			}
			if al.Widths[i] == floor {
				onFloor++
			}
		}
		if narrowest < floor {
			r.fail("relax_clauses", "mode %d: a column was allocated %d cells, below the %d-cell marker of this mode",
				mode, narrowest, floor)
		}
		if mode == GlyphASCII && onFloor == 0 {
			r.fail("relax_clauses", "mode %d: no column reached the floor of %d, so the floor was never tested", mode, floor)
		}
	}

	// Termination at n = 0: chrome and total are both undefined and must be 0,
	// and one cell more brings the column back. The render-level half of this
	// — that no box is drawn — is asserted by assertGridPairing's n = 0 branch
	// in acceptance_test.go.
	cc, cr := fxCaptionOnlyCols()
	z := Allocate(cc, cr, MinWidth, GlyphUTF8)
	if len(z.Kept) != 0 || z.Chrome != 0 || z.Total != 0 {
		r.fail("relax_clauses", "at n = 0: kept %v chrome %d total %d; all three must be empty/0",
			z.Kept, z.Chrome, z.Total)
	}
	if wider := Allocate(cc, cr, MinWidth+1, GlyphUTF8); len(wider.Kept) != 1 || wider.Total != MinWidth+1 {
		r.fail("relax_clauses", "at %d the column fits at its Min and must come back: kept %v total %d",
			MinWidth+1, wider.Kept, wider.Total)
	}
}

// TestRelaxClauses is step 5 on the shipped behaviour, and the record of which
// fixture reaches which clause — without which the clauses read as speculation
// about a branch nothing executes, which is what they were at 0% coverage.
func TestRelaxClauses(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	r := newResults()
	checkRelaxClauses(r)
	r.report(t)

	ac, ar := fxAtomicSqueezeCols()
	sc, sr := fxStateFloorCols()
	mc, mr := fxMarkerFloorCols()
	cc, cr := fxCaptionOnlyCols()
	t.Logf("5(a) @%d      : widths %v — the Atomic column absorbed the deficit",
		MinWidth, Allocate(ac, ar, MinWidth, GlyphUTF8).Widths)
	sf := Allocate(sc, sr, MinWidth, GlyphUTF8)
	t.Logf("5(b) @%d      : widths %v relaxed %v — the state column kept its Min", MinWidth, sf.Widths, sf.Relaxed)
	t.Logf("5(b) floor    : UTF-8 %v (floor %d), ASCII %v (floor %d)",
		Allocate(mc, mr, MinWidth, GlyphUTF8).Widths, fxMarkerCells(GlyphUTF8),
		Allocate(mc, mr, MinWidth, GlyphASCII).Widths, fxMarkerCells(GlyphASCII))
	t.Logf("5(c) @%d ASCII: dropped %v", MinWidth, Allocate(mc, mr, MinWidth, GlyphASCII).Dropped)
	z := Allocate(cc, cr, MinWidth, GlyphUTF8)
	t.Logf("n = 0 @%d     : kept %v chrome %d total %d", MinWidth, z.Kept, z.Chrome, z.Total)
}

// TestAllocatorMutantsRedenTheDirectChecks is the control for all three bodies
// above. A golden, an invariant and a clause check are only worth what they
// cost if a wrong allocator turns them red, so every §4.3 switch in mutants.go
// is planted here one at a time and the check that catches it is named.
//
// Without this, the tie-break golden in particular is unfalsifiable decoration:
// it is the one assertion no invariant can adjudicate, and the measurement
// above shows the sort call alone does not guarantee it.
//
// It does not only ask WHETHER something went red. Each plant carries the set
// of assertion bodies that MUST redden, and the set is asserted as a lower
// bound — extra reds are welcome, a missing one is a failure. Without that,
// the pass condition would be "at least one of the three noticed", the
// per-body matrix published in assertAllocPolicy's doc comment would be
// guarded by nothing, and a change that stopped alloc_policy catching
// drop_against_natural_sum would leave the goldens catching it, this test
// green, and that comment silently false. The sets below were taken from a run
// of this test, not from the comment; if the two ever disagree the run wins.
//
// This test is one of the controls mutants.go's rule 3 carves out by name: it
// is untagged, it assigns `mutants`, and it therefore registers the restore
// with t.Cleanup before the first plant AND restores the zero value between
// plants, so neither a t.Fatal nor the next iteration runs against a
// switched-on defect.
func TestAllocatorMutantsRedenTheDirectChecks(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	t.Cleanup(func() { mutants = mutantSwitches{} })

	const (
		golden  = "alloc_golden"
		policy  = "alloc_policy"
		clauses = "relax_clauses"
	)
	planted := []struct {
		name   string
		defect string
		apply  func(*mutantSwitches)
		// wantRed is the LOWER BOUND on the assertion bodies this plant must
		// redden, measured by running this test rather than predicted.
		wantRed []string
	}{
		{"relax_skips_atomic_squeeze", "step 5(a) removed", func(m *mutantSwitches) { m.NoAtomicSqueeze = true },
			[]string{golden, policy, clauses}},
		{"relax_order_swapped", "step 5(b) before step 5(a)", func(m *mutantSwitches) { m.RelaxOrderSwapped = true },
			[]string{golden, policy, clauses}},
		{"relax_floor_is_one", "step 5(b) floor back to 1", func(m *mutantSwitches) { m.RelaxFloorOne = true },
			[]string{golden, policy, clauses}},
		// The ColState exemption is invisible to alloc_policy: removing it
		// produces an allocation that breaks no §4.3 arithmetic, only the
		// vocabulary rule. The goldens and the clause checks are what hold it.
		{"state_column_not_exempt_from_5b", "the ColState exemption removed", func(m *mutantSwitches) { m.NoStateExemption = true },
			[]string{golden, clauses}},
		// checkRelaxClauses works at MinWidth on the step-5 fixtures, where
		// the step-2 wording makes no difference; the sweep and the goldens
		// are what reach the widths at which it does.
		{"drop_against_natural_sum", "step 2 worded against the natural sum", func(m *mutantSwitches) { m.DropAgainstNatural = true },
			[]string{golden, policy}},
		{"chrome_off_by_one", "chromeFor undercounts by a cell", func(m *mutantSwitches) { m.ChromeOff = 1 },
			[]string{golden, policy, clauses}},
		{"chrome_over_by_one", "chromeFor overcounts by a cell", func(m *mutantSwitches) { m.ChromeOff = -1 },
			[]string{golden, policy, clauses}},
	}
	for _, p := range planted {
		mutants = mutantSwitches{}
		p.apply(&mutants)
		r := newResults()
		checkAllocGoldens(r)
		checkRelaxClauses(r)
		checkAllocPolicy(r)
		mutants = mutantSwitches{}

		if !r.any() {
			t.Errorf("SURVIVING MUTANT %s (%s): every golden, invariant and step-5 clause stayed green",
				p.name, p.defect)
			continue
		}
		names := r.redAssertions()
		red := make(map[string]bool, len(names))
		for _, n := range names {
			red[n] = true
		}
		var missing []string
		for _, want := range p.wantRed {
			if !red[want] {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s (%s): %v no longer catches it — red: %v, expected at least %v. "+
				"The per-body split in assertAllocPolicy's doc comment is now false; re-measure it and re-pin both.",
				p.name, p.defect, missing, names, p.wantRed)
		}
		t.Logf("%-34s %-44s red: %v", p.name, p.defect, names)
		t.Logf("    first violation: %s", r.first[names[0]])
	}
}

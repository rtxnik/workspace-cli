package output

import (
	"strconv" // the "N." step prefixes and numberWidth
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// §6.1's paired assertion, on tables.
//
// The width sweep alone is blind. If the renderer hands its computed total to
// lipgloss as .Width(total), lipgloss re-fits the grid to whatever number it is
// given and absorbs an arithmetic error as silent CONTENT LOSS instead of
// overflow. Measured over the 2,752-render sweep below: a chrome off-by-one
// produces 0 overflowing lines with the call in place where it produces 5,174
// without it — complete absorption. A chrome OVER-count produces 0 overflowing
// lines either way, with the call and without it — it renders narrower than
// allocated — and is visible only here: 2,748 grid_pairing violations without
// the call and 10,656 with it.
//
// So the render is held against what the allocator SAID it allocated:
//
//	(a) every grid line is exactly Alloc.Total cells wide;
//	(b) every cell's field is exactly Alloc.Widths[i] + 2 cells wide;
//	(c) a cell whose content fits its allocation is rendered in FULL;
//	(d) a cell that does not fit uses its allocation and abbreviates honestly.
//
// Which clause catches which defect is measured rather than assumed. With the
// pinned width on, a chrome off-by-one is caught by the bordered-row shape
// check in gridFields — lipgloss re-fits the row and the right-hand border
// goes with the content behind it — in all 2,752 renders of the sweep, and a
// chrome over-count is caught by (b) in 2,748 of them. Clause (c) is fired by
// none of this task's mutants: it stands against a renderer that loses content
// a cell had room for, and no switch here expresses that.
//
// Two rules hold throughout this file and the ones that follow it:
//
//   - The harness never measures with the code under test. Widths are taken
//     with ansi.StringWidth directly and never with W(), because W() is one of
//     the things being mutated; a harness that measured with the mutated
//     function would agree with the defect and report success.
//   - The harness owns its expectations. The truncation markers, the state
//     vocabulary and the border glyphs are declared in the test files from
//     §4.5, not read back out of the package, so a self-consistent renaming
//     cannot satisfy them.
//
// NOTHING IN THIS FILE RE-DECLARES A SHARED NAME. `results` is Task 4's,
// `squash` is Task 5's, `fxStateVocabulary`/`fxMark`/`fxBadge` are Task 2's,
// and `fixture`/`renderCase`/`sweepAssertion`/`newRenderCase`/`fxBudget`/
// `fxMarker*`/`fxTitle`/`fxRow` belong to the allocator harness in
// alloc_policy_test.go. Go has one package scope across every _test.go file: a
// second declaration is a compile error, and the failure reads like a merge
// accident rather than the contract breach it is.

// Border glyphs the harness looks for, declared here rather than read from
// glyph.go: the assertion must be able to fail when the renderer's borders
// change, not agree with whatever the renderer emitted.
const (
	fxVertUTF8  = "│"
	fxVertASCII = "|"
)

func fxVert(mode GlyphMode) string {
	if mode == GlyphASCII {
		return fxVertASCII
	}
	return fxVertUTF8
}

// fxCellSource is the plain text a cell must render, computed from §4.5's
// test-side vocabulary (fxBadge) rather than from the package's own
// stateText().
//
// Task 10 routes this through fxExpandTabs when the tab fixture arrives; until
// then there are no tabs in any fixture.
func fxCellSource(c Cell, mode GlyphMode) string {
	if c.isState {
		return fxBadge(c.state, c.Text, mode)
	}
	return c.Text
}

// fxFaithful reports whether rendered is an honest abbreviation of src: the
// whole of it, or a head and/or tail of it joined by a truncation marker, or a
// bare head or tail where the allocated width could not hold a marker.
//
// It deliberately does not re-implement truncate(): a test that recomputed the
// expected string with the same code the renderer uses would agree with that
// code's mistakes. What it asserts is the property truncation must HAVE — that
// no character appears that was not in the source, and that the text is cut
// from an end rather than from the middle of the meaning.
func fxFaithful(rendered, src string) bool {
	if rendered == src || rendered == "" {
		return true
	}
	for _, mk := range []string{fxMarkerUTF8, fxMarkerASCII} {
		if i := strings.Index(rendered, mk); i >= 0 {
			head, tail := rendered[:i], rendered[i+len(mk):]
			return strings.HasPrefix(src, head) && strings.HasSuffix(src, tail)
		}
	}
	return strings.HasPrefix(src, rendered) || strings.HasSuffix(src, rendered)
}

func bodyLineIndexes(n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, 3+i)
	}
	return out
}

// gridFields splits the bordered grid out of a table render: one []string of
// fields per grid row — the header row first, then the body rows — with the
// outer border stripped. A render whose shape does not match the geometry the
// allocator implies is a failure in itself: the harness refuses to silently
// check nothing.
//
// The length check is hoisted ABOVE every index into rc.plain, here and in
// assertGridPairing's grid-total loop. A mutant or a regression that makes the
// renderer emit fewer lines must fail with the diagnosis the assertion was
// written to give, not panic the sweep with an index-out-of-range.
func gridFields(rc renderCase, a Alloc, r *results) ([][]string, bool) {
	want := 4 + len(rc.fx.rows) // top rule, headers, header rule, rows, bottom rule
	if len(rc.plain) < want {
		r.fail("grid_pairing", "%s @ %d (mode %d): %d lines rendered, grid needs %d",
			rc.fx.name, rc.width, rc.mode, len(rc.plain), want)
		return nil, false
	}
	vert := fxVert(rc.mode)
	rowsOut := make([][]string, 0, 1+len(rc.fx.rows))
	for _, idx := range append([]int{1}, bodyLineIndexes(len(rc.fx.rows))...) {
		line := rc.plain[idx]
		if strings.HasPrefix(line, vert) && !strings.HasSuffix(line, vert) {
			// The shape lipgloss leaves behind when it is handed a width it
			// cannot honour: the row is re-fitted to that number and the
			// right-hand border — with whatever content was standing where it
			// used to be — is simply gone. This is the content loss §6.1 says
			// the overflow sweep cannot see, because the truncated line is no
			// wider than the budget.
			r.fail("grid_pairing", "%s @ %d (mode %d): line %d lost its right-hand border and the content behind it; "+
				"the row was re-fitted to %d cells: %q",
				rc.fx.name, rc.width, rc.mode, idx+1, ansi.StringWidth(line), line)
			return nil, false
		}
		if !strings.HasPrefix(line, vert) || !strings.HasSuffix(line, vert) {
			r.fail("grid_pairing", "%s @ %d (mode %d): line %d is not a bordered row: %q",
				rc.fx.name, rc.width, rc.mode, idx+1, line)
			return nil, false
		}
		fields := strings.Split(line, vert)
		fields = fields[1 : len(fields)-1] // drop the empty outer pieces
		if len(fields) != len(a.Kept) {
			r.fail("grid_pairing", "%s @ %d (mode %d): line %d has %d cells, the allocator kept %d",
				rc.fx.name, rc.width, rc.mode, idx+1, len(fields), len(a.Kept))
			return nil, false
		}
		rowsOut = append(rowsOut, fields)
	}
	return rowsOut, true
}

// assertGridPairing is §6.1's paired assertion, plus the width half for the
// lines the grid does not own (the caption).
//
// It is a sweepAssertion rather than a plain function because Task 10
// registers it in sweepAssertions() and Task 11 asks which assertion killed
// which mutant. TestTableGridPairing below is the standalone entry point.
//
// The width half reports under the assertion name "width_budget", whose own
// sweepAssertion arrives in Task 10; until then the name exists only as a
// failure label in this body.
var assertGridPairing = sweepAssertion{
	name: "grid_pairing",
	spec: "§6.1 paired assertion",
	what: "rendered grid width == Alloc.Total; each cell's field == its allocated width; no content lost that was allocated for",
	check: func(rc renderCase, r *results) {
		for i, line := range rc.plain {
			if got := ansi.StringWidth(line); got > rc.budget {
				r.fail("width_budget", "%s @ %d (mode %d): line %d is %d cells, budget %d: %q",
					rc.fx.name, rc.width, rc.mode, i+1, got, rc.budget, line)
			}
		}
		if !rc.fx.isTable {
			return
		}
		a := Allocate(rc.fx.cols, rc.fx.rows, rc.budget, rc.mode)
		if a.Total > rc.budget {
			r.fail("grid_pairing", "%s @ %d (mode %d): allocator returned Total %d over budget %d",
				rc.fx.name, rc.width, rc.mode, a.Total, rc.budget)
		}
		if len(a.Kept) == 0 {
			// §4.3 termination at n = 0: caption alone, no box. This is the
			// render-level half of the allocator harness's TestRelaxClauses.
			// Only the glyphs a caption cannot legitimately contain are
			// checked — "-" and "+" are ordinary prose, the rules and corners
			// are the tell.
			for _, glyph := range []string{fxVertUTF8, fxVertASCII, "─", "╭", "╮", "╰", "╯"} {
				if strings.Contains(rc.out, glyph) {
					r.fail("grid_pairing", "%s @ %d (mode %d): every column was dropped but the render still draws %q: %q",
						rc.fx.name, rc.width, rc.mode, glyph, rc.out)
				}
			}
			return
		}

		// The per-cell pass owns the length check, so run it first and let it
		// report a short render rather than indexing past the end here.
		fields, ok := gridFields(rc, a, r)
		if !ok {
			return
		}

		// Both directions matter. A grid WIDER than the allocator's total is an
		// overflow the sweep would also have seen; a grid NARROWER than it is
		// content the allocator paid for and the renderer did not draw, and the
		// sweep is blind to that by construction.
		for i := 0; i < 4+len(rc.fx.rows); i++ {
			if got := ansi.StringWidth(rc.plain[i]); got != a.Total {
				r.fail("grid_pairing", "%s @ %d (mode %d): grid line %d is %d cells, the allocator computed Total %d",
					rc.fx.name, rc.width, rc.mode, i+1, got, a.Total)
				break
			}
		}
		// The per-cell checks still run after a total mismatch: WHICH cell lost
		// the cell is the diagnosis, and the total alone does not say.

		for rowIdx, row := range fields {
			for k, col := range a.Kept {
				field := row[k]
				alloc := a.Widths[col]
				// fxTitle, not the raw Col.Title: the renderer draws the
				// heading through Col.title() and the allocator measures it
				// there too (D-13), so an expectation built from the raw field
				// would disagree with both for any heading that needed
				// scrubbing.
				title := fxTitle(rc.fx.cols[col])
				if got := ansi.StringWidth(field); got != alloc+2 {
					r.fail("grid_pairing", "%s @ %d (mode %d): row %d cell %q is %d cells, the allocator allocated %d (+2 padding)",
						rc.fx.name, rc.width, rc.mode, rowIdx, title, got, alloc)
					continue
				}
				src := title
				if rowIdx > 0 {
					src = ""
					if cells := rc.fx.rows[rowIdx-1]; col < len(cells) {
						src = fxCellSource(cells[col], rc.mode)
					}
				}
				content := strings.TrimSpace(field)
				switch {
				case ansi.StringWidth(src) <= alloc:
					// The allocation was big enough: the cell must be whole.
					if content != src {
						r.fail("grid_pairing", "%s @ %d (mode %d): %q allocated %d cells for %q but rendered %q — content lost",
							rc.fx.name, rc.width, rc.mode, title, alloc, src, content)
					}
				default:
					// A cut that lands on a wide cluster loses a cell, because a
					// two-cell cluster cannot be halved; TruncMid cuts at both
					// ends and can lose one at each. Anything beyond that is
					// content the allocator paid for and the render lost.
					slack := 1
					if rc.fx.cols[col].Trunc == TruncMid {
						slack = 2
					}
					if w := ansi.StringWidth(content); w > alloc || w < alloc-slack {
						r.fail("grid_pairing", "%s @ %d (mode %d): %q allocated %d cells but rendered %d (%q)",
							rc.fx.name, rc.width, rc.mode, title, alloc, w, content)
					}
					if !fxFaithful(content, src) {
						r.fail("grid_pairing", "%s @ %d (mode %d): %q rendered %q, which is not an abbreviation of %q",
							rc.fx.name, rc.width, rc.mode, title, content, src)
					}
				}
			}
		}
	},
}

// tableFixtures wraps the allocator harness's (cols, rows) pairs into the
// shared `fixture` shape with a renderer attached, and adds the ONE fixture
// §4.4's WideFlag contract needs: a valid, constructor-built table that drops a
// column and declares NO WideFlag.
//
// It takes no *testing.T, because Task 10's fxCorpus() — which has none — is
// built on top of it. A Col set the constructor refuses where the corpus needs
// it accepted, or accepts where the corpus needs it refused, is a broken plan
// rather than a test failure, so it panics with the diagnosis rather than
// returning a half-built corpus. (The same degenerate/valid split is asserted
// as a first-class check in the allocator harness's checkRelaxClauses.)
//
// Why the no-wide-flag fixture, measured rather than asserted. With it removed
// the corpus produces 98 drop-renders, 10 of them declaring no WideFlag — and
// 0 of those 10 from a Col set the constructor would accept: every one comes
// from a degenerate literal. With it in, the corpus produces 116
// drop-renders, 88 with a flag and 28 without, 18 of the 28 constructor-built.
//
// Hardcoding `clause += " (--wide)"` in place of the guard — the defect
// accepted review finding #12 exists to prevent — is killed either way: 10
// caption_discloses violations without this fixture, 28 with it. So the
// fixture is not what makes the mutant die. What it adds is the honest case:
// a table a command could actually build, dropping a column, with no flag to
// name. Before it, that case had 0 renders in the corpus.
func tableFixtures() []fixture {
	mk := func(name, spec string, cols []Col, rows [][]Cell, caption, wide string) fixture {
		tbl, err := NewTableBlock(cols, rows)
		if err != nil {
			panic(name + ": NewTableBlock rejected a Col set the corpus needs: " + err.Error())
		}
		tbl.Caption, tbl.WideFlag = caption, wide
		return fixture{
			name: name, kind: "table", spec: spec,
			isTable: true, cols: cols, rows: rows, caption: caption, wideFlag: wide,
			render: func(s *Stream) string { return tbl.Render(s) },
		}
	}
	lit := func(name, spec string, cols []Col, rows [][]Cell, caption, wide string) fixture {
		// Degenerate on purpose: validateCols refuses this Col set, and the
		// literal is how §4.3 step 5(b)/(c) is reached at all.
		if err := validateCols(cols); err == nil {
			panic(name + " is declared degenerate but the constructor accepts it")
		}
		tbl := Table{Cols: cols, Rows: rows, Caption: caption, WideFlag: wide}
		return fixture{
			name: name, kind: "table", spec: spec,
			isTable: true, cols: cols, rows: rows, caption: caption, wideFlag: wide,
			render: func(s *Stream) string { return tbl.Render(s) },
		}
	}

	pc, pr := fxProfilesCols()
	lc, lr := fxListCols()
	ec, er := fxEqualPrioCols()
	ac, ar := fxAtomicSqueezeCols()
	sc, sr := fxStateFloorCols()
	mc, mr := fxMarkerFloorCols()
	cc, cr := fxCaptionOnlyCols()

	return []fixture{
		mk("table/profiles", "§6.1 table", pc, pr, "8 profiles", "--wide"),
		mk("table/list", "§6.1 table + §6.5 state cells", lc, lr, "5 workspaces, 2 running, 2 via proxy", "--wide"),
		mk("table/equal-prio", "§4.3 step 2 tie-break", ec, er, "2 workspaces", "--wide"),
		mk("table/atomic-squeeze", "§4.3 step 5(a)", ac, ar, "2 workspaces", "--wide"),
		// §4.4's WideFlag contract: the same Col set, no flag declared. `ws
		// proxy profile list` has no --wide today, and the caption must not
		// invent one.
		mk("table/no-wide-flag", "§4.4 WideFlag contract — a valid table that drops a column with no flag",
			ec, er, "2 profiles", ""),
		lit("table/degenerate-state-floor", "§4.3 step 5(b) + the ColState exemption",
			sc, sr, "2 workspaces, 1 running", ""),
		lit("table/degenerate-marker-floor", "§4.3 step 5(b) floor = markerWidth, then 5(c)",
			mc, mr, "6 columns at the floor", ""),
		lit("table/degenerate-caption-only", "§4.3 termination at n = 0", cc, cr, "1 workspace", ""),
	}
}

// TestTableGridPairing sweeps every table fixture at every width from MinWidth
// to 200 in both glyph modes and applies §6.1's paired assertion to each.
func TestTableGridPairing(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	r := newResults()
	renders := 0
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for w := MinWidth; w <= 200; w++ {
			for _, fx := range tableFixtures() {
				assertGridPairing.check(newRenderCase(fx, w, mode), r)
				renders++
			}
		}
	}
	r.report(t)
	// A sweep that swept nothing must fail rather than pass quietly: without
	// this, an empty tableFixtures() reports success over zero renders.
	if renders == 0 {
		t.Fatal("no table fixture was swept: this assertion cannot fail")
	}
	t.Logf("%d table renders swept, widths %d..200, both glyph modes", renders, MinWidth)
}

// TestNewTableBlockRejectsImpossibleColSets is §4.3's construction-time
// rejection (accepted review finding #18), asserted from both sides.
func TestNewTableBlockRejectsImpossibleColSets(t *testing.T) {
	if _, err := NewTableBlock(fxEqualPrioCols()); err != nil {
		t.Errorf("a Col set whose un-droppable columns fit MinWidth was rejected: %v", err)
	}
	for _, c := range []struct {
		name string
		cols []Col
	}{
		{"un-droppable Min sum over MinWidth", []Col{
			{Title: "NAME", Prio: 1, Min: 20, Trunc: TruncMid},
			{Title: "STATUS", Prio: 1, Min: 12, Kind: ColState},
		}},
		{"Min below 1", []Col{{Title: "NAME", Prio: 1, Min: 0}}},
	} {
		if _, err := NewTableBlock(c.cols, [][]Cell{}); err == nil {
			t.Errorf("%s: NewTableBlock accepted a Col set §4.3 rejects at construction", c.name)
		}
	}
}

// §4.7's "a caption belongs to its table and goes to the same stream, always
// present".
//
// The STREAM half holds by construction: Table.Render returns one string, so
// there is no seam at which a caption could be routed elsewhere. The caller-
// side half — the caption/stream split in cmd/workspace.go's list renderer —
// belongs to the phase that moves `ws list` onto Table (phase 3), and is named
// as out of scope in the phase-0b definition of done.
//
// The ALWAYS-PRESENT half is checkable, and is checked here: a declared caption
// survives the render whole, at every width, once whitespace is collapsed. It
// is not a tautology — the caption is wrapped rather than truncated (§4.3), so
// a renderer that clipped it, or dropped it when the grid was already at the
// budget, or emitted it only above some width, fails.
func TestCaptionTravelsWithItsTable(t *testing.T) {
	checked := 0
	for _, fx := range tableFixtures() {
		if fx.caption == "" {
			continue
		}
		for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
			for w := MinWidth; w <= 200; w++ {
				rc := newRenderCase(fx, w, mode)
				if !strings.Contains(squash(strings.Join(rc.plain, "\n")), squash(fx.caption)) {
					t.Fatalf("%s @ %d (mode %d): the caption %q is not in the render whole:\n%s",
						fx.name, w, mode, fx.caption, strings.Join(rc.plain, "\n"))
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no fixture declares a caption: this assertion cannot fail")
	}
	t.Logf("%d renders carry their declared caption whole", checked)
}

// §4.4's block geometry, asserted as geometry.
//
// Measured before these assertions existed, on the reference implementation:
// rendering Problem's Facts and Steps in the opposite order left the whole
// suite green (ok wsrender 101.418s), and changing the Cause indent from 2 to 4
// while dropping the "N." numbering went red on exactly ONE thing — the
// reference's budget-28 control count, 421 -> 425 — which is a line
// count, not a geometry check. §6.1 says in terms that "a golden file at 80
// columns is not acceptance"; a pinned line count is less than that.
//
// Measured HERE, with the assertions below in place — each planted as a single
// substitution in blocks.go, matched once, and restored:
//
//	Facts and Steps swapped     @29 §4.4 orders Title, Cause, Facts, Steps;
//	                            got title=0 cause=2 facts=13 steps=7
//	Cause indent 2 -> 4         @29 Cause is indented 4 cells; §4.4 says 2
//	the "." dropped from "N."   @29 a declared section is missing: steps=-1
//	the key column not padded   @29 fact "profile" is not `2sp + key padded
//	                            to 9 + 2sp + value`: "  profile  go"
//	a border glyph written      @29 line 1 draws the border glyph "│"

// fxEscCause is §6.7's fixture: upstream text that CONTAINS control sequences —
// a line clear, a cursor-up, a cursor-position move, an OSC 8 hyperlink and an
// OSC 0 title rewrite. ansi.StringWidth counts every one of them as zero-width,
// so a width sweep over this text passes while the operator's terminal is being
// rewritten. That is the whole reason §4.4 sanitises rather than forwards.
// Measured: 9 ESC bytes in, ansi.StringWidth 70.
const fxEscCause = "\x1b[2K\x1b[1Aunable to pull image: " +
	"\x1b]8;;https://registry.example.invalid/help\x1b\\see the registry log\x1b]8;;\x1b\\" +
	"\x1b[3;7H \x1b]0;OWNED\x07 giving up after 3 attempts\x1b[0m"

// fxEscSurvivors is what must still be there once the sequences are stripped. A
// fixture whose text is deliberately altered in flight cannot declare its raw
// source as a fidelity claim, so it declares what must survive instead —
// otherwise "zero ESC bytes" is satisfied by emitting nothing at all.
var fxEscSurvivors = []string{"unable to pull image", "see the registry log", "giving up after 3 attempts"}

func fxProblem() Problem {
	return Problem{
		Title: "cannot start workspace \"api\"",
		Cause: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.",
		Facts: []Fact{{"workspace", "api"}, {"profile", "go"}, {"proxy", "de-fra-01"}},
		Steps: []Remedy{{"start docker", "sudo systemctl start docker"}, {"check", "ws proxy check"}},
	}
}

// plainLines is the render as the terminal shows it: SGR stripped, split into
// lines. Stripping is done with ansi.Strip and measuring with
// ansi.StringWidth — never with W(), which is one of the things being mutated.
func plainLines(out string) []string {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = ansi.Strip(l)
	}
	return lines
}

// blockStream is the sweep's own stream at GlyphUTF8: a TTY with colour ON, so
// every assertion below is forced to strip SGR to measure — the same
// arithmetic the terminal does. Asserting at ColourNone would let a render that
// mismeasures its own escapes pass.
//
// It delegates to sweepStream rather than building its own Stream, so a change
// to the sweep's colour level or TTY flag reaches every call site here too.
// Measured: blockStream(80) and sweepStream(80, GlyphUTF8) agree on width, TTY
// status, colour level and glyph mode, and fxProblem().Render(blockStream(80))
// carries 14 ESC bytes — so the stripping every assertion below does is real
// work rather than a no-op over plain text.
func blockStream(w int) *Stream {
	return sweepStream(w, GlyphUTF8)
}

// indentOf is the number of leading spaces on a line, measured in display cells
// with ansi.StringWidth rather than with W(): the harness never measures with
// the code under test.
func indentOf(line string) int {
	return ansi.StringWidth(line) - ansi.StringWidth(strings.TrimLeft(line, " "))
}

// TestProblemGeometry asserts §4.4's geometry directly, at every width from
// MinWidth to 200, rather than pinning a render at 80 columns.
func TestProblemGeometry(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	p := fxProblem()
	widestKey := 0
	for _, f := range p.Facts {
		if w := ansi.StringWidth(f.K); w > widestKey {
			widestKey = w
		}
	}
	widestLabel := 0
	for _, s := range p.Steps {
		if w := ansi.StringWidth(s.Label); w > widestLabel {
			widestLabel = w
		}
	}

	for w := MinWidth; w <= 200; w++ {
		lines := plainLines(p.Render(blockStream(w)))
		budget := clampBudget(w)

		// No line ever exceeds the budget, and no box is drawn: Problem is an
		// indented text block. A border around unbounded upstream text is what
		// turns overflow into shredded output — the audit measured such blocks
		// at 90 to 227 columns.
		for i, l := range lines {
			if got := ansi.StringWidth(l); got > budget {
				t.Fatalf("@%d line %d is %d cells, budget %d: %q", w, i+1, got, budget, l)
			}
			for _, glyph := range []string{"│", "─", "╭", "╮", "╰", "╯", "|"} {
				if strings.Contains(l, glyph) {
					t.Fatalf("@%d line %d draws the border glyph %q; Problem carries no box: %q", w, i+1, glyph, l)
				}
			}
		}

		// Title: column 0, with a 2-space hanging indent on continuation lines.
		if indentOf(lines[0]) != 0 {
			t.Fatalf("@%d the title is indented %d cells; §4.4 puts it at column 0: %q", w, indentOf(lines[0]), lines[0])
		}
		// Locate the declared sections by their first line. Facts and Steps are
		// found by content, not by position, so the ORDER assertion below is a
		// real check rather than a restatement of how the slices were indexed.
		idx := func(want string) int {
			for i, l := range lines {
				if strings.HasPrefix(strings.TrimLeft(l, " "), want) {
					return i
				}
			}
			return -1
		}
		iCause := idx("Cannot connect")
		iFacts := idx(p.Facts[0].K)
		iSteps := idx("1.")
		if iCause < 0 || iFacts < 0 || iSteps < 0 {
			t.Fatalf("@%d a declared section is missing: cause=%d facts=%d steps=%d\n%s",
				w, iCause, iFacts, iSteps, strings.Join(lines, "\n"))
		}
		// §4.4's declared ORDER: Title, Cause, Facts, Steps. This is the
		// assertion that a Facts/Steps swap fails — and, measured, that swap
		// left the reference's entire suite green.
		// Written as the disjunction rather than as `!(a && b && c)`: staticcheck's
		// QF1001 flags the negated conjunction, and `golangci-lint run` is part of
		// this task's acceptance gate.
		if iCause <= 0 || iCause >= iFacts || iFacts >= iSteps {
			t.Fatalf("@%d §4.4 orders Title, Cause, Facts, Steps; got title=0 cause=%d facts=%d steps=%d\n%s",
				w, iCause, iFacts, iSteps, strings.Join(lines, "\n"))
		}

		// Cause: indented 2. This is the assertion an indent change fails.
		if got := indentOf(lines[iCause]); got != 2 {
			t.Fatalf("@%d Cause is indented %d cells; §4.4 says 2: %q", w, got, lines[iCause])
		}

		// Facts: 2sp + key padded to the widest key + 2sp + value. Checked on
		// the SHORTEST key, where padding is observable: a renderer that did not
		// pad would put the value two cells earlier.
		//
		// EACH FACT LINE IS LOCATED BY ITS OWN `"  " + key` PREFIX, NOT BY AN
		// OFFSET FROM iFacts. A fact occupies one line only while its value
		// fits the value column; a value that does not fit wraps at the hanging
		// indent and the next fact is no longer at iFacts+k. This fixture
		// happens never to wrap — measured, its three facts are on consecutive
		// lines at all 172 widths from 29 to 200 — and that is exactly why an
		// offset index here would be an assumption nothing states.
		//
		// Below the point where renderPairs stacks the pair (§4.4: when
		// budget − valueIndent < 12), the aligned form does not apply.
		valueIndent := 2 + widestKey + 2
		if budget-valueIndent >= 12 {
			cursor := iFacts
			for _, f := range p.Facts {
				pfx := "  " + f.K
				for cursor < len(lines) && !strings.HasPrefix(lines[cursor], pfx) {
					cursor++
				}
				if cursor >= len(lines) {
					t.Fatalf("@%d no line begins with %q, so fact %q is not `2sp + key`:\n%s",
						w, pfx, f.K, strings.Join(lines, "\n"))
				}
				line := lines[cursor]
				cursor++
				want := strings.Repeat(" ", 2) + f.K + strings.Repeat(" ", widestKey-ansi.StringWidth(f.K)+2)
				if !strings.HasPrefix(line, want+f.V) {
					t.Fatalf("@%d fact %q is not `2sp + key padded to %d + 2sp + value`: %q",
						w, f.K, widestKey, line)
				}
			}
		}

		// Steps: 2sp + "N." + sp + label padded + 2sp + command — when the
		// command fits its aligned slot. This is the assertion that dropping the
		// "N." numbering fails.
		numberWidth := ansi.StringWidth(strconv.Itoa(len(p.Steps))) + 2 // "N." plus one space
		cmdIndent := 2 + numberWidth + widestLabel + 2

		// EACH STEP HEADER IS LOCATED BY ITS OWN `"  N."` PREFIX, NOT BY AN
		// OFFSET FROM iSteps. A step occupies ONE line only while its command
		// fits the aligned slot; §4.4 — the rule this same loop asserts further
		// down — drops a command that does not fit onto its own line, and a
		// command of C cells then wraps below C + 5 columns. Measured on this
		// fixture: step 1's 27-cell command is on its own line at the 17 widths
		// from 29 to 45, and wrapped at 29, 30 and 31. So lines[iSteps+k] is
		// step k only from 46 up. Measured against the implementation this task
		// prescribes, by replacing the scan below with `stepLine[k] = iSteps+k`:
		// the offset form is red at every width from 29 to 45 and green from 46
		// up. At budget 29 the block is
		//     11  "  1. start docker"
		//     12  "     sudo systemctl start"
		//     13  "     docker"
		//     14  "  2. check"
		//     15  "     ws proxy check"
		// so lines[iSteps+1] is step 1's wrapped command, and the assertion
		// fails with `@29 step 2 does not start with 2sp + "2."`.
		stepLine := make([]int, len(p.Steps))
		cursor := iSteps
		for k := range p.Steps {
			pfx := "  " + strconv.Itoa(k+1) + "."
			for cursor < len(lines) && !strings.HasPrefix(lines[cursor], pfx) {
				cursor++
			}
			if cursor >= len(lines) {
				t.Fatalf("@%d step %d header line not found:\n%s", w, k+1, strings.Join(lines, "\n"))
			}
			stepLine[k] = cursor
			cursor++
		}
		for k, s := range p.Steps {
			line := lines[stepLine[k]]
			prefix := "  " + strconv.Itoa(k+1) + "."
			// This one CANNOT fail while the scan above locates the header,
			// because the scan requires the same prefix. It is kept as the
			// guard for the offset form the comment above warns against:
			// measured, with `stepLine[k] = iSteps + k` planted in place of the
			// scan, this is the line that fires, at every width from 29 to 45
			// (`@29 step 2 does not start with 2sp + "2."`, on step 1's wrapped
			// command line). A renumbered or missing header reddens at the scan
			// instead, with `step N header line not found`.
			if !strings.HasPrefix(line, prefix) {
				t.Fatalf("@%d step %d does not start with `2sp + \"%d.\"`: %q", w, k+1, k+1, line)
			}
			if ansi.StringWidth(s.Cmd) <= budget-cmdIndent {
				want := prefix + " " + s.Label + strings.Repeat(" ", widestLabel-ansi.StringWidth(s.Label)+2) + s.Cmd
				if line != want {
					t.Fatalf("@%d step %d is not `2sp + \"N.\" + sp + label padded to %d + 2sp + command`:\n got  %q\n want %q",
						w, k+1, widestLabel, line, want)
				}
			} else {
				// §4.4: the command drops to its OWN line rather than being
				// truncated, and below roughly 60 columns it wraps — because the
				// width contract outranks copy-pasteability.
				if strings.Contains(line, s.Cmd) {
					t.Fatalf("@%d step %d kept a %d-cell command in a %d-cell slot: %q",
						w, k+1, ansi.StringWidth(s.Cmd), budget-cmdIndent, line)
				}
				// The command line is the line AFTER the header, which holds
				// only while the label itself did not wrap — a wrapped label's
				// continuation carries the same indent as the command and would
				// satisfy the indent check below for the wrong reason. So the
				// header is pinned to `2sp + "N." + sp + the WHOLE label` first.
				// Measured: with the label's wrap width cut to 6 the indent
				// check alone stays GREEN and this pin is what reddens.
				if wantHeader := prefix + " " + s.Label; line != wantHeader {
					t.Fatalf("@%d step %d's own-line header is not `2sp + \"N.\" + sp + label`:\n got  %q\n want %q",
						w, k+1, line, wantHeader)
				}
				// Hoisted above the index, as everywhere else in this file: a
				// renderer that emitted no command line must fail with the
				// diagnosis this branch was written to give, not panic the
				// sweep with an index-out-of-range.
				if stepLine[k]+1 >= len(lines) {
					t.Fatalf("@%d step %d dropped its command to its own line and then wrote no such line:\n%s",
						w, k+1, strings.Join(lines, "\n"))
				}
				cmdLine := lines[stepLine[k]+1]
				if indentOf(cmdLine) != 2+numberWidth {
					t.Fatalf("@%d step %d's command line is indented %d cells, want %d: %q",
						w, k+1, indentOf(cmdLine), 2+numberWidth, cmdLine)
				}
			}
		}
	}
}

// TestProblemCauseIsSanitised is §6.7, with a fixture that CONTAINS escape
// sequences rather than one merely checked for their absence.
//
// Without the survivor half, "zero ESC bytes on the TTY path" is satisfied by a
// renderer that emits nothing at all.
func TestProblemCauseIsSanitised(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	if n := strings.Count(fxEscCause, "\x1b"); n == 0 {
		t.Fatal("the §6.7 fixture carries no ESC bytes, so the assertion below cannot fail")
	}
	p := Problem{Title: "pull failed", Cause: fxEscCause}
	for w := MinWidth; w <= 200; w++ {
		// Rendered on a stream where colour is ON and the fd is a TTY: the
		// claim is that the layer strips the CHILD's sequences, not that it
		// happens to be running somewhere colour was already off.
		out := p.Render(blockStream(w))
		body := ansi.Strip(out)
		if strings.Contains(body, "\x1b") {
			t.Fatalf("@%d the sanitised body still carries an ESC byte: %q", w, body)
		}
		if strings.Contains(out, "OWNED") {
			t.Fatalf("@%d the OSC 0 title payload survived into the render: %q", w, out)
		}
		for _, want := range fxEscSurvivors {
			if !strings.Contains(squash(body), squash(want)) {
				t.Fatalf("@%d the prose %q did not survive sanitising: %q", w, want, body)
			}
		}
	}
}

// TestKVStacksBelowTwelve pins §4.4's stacking rule at its boundary, from both
// sides. The 12 here is the value column's readability threshold; the 12 in
// TestChecksFloorIsVocabularyWide below is the vocabulary-wide floor set by
// "degraded". They are different numbers that happen to be equal.
func TestKVStacksBelowTwelve(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	key := strings.Repeat("k", 20) // valueIndent = 2 + 20 + 2 = 24
	const value = "de-fra-01.example-vpn.net:443"
	k := KV{Title: "Report", Pairs: []Fact{{key, value}}}
	const valueIndent = 24

	// The pair lines start at index 1 only while the title occupies exactly one
	// line, which this fixture's six-cell title does at both budgets below.
	// Asserted rather than assumed: every index into these two renders is an
	// offset, and a title that wrapped would shift all of them.
	mustTitled := func(what string, lines []string, want int) {
		t.Helper()
		if len(lines) < want || lines[0] != "Report" {
			t.Fatalf("%s: want at least %d lines with %q on line 1, got %d lines:\n%s",
				what, want, "Report", len(lines), strings.Join(lines, "\n"))
		}
	}

	// budget − valueIndent == 12: aligned. The value starts on the key's line.
	aligned := plainLines(k.Render(blockStream(valueIndent + 12)))
	mustTitled("at budget − valueIndent = 12", aligned, 2)
	if !strings.HasPrefix(aligned[1], "  "+key+"  ") {
		t.Errorf("at budget − valueIndent = 12 the pair must stay aligned: %q", aligned[1])
	}
	if strings.TrimSpace(aligned[1]) == key {
		t.Errorf("at budget − valueIndent = 12 the pair stacked; §4.4 stacks below 12, not at it: %q", aligned[1])
	}

	// budget − valueIndent == 11: stacked. Key alone, value indented beneath.
	stacked := plainLines(k.Render(blockStream(valueIndent + 11)))
	mustTitled("at budget − valueIndent = 11", stacked, 3)
	if strings.TrimSpace(stacked[1]) != key {
		t.Errorf("at budget − valueIndent = 11 the key must stand alone on its line: %q", stacked[1])
	}
	if got := indentOf(stacked[2]); got != 4 {
		t.Errorf("a stacked value is indented %d cells, want 4 (the key's indent + 2): %q", got, stacked[2])
	}
}

// TestChecksFloorIsVocabularyWide asserts that the badge column is sized from
// §4.5's whole vocabulary and not from the items present.
//
// The fixture contains ONLY StateOK. A content-sized implementation would
// render `✓ ok  binary present` and pass any assertion written against the
// items it was given; this one fails it. The reason the rule matters is that a
// content-sized column re-aligns between two runs of the same command — `ws
// proxy doctor` would indent its names at column 6 with everything passing and
// at column 14 with one check degraded.
func TestChecksFloorIsVocabularyWide(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	// The expected geometry, re-derived from the code under test — the badge
	// column is whatever widestStateMark and widestStateWord say it is, so the
	// loop below fails a renderer that sized the column from c.Items even
	// though it cannot fail a change to the vocabulary. The literal 12 after
	// the loop is the independent expectation that catches THAT: §4.4 fixes the
	// floor at 2 + mark 1 + sp + "degraded" 8. An earlier draft computed
	// wantFloor from four constants declared three lines above it and then
	// compared it to 12, so nothing under test could make it fail.
	markCells := widestStateMark(GlyphUTF8)
	wordCells := widestStateWord()
	const indent, gap = 2, 2
	wantNameIndent := indent + markCells + 1 + wordCells + gap // 14
	wantFloor := indent + markCells + 1 + wordCells            // 12

	c := Checks{Title: "Doctor", Items: []Check{
		{"binary present", StateOK, ""},
		{"config exists", StateOK, ""},
	}}
	for w := MinWidth; w <= 200; w++ {
		lines := plainLines(c.Render(blockStream(w)))
		// Line 1+k is item k only while no name wraps and no item carries a
		// note. The fixture is built so that neither happens — its widest name
		// is 14 cells against a name column of budget − 14 = 15 at MinWidth —
		// and this is the precondition that says so, rather than leaving the
		// offset index below resting on it silently.
		if len(lines) != 1+len(c.Items) {
			t.Fatalf("@%d the fixture rendered %d lines, want %d (title plus one per item); "+
				"the offset index below would be reading the wrong line:\n%s",
				w, len(lines), 1+len(c.Items), strings.Join(lines, "\n"))
		}
		for k := range c.Items {
			line := lines[1+k]
			badge := "✓ ok"
			want := strings.Repeat(" ", indent) + badge +
				strings.Repeat(" ", markCells+1+wordCells-ansi.StringWidth(badge)) +
				strings.Repeat(" ", gap)
			if !strings.HasPrefix(line, want) {
				t.Fatalf("@%d item %d: the badge column is sized from the items present, not from §4.5's vocabulary; "+
					"want the name to start at column %d (2 + mark 1 + sp + %q 8 + gap 2): %q",
					w, k, wantNameIndent, "degraded", line)
			}
		}
	}
	if wantFloor != 12 {
		t.Fatalf("the Checks floor arithmetic drifted: 2 + mark %d + 1 + widest word %d is %d, not 12",
			markCells, wordCells, wantFloor)
	}
	// And the block still fits at the product floor, where the name column is
	// budget − nameIndent = 29 − 14 = 15 cells wide.
	lines := plainLines(c.Render(blockStream(MinWidth)))
	for i, l := range lines {
		if got := ansi.StringWidth(l); got > MinWidth {
			t.Fatalf("@%d line %d is %d cells: %q", MinWidth, i+1, got, l)
		}
	}
}

// TestEmptyNamesTheNextStep is §4.9's one shape: exit 0, always naming the next
// step, no numbering. The assertion is that Empty's remedies are NOT numbered —
// the shape differs from Problem's on purpose, and nothing else would notice.
func TestEmptyNamesTheNextStep(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	e := Empty{Subject: "workspaces", Steps: []Remedy{
		{"Create one", "ws new <name>"},
		{"See profiles", "ws profiles"},
	}}
	for w := MinWidth; w <= 200; w++ {
		lines := plainLines(e.Render(blockStream(w)))
		if lines[0] != "No workspaces yet." {
			t.Fatalf("@%d the subject line is %q, want %q", w, lines[0], "No workspaces yet.")
		}
		// Line 1+k is remedy k only while the subject line, every label and
		// every command stay on one line each. The fixture is built so that
		// they do — its widest command is 13 cells against an aligned slot of
		// budget − 16 = 13 at MinWidth — and this precondition says so instead
		// of leaving the offset index resting on it silently.
		if len(lines) != 1+len(e.Steps) {
			t.Fatalf("@%d the fixture rendered %d lines, want %d (subject plus one per remedy); "+
				"the offset index below would be reading the wrong line:\n%s",
				w, len(lines), 1+len(e.Steps), strings.Join(lines, "\n"))
		}
		for k, s := range e.Steps {
			line := lines[1+k]
			if strings.HasPrefix(strings.TrimLeft(line, " "), strconv.Itoa(k+1)+".") {
				t.Fatalf("@%d Empty's remedies are numbered; §4.9 numbers Problem's, not these: %q", w, line)
			}
			if indentOf(line) != 2 {
				t.Fatalf("@%d remedy %q is indented %d cells, want 2: %q", w, s.Label, indentOf(line), line)
			}
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got > clampBudget(w) {
				t.Fatalf("@%d line %d is %d cells: %q", w, i+1, got, l)
			}
		}
	}
}

// TestStreamStateHelpers covers §4.5's stream-level seam: the two exported
// methods a call site uses when it builds a one-off line rather than a block.
// They have no caller inside the layer, so without this they would ship
// unexercised — and an exported symbol is invisible to golangci-lint's
// `unused`, which is the check that catches the unexported ones.
//
// The expectations come from fxBadge/fxMark — the harness's own copy of §4.5's
// table — and not from stateText, so a self-consistent renaming of the
// vocabulary cannot satisfy them.
func TestStreamStateHelpers(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		s := sweepStream(80, mode)
		for _, st := range allStates {
			if got, want := s.StateMark(st), fxMark(st, mode); got != want {
				t.Errorf("mode %d: StateMark(%v) = %q, want %q", mode, st, got, want)
			}
			if got, want := s.StateText(st, ""), fxBadge(st, "", mode); got != want {
				t.Errorf("mode %d: StateText(%v, \"\") = %q, want %q", mode, st, got, want)
			}
			if got, want := s.StateText(st, "running"), fxBadge(st, "running", mode); got != want {
				t.Errorf("mode %d: StateText(%v, \"running\") = %q, want %q", mode, st, got, want)
			}
		}
	}
}

package output

import (
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

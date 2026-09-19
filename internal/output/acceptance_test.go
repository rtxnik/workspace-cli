package output

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv" // the "N." step prefixes and numberWidth
	"strings"
	"testing"
	"unicode/utf8"

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
//     with ansi.StringWidth directly and never with W(), because W() is
//     production code this suite has to be able to disagree with — as are
//     firstCell, Wrap, marker and markerWidth, which the mutation switches
//     also reach. A harness that measured with any of them would agree with
//     its mistakes and report success.
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
// It composes the cell the way Cell.display does, in the same order: sanitise
// first (D-13), then expand tabs, because that is what the renderer does —
// display returns SanitiseInline(Text) and the truncation and padding
// primitives expand tabs afterwards.
//
// Both were measured, each with the other in place and the layer correct:
//
//	without fxExpandTabs    688 grid_pairing violations, first `"TOOLS\tSET"
//	                        allocated 18 cells for "go\tnode\tpython" but
//	                        rendered "go      node    p…" — content lost`
//	without SanitiseInline  688 grid_pairing violations, first `"STATUS"
//	                        rendered "unable to pull im…", which is not an
//	                        abbreviation of "\x1b[2K\x1b[1Aunable to …"`
//
// In each case the harness is demanding that the renderer NOT do something it
// is obliged to do. SanitiseInline is CONSUMED here rather than re-derived, on
// the same grounds as fxTitle and fxNatural in alloc_policy_test.go: the
// sanitiser is pinned by text_invariants_test.go, and a second copy of it in
// the harness would pin nothing that file does not already pin. The tab rule
// is re-derived, because nothing else pins it.
func fxCellSource(c Cell, mode GlyphMode) string {
	if c.isState {
		return fxExpandTabs(fxBadge(c.state, SanitiseInline(c.Text), mode))
	}
	return fxExpandTabs(SanitiseInline(c.Text))
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

// assertGridPairing is §6.1's paired assertion, on the grid alone.
//
// It is a sweepAssertion rather than a plain function because sweepAssertions()
// registers it beside the corpus-wide checks and TestMutationHarness in
// mutation_test.go asks which assertion killed which mutant. TestTableGridPairing
// below is the standalone entry point.
//
// THE WIDTH HALF IS NOT HERE ANY MORE. It began life as this body's first
// loop, and assertWidthBudget in this file now owns it, so that every fixture
// in fxCorpus() gets it and not only the tables. Every harness that runs this
// assertion must run assertWidthBudget beside it — TestTableGridPairing below
// and TestTableMutantsRedenTheBlockChecks in disclosure_test.go both do —
// because caption_wrapped_too_wide is killed by that loop and by nothing else.
var assertGridPairing = sweepAssertion{
	name: "grid_pairing",
	spec: "§6.1 paired assertion",
	what: "rendered grid width == Alloc.Total; each cell's field == its allocated width; no content lost that was allocated for",
	check: func(rc renderCase, r *results) {
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
				// fxExpandTabs for the same reason fxCellSource applies it:
				// the renderer draws the heading through clipTail and Pad,
				// both of which expand tabs before they measure. Measured with
				// this call removed and the layer correct: 344 grid_pairing
				// violations on the heading alone, the 688 above being the
				// cells' share of the same 1032.
				src := fxExpandTabs(title)
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
// It takes no *testing.T, because fxCorpus() in corpus_test.go — which has
// none — is built on top of it. A Col set the constructor refuses where the corpus needs
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
//
// It runs assertWidthBudget beside it because the width loop USED TO BE
// assertGridPairing's first clause and now lives in its own assertion. Without
// the extra line this harness would measure no line width at all — splitting an
// assertion is never a local edit, and every harness that ran the original has
// to be told about the half that moved.
func TestTableGridPairing(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	r := newResults()
	renders := 0
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for w := MinWidth; w <= 200; w++ {
			for _, fx := range tableFixtures() {
				rc := newRenderCase(fx, w, mode)
				assertWidthBudget.check(rc, r)
				assertGridPairing.check(rc, r)
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
// ansi.StringWidth — never with W(), which is production code this suite has
// to be able to disagree with.
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
		// A step header is located by its SHAPE — exactly two leading spaces
		// and then a digit — and never by the number it is expected to carry,
		// so the number is left for an assertion to check rather than being
		// baked into the locator. Measured on this fixture: the shape locator
		// and a `1.` locator agree at all 172 swept widths, and exactly 2 lines
		// per render are header-shaped at every one of them, so nothing in the
		// Title, the Cause or the Facts is mistaken for a step.
		isHeader := func(l string) bool {
			rest := strings.TrimPrefix(l, "  ")
			return len(l)-len(rest) == 2 && rest != "" && rest[0] >= '0' && rest[0] <= '9'
		}
		iCause := idx("Cannot connect")
		iFacts := idx(p.Facts[0].K)
		iSteps := -1
		for i, l := range lines {
			if isHeader(l) {
				iSteps = i
				break
			}
		}
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

		// Title: the continuation lines carry the 2-space hanging indent. They
		// are the lines between the title's first line and the Cause, and the
		// whole point of wrapping the title at budget-hang rather than at
		// budget is that they have a known width to be indented into. Without
		// this, deleting the indent from Problem.Render passed every other
		// assertion in the task.
		//
		// NARROW BY CONSTRUCTION: fxProblem's 28-cell title only wraps where
		// budget-hang is under 28, which on the swept range is w == 29 alone —
		// measured, there is 1 continuation line at 1 of the 172 widths and 0
		// at the other 171. That is enough to catch the deletion and no more; a
		// long-title fixture belongs in the corpus rather than here.
		for i := 1; i < iCause; i++ {
			if got := indentOf(lines[i]); got != 2 {
				t.Fatalf("@%d title continuation line %d is indented %d cells; §4.4 hangs it by 2: %q",
					w, i+1, got, lines[i])
			}
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
		// command fits its aligned slot. The prefix clause further down is the
		// assertion that dropping or misnumbering the "N." fails, and it is the
		// clause two plants below were written to redden.
		numberWidth := ansi.StringWidth(strconv.Itoa(len(p.Steps))) + 2 // "N." plus one space
		cmdIndent := 2 + numberWidth + widestLabel + 2

		// EACH STEP HEADER IS LOCATED BY ITS SHAPE, NOT BY AN OFFSET FROM
		// iSteps AND NOT BY THE NUMBER IT SHOULD CARRY. A step occupies ONE
		// line only while its command fits the aligned slot; §4.4 — the rule
		// this same loop asserts further down — drops a command that does not fit onto its own line, and a
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
			for cursor < len(lines) && !isHeader(lines[cursor]) {
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
			// The scan above matches a header by shape alone, so THIS is the
			// clause that checks the number, and it can fail on the code under
			// test rather than only on a mutation of this harness. Measured,
			// one plant each: dropping the "." from the numbering reddens it
			// with `@29 step 1 does not start with 2sp + "1.": "  1  start
			// docker"`, and numbering every step "1." reddens it with `@29 step
			// 2 does not start with 2sp + "2.": "  1. check"`. It also still
			// catches the offset form the comment above warns against: with
			// `stepLine[k] = iSteps + k` planted in place of the scan it fires
			// at every width from 29 to 45.
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
// proxy doctor` would indent its names at column 8 with everything passing and
// at column 14 with one check degraded — both figures being the gap-inclusive
// name indent, 2 + mark + sp + word + gap, so `2 + 1 + 1 + 2 + 2` against
// `2 + 1 + 1 + 8 + 2`. Measured on the content-sized plant, whose render is
// `"  ✓ ok  binary present"`.
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

// =================================================== the corpus-wide sweep
//
// What follows is the §6.1/§6.2 acceptance sweep: the whole corpus of
// corpus_test.go, at every width from MinWidth to sweepMaxWidth, in both glyph
// modes, held against every assertion in sweepAssertions().
//
// Two rules hold throughout, and are the reason the harness is worth anything:
//
//   - The harness never measures with the code under test. Widths are taken
//     with ansi.StringWidth directly and never through W(), because W() is
//     production code this suite has to be able to disagree with: a harness
//     that measured with it would agree with its mistakes and report success.
//     The same holds for every other primitive this harness could have leaned
//     on: the switches aimed at the text layer are spread across W, firstCell
//     and Wrap in text.go and marker/markerWidth in glyph.go, so "measure with
//     x/ansi, never with the package" is the only rule that covers all of
//     them. It is the line between a harness that can adjudicate and one that
//     cannot.
//   - The harness owns its expectations. The state vocabulary, the truncation
//     markers and the border glyphs are declared in the test files from §4.5
//     and §4.4, not read back out of the package, so a self-consistent
//     renaming cannot satisfy them.

// globalAssertion is one property that needs its own renders, or no renders at
// all, rather than a pass over the sweep.
type globalAssertion struct {
	name  string
	spec  string
	what  string
	check func(r *results)
}

// sweepStats is what one pass over the corpus measured.
type sweepStats struct {
	renders int
	lines   int
	digest  string // over every rendered byte: the vacuity check of a mutant run
}

const sweepMaxWidth = 200

// runSweep renders every fixture at every width in [lo, hi] in both glyph
// modes and applies every supplied assertion to each render.
//
// It builds its renderCases with newRenderCase, which is the same constructor
// TestAllocPolicy, TestTableGridPairing and TestCaptionDiscloses use: one
// render path, so a defect cannot hide behind a second one.
//
// The digest is not decoration. It is the only thing that lets a mutation
// harness distinguish "no assertion caught this mutant" from "this mutant
// changed nothing at all", and those two outcomes must never be reported the
// same way.
func runSweep(lo, hi int, checks []sweepAssertion, r *results) sweepStats {
	h := sha256.New()
	st := sweepStats{}
	corpus := fxCorpus()
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for w := lo; w <= hi; w++ {
			for _, fx := range corpus {
				rc := newRenderCase(fx, w, mode)
				st.renders++
				st.lines += len(rc.lines)
				_, _ = fmt.Fprintf(h, "%s|%d|%d|%s\x00", fx.name, w, mode, rc.out)
				for _, a := range checks {
					a.check(rc, r)
				}
			}
		}
	}
	st.digest = hex.EncodeToString(h.Sum(nil))
	return st
}

// ------------------------------------------------------- §6.1 width budget

// assertWidthBudget is the property test of §6.1: every line of every render of
// every block type and every message helper, at every width from MinWidth to
// sweepMaxWidth, is at most the budget wide.
//
// It was assertGridPairing's first clause, and it is LIFTED OUT here rather
// than duplicated: a table render still gets it, and so now does every
// Problem, Empty, KV, Checks and message render in the corpus.
//
// Goes red when: the allocator's arithmetic is wrong by a cell (chrome
// off-by-one), when a truncation marker is emitted without being reserved,
// when a width is counted in runes instead of cells, when a wrapped block
// ignores the indent it is printed at, or when a message helper stops
// wrapping.
var assertWidthBudget = sweepAssertion{
	name: "width_budget",
	spec: "§6.1",
	what: "ansi.StringWidth(line) <= budget for every line of every render, MinWidth..200",
	check: func(rc renderCase, r *results) {
		for i, line := range rc.plain {
			if got := ansi.StringWidth(line); got > rc.budget {
				r.fail("width_budget", "%s @ %d (mode %d): line %d is %d cells, budget %d: %q",
					rc.fx.name, rc.width, rc.mode, i+1, got, rc.budget, line)
			}
		}
	},
}

// THE TWO HALVES OF §6.1's PAIRED ASSERTION, AND WHY BOTH.
//
// §6.1 asks for two things: the rendered grid width equals the allocator's
// computed total, AND cell content equals what the allocator said it
// allocated. They are not redundant, and each owns a defect class the other
// cannot see:
//
//   - a chrome UNDER-count makes the render wider than the budget, so
//     width_budget sees it — unless the total is also handed to lipgloss as
//     .Width(), which re-fits the row and absorbs the error as content loss.
//     Then only the paired assertion is left.
//   - a chrome OVER-count makes the render NARROWER than the budget. Nothing
//     overflows, so width_budget is silent in both directions, and the
//     grid-total clause is the only clause OF THE PAIR that sees it.
//
// Those two shapes are asserted, not assumed: TestTableMutantsRedenTheBlockChecks
// in disclosure_test.go plants chrome_off_by_one and chrome_over_by_one each
// combined with lipgloss_width_pinning and requires both to be killed.
// chrome_over_by_one is also seen by alloc_policy, which is an argument for
// registering assertAllocPolicy in the sweep registry rather than an argument
// against the paired assertion.

// ------------------------------------------------------------ §6.3 UTF-8

// assertValidUTF8 is §6.3. Goes red when any cut is taken in BYTES rather than
// on a grapheme-cluster boundary — the measured defect it guards is
// `tools[:maxTools-1]` (cmd/profile.go), which slices mid-rune and emits
// invalid UTF-8. §6.3 records that this "fails today".
var assertValidUTF8 = sweepAssertion{
	name: "valid_utf8",
	spec: "§6.3",
	what: "every rendered byte sequence decodes as UTF-8",
	check: func(rc renderCase, r *results) {
		if !utf8.ValidString(rc.out) {
			r.fail("valid_utf8", "%s @ %d (mode %d) is not valid UTF-8", rc.fx.name, rc.width, rc.mode)
		}
	},
}

// -------------------------------------------------- §6.5 structural colour

// assertStateStructure is §6.5: colour is decorative, so the assertion is
// STRUCTURAL, not photometric. Every rendered state carries its mark AND its
// word. Four parts, because four different things can go wrong:
//
//	(1) a message helper's shape is a mark then prose, so its mark must still
//	    be at the head of the first line however narrow the stream is;
//	(2) blocks that never abbreviate a badge — Checks pads to the vocabulary's
//	    widest mark and word — must show the whole "mark word" at every width.
//	    The badge is composed by fxBadge from the harness's own copy of §4.5,
//	    with an empty label falling back to the state's own word, so this is
//	    what pins stateText's fallback and its single-space separator BY BYTES
//	    rather than by width;
//	(3) a state cell in a table must never lose its MARK, at any width, even
//	    when the allocator has squeezed it: `-` alone renders `- stopped` and
//	    `- not created` identically, which is the ambiguity §4.5 exists to
//	    remove;
//	(4) a state column must never be taken below its Min by step 5(b) — §4.3
//	    exempts it precisely so that (3) stays satisfiable — and must be
//	    dropped instead.
//
// FIVE plants, one per part plus one more on the vocabulary all four read,
// each applied alone and restored. Four of the five are caught by this
// assertion and by nothing else:
//
//	(1) renderMessage's first line emitted   1376, ALONE, first
//	    without its prefix                   "message/success-cjk @ 29: first
//	                                         line \"工作区已启动：拨号失败：连\"
//	                                         does not start with the mark \"✓ \""
//	(2) stateText's empty-label fallback      4816, ALONE, first
//	    upper-cased                          "checks/proxy-doctor @ 29:
//	                                         \"✓ ok\" missing from the render"
//	(3) a state cell cut from the head, so    1372, ALONE, first
//	    the squeeze eats the mark instead    "table/list @ 29: squeezed state
//	    of the word                          cell \"…starting\" lost its mark"
//	(4) the ColState exemption removed        8, ALONE, first
//	    from step 5(b)                       "table/degenerate-state-floor @ 29:
//	                                         state column \"STATUS\" allocated
//	                                         11, below its Min 12" — alloc_policy
//	                                         does NOT see this one
//	    stateText's separator changed to a   12382 (grid_pairing sees it too);
//	    same-width character                 this one reaches parts (2) and (3)
//	                                         and NOT part (1), because
//	                                         renderMessage builds its prefix
//	                                         from stateMark directly and never
//	                                         calls stateText
//
// §6.5 also records, so it is not rediscovered, that a 4.5:1 contrast gate
// against BOTH a light and a dark background is unsatisfiable in sRGB — the
// window is empty and the theoretical ceiling is 3.84:1 — so no photometric
// gate is adopted and none may be added here.
var assertStateStructure = sweepAssertion{
	name: "state_mark_and_word",
	spec: "§6.5",
	what: "every rendered state carries its mark and its word; state columns keep their mark and are never relaxed below Min",
	check: func(rc renderCase, r *results) {
		joined := strings.Join(rc.plain, "\n")
		if rc.fx.hasPrefixState {
			want := fxMark(rc.fx.prefixState, rc.mode) + " "
			if !strings.HasPrefix(rc.plain[0], want) {
				r.fail("state_mark_and_word", "%s @ %d (mode %d): first line %q does not start with the mark %q",
					rc.fx.name, rc.width, rc.mode, rc.plain[0], want)
			}
		}
		for _, want := range rc.fx.states {
			badge := fxBadge(want.st, want.label, rc.mode)
			if !strings.Contains(joined, badge) {
				r.fail("state_mark_and_word", "%s @ %d (mode %d): %q missing from the render",
					rc.fx.name, rc.width, rc.mode, badge)
			}
		}
		if !rc.fx.isTable {
			return
		}
		// Parts (3) and (4) report on ColState columns and on nothing else, so
		// a table with none of them can be answered without an Allocate and a
		// gridFields — both of which assertGridPairing has already run over
		// this same render. Measured: 9 of the 18 table fixtures declare no
		// state column, exactly half, which is 3,096 of the sweep's Allocate
		// calls removed.
		//
		// IT DOES NOT MAKE THE SWEEP FASTER, and the number is recorded here
		// so nobody looks for the saving again. Measured five runs each way,
		// the sweep is 4.15-4.30s with this early return and 4.18-4.32s
		// without: one noise band. The cost is not in the assertions. Timed
		// separately over the same 16,856 renders, building the renderCases
		// alone takes 3.88s, and no single assertion in sweepAssertions()
		// adds more than 64ms on top of it. A harness that needs the sweep to
		// be cheaper has to render fewer times — fewer widths or fewer
		// fixtures — not assert less.
		hasStateCol := false
		for _, col := range rc.fx.cols {
			if col.Kind == ColState {
				hasStateCol = true
				break
			}
		}
		if !hasStateCol {
			return
		}
		a := Allocate(rc.fx.cols, rc.fx.rows, rc.budget, rc.mode)
		for i, col := range rc.fx.cols {
			if col.Kind != ColState || a.Widths[i] < 0 {
				continue
			}
			if a.Widths[i] < col.Min {
				r.fail("state_mark_and_word", "%s @ %d (mode %d): state column %q allocated %d, below its Min %d",
					rc.fx.name, rc.width, rc.mode, col.Title, a.Widths[i], col.Min)
			}
			for _, relaxed := range a.Relaxed {
				if relaxed == fxTitle(col) {
					r.fail("state_mark_and_word", "%s @ %d (mode %d): state column %q was relaxed by step 5(b); §4.3 exempts it",
						rc.fx.name, rc.width, rc.mode, col.Title)
				}
			}
		}
		if len(a.Kept) == 0 {
			return
		}
		fields, ok := gridFields(rc, a, r)
		if !ok {
			return
		}
		for rowIdx, row := range fields[1:] {
			for k, col := range a.Kept {
				if rc.fx.cols[col].Kind != ColState {
					continue
				}
				cells := rc.fx.rows[rowIdx]
				if col >= len(cells) {
					continue
				}
				src := fxCellSource(cells[col], rc.mode)
				content := strings.TrimSpace(row[k])
				mark := fxMark(cells[col].state, rc.mode)
				if a.Widths[col] >= ansi.StringWidth(src) {
					if content != src {
						r.fail("state_mark_and_word", "%s @ %d (mode %d): state cell had room for %q but rendered %q",
							rc.fx.name, rc.width, rc.mode, src, content)
					}
					continue
				}
				if !strings.HasPrefix(content, mark+" ") && content != mark {
					r.fail("state_mark_and_word", "%s @ %d (mode %d): squeezed state cell %q lost its mark %q",
						rc.fx.name, rc.width, rc.mode, content, mark)
				}
			}
		}
	},
}

// sweepAssertions is the registry runSweep is driven with.
func sweepAssertions() []sweepAssertion {
	return []sweepAssertion{
		assertWidthBudget,
		assertGridPairing,
		assertValidUTF8,
		assertStateStructure,
		assertAllocPolicy,      // §4.3 against the spec, not against Allocate — alloc_policy_test.go
		assertCaptionDiscloses, // §4.3 disclosure clause — disclosure_test.go
		assertContentFidelity,  // §4.4 wrapping, outside the grid — disclosure_test.go
	}
}

// --------------------------------------------------- §6.7 ESC containment

func escCount(s string) int { return strings.Count(s, "\x1b") }

// nonSGRSequences returns the escape sequences in s that are NOT plain SGR
// (CSI … m). Those are the ones that move the cursor, clear the screen,
// rewrite the window title or plant a hyperlink — the ones a container's
// stderr must never be able to reach the operator's terminal with.
func nonSGRSequences(s string) []string {
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] != 0x1b {
			continue
		}
		if i+1 >= len(rs) {
			out = append(out, "bare ESC")
			continue
		}
		switch rs[i+1] {
		case '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) && rs[j] != 'm' {
				out = append(out, fmt.Sprintf("CSI %c", rs[j]))
			}
			i = j
		case ']':
			out = append(out, "OSC")
			i++
		default:
			out = append(out, fmt.Sprintf("ESC %c", rs[i+1]))
			i++
		}
	}
	return out
}

// assertESCContainment is §6.7, in three parts:
//
//	(a) the whole corpus rendered at ColourNone yields zero ESC bytes;
//	(b) a Problem whose Cause CONTAINS cursor moves, a line clear, an OSC
//	    hyperlink and a title rewrite renders with zero ESC bytes on the TTY
//	    path — and with none of the payload those sequences carried;
//	(c) the same for message text, because the helpers travel the same
//	    sanitising path and are 137 of the ~157 call sites.
//
// It also asserts the FIXTURE is potent: ansi.StringWidth measures the raw
// Cause as narrower than a terminal would show it, which is exactly why the
// width sweep alone cannot see this defect.
//
// §6.7's other clause — "NO_COLOR=1 and a piped stream both yield zero ESC
// bytes" — is asserted at the colour probe in stream_contract_test.go (D-7),
// where the environment seam actually lives. What is asserted HERE is the
// corpus-level consequence, over every block type.
//
// Goes red when: Sanitise stops stripping CSI/OSC, or stops being applied to
// Problem.Cause or to message text.
var assertESCContainment = globalAssertion{
	name: "esc_containment",
	spec: "§6.7",
	what: "the corpus at ColourNone yields zero ESC bytes; a Cause containing control sequences renders with none",
	check: func(r *results) {
		for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
			for _, w := range []int{MinWidth, 80, sweepMaxWidth} {
				for _, fx := range fxCorpus() {
					s := NewStreamAt(io.Discard, w, false, ColourNone, mode == GlyphASCII)
					out := fx.render(s)
					if n := escCount(out); n != 0 {
						r.fail("esc_containment", "%s @ %d (mode %d): %d ESC bytes at ColourNone",
							fx.name, w, mode, n)
					}
					// Stripping the sequence is not enough on its own: an
					// implementation that dropped the ESC byte and kept the
					// rest would satisfy the count above while handing the
					// operator the payload as prose.
					if strings.Contains(out, "pwned") {
						r.fail("esc_containment", "%s @ %d (mode %d): the OSC title-rewrite payload survived as text",
							fx.name, w, mode)
					}
				}
			}
		}

		// The fixture must be potent, or part (b) proves nothing.
		if got := escCount(fxEscCause); got < 4 {
			r.fail("esc_containment", "the §6.7 fixture carries only %d ESC bytes; it cannot demonstrate containment", got)
		}
		if ansi.StringWidth(fxEscCause) >= len([]rune(fxEscCause)) {
			r.fail("esc_containment", "the §6.7 fixture's escapes are not zero-width to ansi.StringWidth; the sweep would already see them")
		}

		// fxEsc is what makes the payload clause above mean anything: if it
		// stopped carrying escapes, or stopped carrying the payload, every
		// block fixture built on it would go quietly inert.
		if n := escCount(fxEsc("x")); n < 2 {
			r.fail("esc_containment", "fxEsc produces only %d ESC bytes; the block surfaces built on it cannot demonstrate containment", n)
		}
		if !strings.Contains(fxEsc("x"), "pwned") {
			r.fail("esc_containment", "fxEsc no longer carries a payload; the leak clause in part (a) cannot fail")
		}

		// The ESC fixture must reach the TABLE surfaces, or D-13's sanitising
		// calls on Cell.display, Col.title() and captionText have no detector.
		// Part (a) above already asserts zero ESC bytes out; this asserts the
		// INPUT still carries them, which is what makes part (a) mean
		// something here.
		//
		// The raw fields are read directly and NOT through fxCellSource: that
		// helper sanitises, as Cell.display does, so routing this count
		// through it would count the escapes the harness has just stripped and
		// the clause would fire on a perfectly potent fixture.
		//
		// The MEMBERSHIP of each escape-bearing fixture is asserted too. Every
		// clause above is a loop over the corpus that skips what it is not
		// interested in, so a fixture quietly dropped or renamed takes its own
		// detector with it and every one of those loops stays green over the
		// remaining fixtures. That is the assertion-that-cannot-fail shape, one
		// level up from the assertions themselves.
		want := map[string]bool{
			"table/esc-in-cell":    false,
			"problem/esc-cause":    false,
			"problem/esc-surfaces": false,
			"empty/esc-surfaces":   false,
			"kv/esc-surfaces":      false,
			"checks/esc-surfaces":  false,
			"message/info-esc":     false,
		}
		for _, fx := range fxCorpus() {
			if _, ok := want[fx.name]; ok {
				want[fx.name] = true
			}
			if fx.name != "table/esc-in-cell" {
				continue
			}
			raw := fx.caption
			for _, row := range fx.rows {
				for _, c := range row {
					raw += c.Text
				}
			}
			for _, c := range fx.cols {
				raw += c.Title
			}
			if n := escCount(raw); n < 4 {
				r.fail("esc_containment", "table/esc-in-cell carries only %d ESC bytes across its cells, "+
					"titles and caption; it cannot demonstrate containment on those surfaces", n)
			}
		}
		for name, seen := range want {
			if !seen {
				r.fail("esc_containment", "%s is not in the corpus; the D-13 surfaces it is the only detector for have none", name)
			}
		}

		problem := Problem{
			Title: "Could not pull the base image",
			Cause: fxEscCause,
			Facts: []Fact{{"image", fxBaseImage}},
			Steps: []Remedy{{"Retry", "ws profile rebuild default"}},
		}
		for _, w := range []int{MinWidth, 80, sweepMaxWidth} {
			// The TTY path at ColourNone: literally zero ESC bytes.
			plain := problem.Render(NewStreamAt(io.Discard, w, true, ColourNone, false))
			if n := escCount(plain); n != 0 {
				r.fail("esc_containment", "Problem with an ESC-laden Cause emitted %d ESC bytes on the TTY path @ %d", n, w)
			}
			// The TTY path WITH colour: the layer's own SGR is legitimate, a
			// forwarded cursor move or OSC is not.
			coloured := problem.Render(NewStreamAt(io.Discard, w, true, ColourTrue, false))
			if seqs := nonSGRSequences(coloured); len(seqs) > 0 {
				r.fail("esc_containment", "Problem forwarded %v from its Cause on the coloured TTY path @ %d", seqs, w)
			}
			if strings.Contains(plain, "OWNED") || strings.Contains(plain, "registry.example.invalid") {
				r.fail("esc_containment", "Problem @ %d leaked an escape payload (title rewrite or hyperlink target) as text", w)
			}
			if !strings.Contains(ansi.Strip(plain), "unable to pull image") {
				r.fail("esc_containment", "Problem @ %d dropped the human-readable part of the Cause", w)
			}
		}

		for _, w := range []int{MinWidth, 80, sweepMaxWidth} {
			msg := renderMessage(NewStreamAt(io.Discard, w, true, ColourNone, false), shapeWarn, fxEscCause)
			if n := escCount(msg); n != 0 {
				r.fail("esc_containment", "Warn with ESC-laden text emitted %d ESC bytes @ %d", n, w)
			}
		}
	},
}

// -------------------------------------------------------- §6.4 glyph width

// assertGlyphWidths is the FIRST half of §6.4: every mark the layer emits is
// one cell wide, under the convention this process is running with. The second
// half — the whole corpus under RUNEWIDTH_EASTASIAN=1 — needs a subprocess and
// is Task 12.
//
// Goes red when: a mark in §4.5's table is replaced by one of the retired
// carriers (`●`, `○`, `·`, `→` are Ambiguous; `⚡` is Wide), or when the ASCII
// counterpart of a mark stops being ASCII, or when the ASCII marker stops
// being three cells — the number §4.3 step 5(b)'s floor depends on.
var assertGlyphWidths = globalAssertion{
	name: "glyph_widths",
	spec: "§6.4",
	what: "every state mark is one cell wide in both glyph modes; the ASCII marker is three cells and markerWidth says so",
	check: func(r *results) {
		for _, st := range allStates {
			for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
				got := stateMark(st, mode)
				want := fxMark(st, mode)
				if got != want {
					r.fail("glyph_widths", "state %d mode %d renders %q, §4.5 fixes it as %q", st, mode, got, want)
				}
				if w := ansi.StringWidth(got); w != 1 {
					r.fail("glyph_widths", "state %d mode %d mark %q is %d cells, §4.5 requires 1", st, mode, got, w)
				}
			}
			if word := stateWord(st); word != fxStateVocabulary[st].word {
				r.fail("glyph_widths", "state %d word is %q, §4.5 fixes it as %q", st, word, fxStateVocabulary[st].word)
			}
		}
		if w := ansi.StringWidth(marker(GlyphASCII)); w != 3 {
			r.fail("glyph_widths", "the ASCII truncation marker measures %d cells, expected 3", w)
		}
		if got := markerWidth(GlyphASCII); got != 3 {
			r.fail("glyph_widths", "markerWidth(ASCII) is %d; step 5(b)'s floor would not hold the marker", got)
		}
		if got := markerWidth(GlyphUTF8); got != 1 {
			r.fail("glyph_widths", "markerWidth(UTF8) is %d, expected 1 under the narrow convention", got)
		}
	},
}

// ------------------------------------------- §4.3 construction-time refusal

// assertColValidation covers §4.3's last clause: a Col set whose forced chrome
// plus its un-droppable Min widths cannot fit MinWidth is rejected at
// CONSTRUCTION rather than rendered, so the zero-column case is reachable only
// through a programming error.
//
// The boundary is asserted from BOTH sides, one cell apart, which is what
// makes it fail when the chrome formula drifts by a cell.
var assertColValidation = globalAssertion{
	name: "col_validation",
	spec: "§4.3 / §6.2",
	what: "an over-constrained Col set is refused at construction; the set one cell inside the boundary is accepted",
	check: func(r *results) {
		// Two un-droppable columns cost 3*2+1 = 7 cells of chrome, so Min
		// widths of 11 and 11 need 29 — exactly MinWidth — and 11 and 12 need
		// 30, one cell too many.
		ok := []Col{{Title: "A", Prio: 1, Min: 11}, {Title: "B", Prio: 1, Min: 11}}
		bad := []Col{{Title: "A", Prio: 1, Min: 11}, {Title: "B", Prio: 1, Min: 12}}
		if _, err := NewTableBlock(ok, nil); err != nil {
			r.fail("col_validation", "a Col set needing exactly MinWidth was rejected: %v", err)
		}
		if _, err := NewTableBlock(bad, nil); err == nil {
			r.fail("col_validation", "a Col set needing MinWidth+1 was accepted; §4.3 requires refusal")
		}
		if _, err := NewTableBlock([]Col{{Title: "A", Prio: 1, Min: 0}}, nil); err == nil {
			r.fail("col_validation", "a column with Min 0 was accepted")
		}
	},
}

func globalAssertions() []globalAssertion {
	// assertAntiDrift joins this list in Task 12.
	return []globalAssertion{assertESCContainment, assertGlyphWidths, assertColValidation}
}

// ============================================================ the tests

// TestAcceptanceSweep is §6.1 and §6.2: every block type, every degenerate
// variant and every message helper, at every width from MinWidth to
// sweepMaxWidth, in both glyph modes, checked by every sweep assertion.
func TestAcceptanceSweep(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	r := newResults()
	allocStats = allocPolicyStats{}
	st := runSweep(MinWidth, sweepMaxWidth, sweepAssertions(), r)
	r.report(t)
	t.Logf("swept %d renders / %d lines over widths %d..%d x 2 glyph modes x %d fixtures",
		st.renders, st.lines, MinWidth, sweepMaxWidth, len(fxCorpus()))
	for _, a := range sweepAssertions() {
		t.Logf("  asserted %-20s %-22s %s", a.name, a.spec, a.what)
	}

	// What §4.3's invariants actually had in front of them. An allocator
	// invariant over a corpus that never drops, never relaxes and never
	// squeezes an Atomic column is green against ANY allocator at all, so the
	// counts are asserted rather than merely logged.
	t.Logf("  allocations %d: %d dropped a column, %d relaxed one below its Min, %d squeezed an Atomic column, %d ended at n = 0",
		allocStats.allocations, allocStats.dropped, allocStats.relaxed, allocStats.squeezed, allocStats.captionOnly)
	if allocStats.dropped == 0 || allocStats.relaxed == 0 || allocStats.squeezed == 0 || allocStats.captionOnly == 0 {
		t.Errorf("the corpus does not reach every branch the §4.3 invariants police: %+v", allocStats)
	}
}

// TestAcceptanceGlobals runs the assertions that need their own renders.
func TestAcceptanceGlobals(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	for _, a := range globalAssertions() {
		t.Run(a.name, func(t *testing.T) {
			r := newResults()
			a.check(r)
			r.report(t)
			t.Logf("%s %s: %s", a.name, a.spec, a.what)
		})
	}
}

// TestControlBudget28 is the control §6.1 demands: the same corpus re-checked
// against a budget of 28 must report a NON-ZERO number of overflowing lines
// while reporting zero at 29.
//
// §4.2 clamps a sub-MinWidth width up to MinWidth, so this is not "render at
// 28" — nothing can be rendered at 28. It is the corpus rendered at the floor
// and measured against a budget one cell below it, which makes the number the
// size of the clamp's visible effect rather than a bug count.
//
// The number is PINNED. It moves when the corpus changes, when the clamp is
// removed, or when any block's geometry at the floor changes — all of which a
// reviewer must be told about rather than have absorbed silently. Record in
// this comment what moved it and by how much, every time.
//
// Measured over 49 fixtures, 18 of them tables: 455 overflowing lines of 1171.
// It moved twice while this corpus was being built, and each move is one table
// fixture's worth of geometry at the floor:
//
//	43 fixtures, 16 tables                431 of 1109
//	+ table/tab-in-cell and the fix       443 of 1123
//	+ table/esc-in-cell                   455 of 1137
//	+ the four block escape fixtures      455 of 1171 (they add 34 lines at
//	                                      the floor and overflow none of them)
//
// The sweep's own line count moved the other way across the tab fix, 111464 to
// 111120, because expanded tabs are wider than the zero cells the layer used
// to measure them at and the wraps land differently.
const control28Overflows = 455

func TestControlBudget28(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	over, total := 0, 0
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for _, fx := range fxCorpus() {
			rc := newRenderCase(fx, MinWidth-1, mode)
			for _, line := range rc.plain {
				total++
				if ansi.StringWidth(line) > MinWidth-1 {
					over++
				}
			}
		}
	}
	if over == 0 {
		t.Fatalf("the corpus produced 0 lines wider than %d: the width assertion cannot discriminate", MinWidth-1)
	}
	if over != control28Overflows {
		t.Errorf("budget %d: %d overflowing lines of %d, pinned at %d",
			MinWidth-1, over, total, control28Overflows)
	}
	t.Logf("budget %d: %d of %d lines overflow (budget %d: 0)", MinWidth-1, over, total, MinWidth)
}

// TestDetectorsCanFail is the control for the two assertions that no runtime
// mutation switch in this package can redden — §6.3's UTF-8 check and §6.7's
// ESC containment. Neither defect class is expressible as a switch in the
// shipped package: every cut in text.go lands on a whole grapheme cluster, and
// Sanitise has no off switch.
//
// What can still be proved, and is proved here, is that the DETECTORS those
// assertions are built on discriminate — that they report the defect when the
// defect is put in front of them, rather than being satisfied by anything at
// all.
func TestDetectorsCanFail(t *testing.T) {
	// §6.3's detector: a cut taken in bytes rather than clusters.
	cjk := "中文工作区"
	if utf8.ValidString(cjk[:2]) {
		t.Error("utf8.ValidString accepted a mid-rune byte slice; §6.3's assertion could not fail")
	}
	if !utf8.ValidString(cjk) {
		t.Error("utf8.ValidString rejected valid text; §6.3's assertion would always fail")
	}

	// §6.7's detectors: ESC counting and the non-SGR sequence scanner.
	if got := escCount(fxEscCause); got == 0 {
		t.Error("escCount found no ESC in the §6.7 fixture")
	}
	if seqs := nonSGRSequences(fxEscCause); len(seqs) == 0 {
		t.Error("nonSGRSequences found nothing in a fixture carrying cursor moves, a line clear and two OSCs")
	} else {
		t.Logf("the §6.7 fixture carries %d ESC bytes and %v", escCount(fxEscCause), seqs)
	}
	// Pure SGR — what the layer itself legitimately emits — must NOT be
	// reported, or the assertion would fire on every coloured render.
	if seqs := nonSGRSequences("\x1b[38;2;184;187;38mok\x1b[0m"); len(seqs) != 0 {
		t.Errorf("nonSGRSequences reported %v for plain SGR; §6.7 would fail on any coloured render", seqs)
	}
	if escCount("no escapes here") != 0 {
		t.Error("escCount reported escapes in plain text")
	}
}

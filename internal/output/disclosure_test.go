package output

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// §4.3's disclosure clause: the caption must say what the allocator DID.
// "The renderer appends the dropped columns to the caption … generated from
// what it actually dropped, so the clause cannot go stale." A table that drops
// a column silently is inside its budget and passes every width check ever
// written.
//
// `squash` is declared in message_test.go — do not re-declare it here.

// captionRegion is the part of a table render below the grid — or the whole
// render when every column was dropped and §4.3's n = 0 case left the caption
// standing alone.
func captionRegion(rc renderCase, a Alloc) string {
	if !rc.fx.isTable {
		return ""
	}
	if len(a.Kept) == 0 {
		return strings.Join(rc.plain, "\n")
	}
	grid := 4 + len(rc.fx.rows)
	if grid >= len(rc.plain) {
		return ""
	}
	return strings.Join(rc.plain[grid:], "\n")
}

// assertCaptionDiscloses goes red when the "Hidden: …" / "Narrowed: …" clause is
// suppressed, when it is generated from something other than what the allocator
// actually did, when a declared WideFlag stops being named beside the columns
// it would bring back — or when a flag is named that the command does not have.
var assertCaptionDiscloses = sweepAssertion{
	name: "caption_discloses",
	spec: "§4.3",
	what: "every dropped and every relaxed column is named in the caption, with the WideFlag where one is declared and none where it is not",
	check: func(rc renderCase, r *results) {
		if !rc.fx.isTable {
			return
		}
		a := Allocate(rc.fx.cols, rc.fx.rows, rc.budget, rc.mode)
		region := captionRegion(rc, a)
		caption := squash(region)

		if len(a.Dropped) == 0 && len(a.Relaxed) == 0 {
			// Nothing was lost, so nothing may be claimed. This half also catches a
			// clause bolted on unconditionally.
			if strings.Contains(caption, "Hidden:") || strings.Contains(caption, "Narrowed:") {
				r.fail("caption_discloses", "%s @ %d (mode %d): nothing was dropped or relaxed but the caption claims otherwise: %q",
					rc.fx.name, rc.width, rc.mode, region)
			}
			return
		}
		for _, title := range a.Dropped {
			if !strings.Contains(caption, squash(title)) {
				r.fail("caption_discloses", "%s @ %d (mode %d): %q was dropped and the caption does not name it: %q",
					rc.fx.name, rc.width, rc.mode, title, region)
			}
		}
		for _, title := range a.Relaxed {
			if !strings.Contains(caption, squash(title)) {
				r.fail("caption_discloses", "%s @ %d (mode %d): %q was taken below its Min and the caption does not name it: %q",
					rc.fx.name, rc.width, rc.mode, title, region)
			}
		}

		// The two lists are DISJOINT, and without this nothing says so.
		// Step 5(b) appends every column it narrowed to Relaxed; step 5(c) may
		// then drop some of those same columns, and nothing removed them from
		// Relaxed, so the caption told the operator a column was both narrowed
		// and hidden. The fix belongs in the allocator; this is its detector.
		//
		// It is a REGRESSION guard and not a live detector: measured on this
		// corpus, 8 of the 2,752 renders carry both a Dropped and a Relaxed
		// list, and none of them overlaps — under the clean allocator and
		// under each of the six §4.3 switches taken one at a time. So the loop
		// runs and finds nothing today. Nothing in this task can make it fire,
		// which is exactly why it is written down rather than assumed.
		dropped := make(map[string]bool, len(a.Dropped))
		for _, title := range a.Dropped {
			dropped[title] = true
		}
		for _, title := range a.Relaxed {
			if dropped[title] {
				r.fail("caption_discloses", "%s @ %d (mode %d): %q is named as both Narrowed and Hidden; "+
					"a column that step 5(c) dropped must not stay in Relaxed: %q",
					rc.fx.name, rc.width, rc.mode, title, region)
			}
		}

		// §4.4's WideFlag contract, BOTH ways. Where the command declares a flag,
		// the clause must carry it — or the caption names columns the operator
		// has no way to bring back. Where it declares none, the clause must not
		// invent one.
		switch {
		case len(a.Dropped) == 0:
		case rc.fx.wideFlag != "":
			if !strings.Contains(caption, squash(rc.fx.wideFlag)) {
				r.fail("caption_discloses", "%s @ %d (mode %d): columns %v were dropped but the caption does not name %q: %q",
					rc.fx.name, rc.width, rc.mode, a.Dropped, rc.fx.wideFlag, region)
			}
		default:
			if strings.Contains(caption, "(--") {
				r.fail("caption_discloses", "%s @ %d (mode %d): the table declares no WideFlag but the caption advertises one: %q",
					rc.fx.name, rc.width, rc.mode, region)
			}
		}
	},
}

// TestCaptionDiscloses sweeps the disclosure clause over every table fixture at
// every width, and refuses to report success over a corpus that never drops
// anything, never relaxes anything, or never withholds a WideFlag from a table
// the constructor accepts.
//
// The third guard is deliberately narrower than "some render dropped a column
// with no flag declared". Three degenerate Col literals in the corpus already
// declare no flag, so the loose form was satisfied before this task's
// table/no-wide-flag fixture existed; what it was not satisfied by is a VALID,
// constructor-built table in the same position. validateCols is what separates
// the two, and it is the same predicate tableFixtures() builds them with.
func TestCaptionDiscloses(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	r := newResults()
	dropped, relaxed, droppedNoFlag, droppedNoFlagValid := 0, 0, 0, 0
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for w := MinWidth; w <= 200; w++ {
			for _, fx := range tableFixtures() {
				rc := newRenderCase(fx, w, mode)
				a := Allocate(fx.cols, fx.rows, rc.budget, mode)
				if len(a.Dropped) > 0 {
					dropped++
					if fx.wideFlag == "" {
						droppedNoFlag++
						if validateCols(fx.cols) == nil {
							droppedNoFlagValid++
						}
					}
				}
				if len(a.Relaxed) > 0 {
					relaxed++
				}
				assertCaptionDiscloses.check(rc, r)
			}
		}
	}
	r.report(t)
	t.Logf("%-56s %d", "renders that dropped a column with NO WideFlag", droppedNoFlag)
	for _, c := range []struct {
		name string
		n    int
	}{
		{"renders that dropped a column", dropped},
		{"renders that relaxed a column", relaxed},
		{"drop-renders, no WideFlag, constructor-built", droppedNoFlagValid},
	} {
		if c.n == 0 {
			t.Errorf("no render in the corpus produced %q: the half of caption_discloses that guards it cannot fail", c.name)
		}
		t.Logf("%-56s %d", c.name, c.n)
	}
}

// TestTableMutantsRedenTheBlockChecks is the control for both assertions above.
// Every switch the table block adds to mutants.go is planted here, one at a
// time, and the assertion that catches each is named.
//
// style_painted_before_fit is planted here as well as in
// TestStyleIsAppliedAfterAllocation below, and it is NOT vacuous on this
// corpus. The sweep renders at ColourTrue — see sweepStream — so a painted
// cell that has to be CUT comes back short of the width the allocator paid
// for. The mechanism, measured rather than inferred: cutAt steps with
// ansi.FirstGraphemeCluster, which returns the ESC byte itself at width 0 and
// then hands back every byte of the parameter string as an ordinary one-cell
// cluster. On a painted "degraded", clipTail(plain, 5) is "degr…" at 5 cells
// and clipTail(painted, 5) is "\x1b[38;…" at 1.
//
// Measured with the switch planted over the sweep TestTableGridPairing covers
// — 2,752 renders, widths 29..200 over the eight table fixtures in both glyph
// modes, and NOT the nine-width sample this test runs: 130 of the 2,752 go
// red, 70 through the abbreviation-width check and 60 through gridFields'
// bordered-row check, the first being
//
//	table/list @ 29 (mode 0): "STATUS" allocated 9 cells but rendered 1 ("…")
//
// Where nothing is cut, the two orders differ only in where the escapes sit,
// and every assertion here measures ansi.Strip'd text — so the corpus is blind
// to the ordering by construction. That is the half the focused test covers.
//
// lipgloss_width_pinning is expected to be VACUOUS ON ITS OWN — with the
// allocator's arithmetic right, re-fitting the grid to the width it already has
// changes nothing, and that is precisely why the call is dangerous: it is
// invisible until it is hiding another defect. It is therefore planted a second
// time COMBINED with a chrome defect, where it must be killed. A harness that
// reported the lone switch as "killed" would be lying.
func TestTableMutantsRedenTheBlockChecks(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	t.Cleanup(func() { mutants = mutantSwitches{} })

	// THE FIXTURE CORPUS IS BUILT ONCE, WITH THE SWITCHES CLEAN, AND OUTSIDE
	// THE MUTANT LOOP. tableFixtures() calls NewTableBlock -> validateCols, and
	// validateCols computes its chrome with chromeFor(n) = 3n+1-mutants.ChromeOff.
	// A fixture set whose validity depends on a mutated constant is not a
	// fixture set. Measured: at ChromeOff = 1 validateCols returns nil for the
	// caption-only Col set that it refuses at ChromeOff = 0 — the chrome it
	// charges drops from 4 to 3 and the Min sum of 26 then fits MinWidth — so
	// a corpus rebuilt inside the loop would trip lit()'s "declared degenerate
	// but the constructor accepts it" panic and take the whole package test
	// binary down with it. Built here, the corpus is the same corpus for the
	// clean baseline and for every mutant, which is the only way the
	// byte-comparison below means anything.
	fxs := tableFixtures()
	run := func() (*results, string) {
		r := newResults()
		var b strings.Builder
		for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
			for _, w := range []int{MinWidth, 30, 34, 40, 46, 60, 80, 120, 200} {
				for _, fx := range fxs {
					rc := newRenderCase(fx, w, mode)
					b.WriteString(rc.out)
					b.WriteByte(0)
					// The width-budget loop used to live inside
					// assertGridPairing. caption_wrapped_too_wide is killed by
					// it and by nothing else, so this harness has to run the
					// half that moved: splitting an assertion is never a local
					// edit.
					assertWidthBudget.check(rc, r)
					assertGridPairing.check(rc, r)
					assertCaptionDiscloses.check(rc, r)
				}
			}
		}
		return r, b.String()
	}

	mutants = mutantSwitches{}
	cleanR, cleanOut := run()
	if cleanR.any() {
		t.Fatalf("the clean corpus is not green: %v", cleanR.redAssertions())
	}

	planted := []struct {
		name    string
		defect  string
		apply   func(*mutantSwitches)
		vacuous bool
	}{
		{"caption_hides_what_it_dropped", "the Hidden:/Narrowed: clause suppressed",
			func(m *mutantSwitches) { m.NoCaptionDisclosure = true }, false},
		{"hardcoded_wide_flag", `" (--wide)" in place of the WideFlag guard`,
			func(m *mutantSwitches) { m.HardcodedWideFlag = true }, false},
		{"caption_wrapped_too_wide", "the caption wrapped two cells past the budget",
			func(m *mutantSwitches) { m.CaptionWidth = 2 }, false},
		{"style_painted_before_fit", "the cell painted before it is truncated and padded",
			func(m *mutantSwitches) { m.PaintBeforeFit = true }, false},
		{"lipgloss_width_pinning", "the allocator's total handed to lipgloss as .Width()",
			func(m *mutantSwitches) { m.PinTableWidth = true }, true},
		{"chrome_off_by_one+lipgloss_width_pinning", "a chrome defect the pinned width absorbs",
			func(m *mutantSwitches) { m.ChromeOff = 1; m.PinTableWidth = true }, false},
		{"chrome_over_by_one+lipgloss_width_pinning", "a chrome over-count the sweep cannot see at all",
			func(m *mutantSwitches) { m.ChromeOff = -1; m.PinTableWidth = true }, false},
	}
	for _, p := range planted {
		mutants = mutantSwitches{}
		p.apply(&mutants)
		r, out := run()
		mutants = mutantSwitches{}

		perturbed := out != cleanOut
		switch {
		case p.vacuous:
			if perturbed {
				t.Errorf("%s was declared VACUOUS but changed the rendered bytes; re-measure before trusting the declaration", p.name)
			}
			if r.any() {
				t.Errorf("%s was declared VACUOUS but reddened %v, which means it is not vacuous", p.name, r.redAssertions())
			}
			t.Logf("%-42s %-52s VACUOUS (re-measured: byte-identical to the clean corpus)", p.name, p.defect)
		case !perturbed:
			t.Errorf("VACUOUS MUTANT %s (%s): the rendered bytes did not change, so nothing could have caught it", p.name, p.defect)
		case !r.any():
			t.Errorf("SURVIVING MUTANT %s (%s): the output changed and every assertion stayed green", p.name, p.defect)
		default:
			t.Logf("%-42s %-52s red: %v", p.name, p.defect, r.redAssertions())
		}
	}
}

// TestStyleIsAppliedAfterAllocation is §4.3's ordering clause, asserted.
//
// Read at ColourNone it is unobservable, so this renders at a real colour
// level and looks at where the SGR sequences fall relative to the PADDING.
// Paint-after-fit wraps the padded cell, so the reset comes after the trailing
// spaces; paint-before-fit wraps the bare text and the padding is added
// outside it, so the reset comes before them.
//
// The fixture is deliberately one that does NOT truncate. §4.3 never stretches
// a column past its natural width, so at width 40 this one column is 4 cells
// wide — the width of its own heading — and "api" carries exactly one cell of
// trailing padding. Measured, the two renders differ only in where that one
// space falls:
//
//	clean   "│ \x1b[38;5;142mapi \x1b[0m │"
//	mutated "│ \x1b[38;5;142mapi\x1b[0m  │"
//
// so the pattern is "one or more spaces before the reset", not "two or more".
// The other harm — a painted string cut to a width that counted its own escape
// bytes as cells, which loses content and leaves the SGR unterminated — shows
// up in the corpus rather than here, and is what kills style_painted_before_fit
// in TestTableMutantsRedenTheBlockChecks above.
func TestStyleIsAppliedAfterAllocation(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	t.Cleanup(func() { mutants = mutantSwitches{} })

	cols := []Col{{Title: "NAME", Prio: 1, Min: 8, Trunc: TruncTail}}
	rows := [][]Cell{{Cell{Text: "api", Role: RoleOK}}}
	tbl, err := NewTableBlock(cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	render := func() string {
		var b strings.Builder
		s := NewStreamAt(&b, 40, true, Colour256, false)
		return tbl.Render(s)
	}

	clean := render()
	if !strings.Contains(clean, "\x1b[") {
		t.Fatal("the fixture renders no SGR at Colour256; this test cannot discriminate")
	}
	// The painted run must END after the cell's trailing padding, not before
	// it: "api" is 3 cells in a column the allocator sized at 4.
	paintedThroughPadding := regexp.MustCompile(`api +\x1b\[0?m`)
	if !paintedThroughPadding.MatchString(clean) {
		t.Errorf("the reset falls before the cell's padding; style was applied before the fit:\n%s", clean)
	}

	mutants.PaintBeforeFit = true
	mutated := render()
	mutants = mutantSwitches{}
	if mutated == clean {
		t.Fatal("VACUOUS: paint-before-fit produced a byte-identical render; the assertion below proves nothing")
	}
	if paintedThroughPadding.MatchString(mutated) {
		t.Error("the assertion does not discriminate: it accepts the paint-before-fit render too")
	}
	t.Logf("clean   %q", clean)
	t.Logf("mutated %q", mutated)
}

// ----------------------------------------------------- §4.4 content fidelity

// assertContentFidelity is §4.4's wrapping contract, asserted OUTSIDE the grid:
// a wrap may insert whitespace but never delete characters.
//
// Why it is not optional, measured on the reference implementation: a message
// helper that truncated instead of wrapping passed the entire suite before this
// assertion existed — across all 137 call sites, with §6.1's width sweep green,
// because a truncated line is by construction no wider than the budget.
//
// The comparison goes through squash (message_test.go), which removes every
// space, tab and newline: a wrapped line break, a hanging indent and a hard
// break at the budget are all whitespace the renderer is entitled to introduce.
//
// A fixture whose text is deliberately altered in flight — §6.7's ESC-laden
// Cause, which Sanitise strips — declares the SURVIVORS as its fidelity list
// rather than its raw source, because the raw source is not what §4.4 promises
// to keep.
//
// Goes red when: a block truncates where §4.4 says it wraps, when a hanging
// indent eats a character instead of a space, or when a value is dropped
// because it did not fit its aligned slot.
var assertContentFidelity = sweepAssertion{
	name: "content_fidelity",
	spec: "§4.4",
	what: "outside the grid, every wrapped source string survives the render whole once line breaks and indents are collapsed",
	check: func(rc renderCase, r *results) {
		if len(rc.fx.fidelity) == 0 {
			return
		}
		rendered := squash(strings.Join(rc.plain, "\n"))
		for _, src := range rc.fx.fidelity {
			want := squash(src)
			if want == "" {
				continue
			}
			if !strings.Contains(rendered, want) {
				r.fail("content_fidelity", "%s @ %d (mode %d): %d-cell source %q is not in the render whole:\n%s",
					rc.fx.name, rc.width, rc.mode, ansi.StringWidth(src), src, strings.Join(rc.plain, "\n"))
			}
		}
	},
}

// TestDisclosureAssertionsArePotent refuses to let either assertion above pass
// over a corpus that never puts it to work: a disclosure check is trivially
// green on a corpus where nothing is ever dropped, and a fidelity check is
// trivially green on one where nothing is ever wrapped.
func TestDisclosureAssertionsArePotent(t *testing.T) {
	if mutants != (mutantSwitches{}) {
		t.Fatalf("mutation switches not clean on entry: %+v", mutants)
	}
	disclosing, wrapping, sources := 0, 0, 0
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for _, w := range []int{MinWidth, 40, 80, 120, sweepMaxWidth} {
			for _, fx := range fxCorpus() {
				rc := newRenderCase(fx, w, mode)
				if fx.isTable {
					a := Allocate(fx.cols, fx.rows, rc.budget, mode)
					if len(a.Dropped)+len(a.Relaxed) > 0 {
						disclosing++
					}
					continue
				}
				sources += len(fx.fidelity)
				for _, src := range fx.fidelity {
					// A source that fits on one line at this width proves
					// nothing about wrapping. Measured in display CELLS, as
					// everywhere in this suite — a byte count would call every
					// CJK fixture wide and every emoji one wider still.
					if len(rc.plain) > 1 && ansi.StringWidth(src) > rc.budget {
						wrapping++
					}
				}
			}
		}
	}
	if disclosing == 0 {
		t.Error("no render in the corpus dropped or relaxed a column: caption_discloses cannot fail")
	}
	if wrapping == 0 {
		t.Error("no declared source is wider than its budget in any render: content_fidelity cannot fail")
	}
	t.Logf("%d renders disclose a dropped or relaxed column; %d of %d declared sources are wider than the budget they are wrapped into",
		disclosing, wrapping, sources)
}

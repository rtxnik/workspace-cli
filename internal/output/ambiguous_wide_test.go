package output

// `sort` and `lipgloss` are both used by fxBorderGlyphs below — it takes a
// lipgloss.Border and returns the deduplicated glyph set in a stable order.
// Leaving either out gives `undefined: lipgloss` and `undefined: sort`.
//
// There is no `io` here: the child renders through sweepStream, which owns the
// io.Discard writer and the colour level the corpus sweeps at.
import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// §6.4, second half: the whole corpus swept again under the East-Asian
// Ambiguous-WIDE convention.
//
// It has to be a subprocess. x/ansi reads RUNEWIDTH_EASTASIAN in init()
// (ansi@v0.11.6 method.go:20-25 sets EastAsianWidth on its two width
// conditions there), so the convention cannot be toggled inside a running
// test — a test that set the variable and carried on would measure the narrow
// convention and report success for a terminal it never simulated.
//
// What §6.4 requires is a choice, and the design made it: either the UTF-8
// corpus passes under both conventions, or the ASCII glyph mode is selected on
// such a locale. It does not pass — the truncation marker and all eleven
// rounded-border glyphs are Ambiguous, and they are the bulk of what a table
// emits — so the assertion is that the SELECTION happens and that the selected
// mode is clean. The forced-UTF-8 run is the control that proves the clean
// result is not vacuous.
//
// Measured on this tree, over fxCorpus()'s 49 fixtures at widths 29..200:
//
//	Ambiguous-wide, mode selected by §4.5: ascii, 0 of 58852 lines overflow
//	Ambiguous-wide, glyph mode forced to utf8: 14550 of 58852 lines overflow
//
// Only the SHAPE of that pair is asserted — zero for the selected mode, some
// non-zero number for the forced control — because both counts move with the
// corpus. The figures are recorded so that a reader who re-runs this and gets
// a different pair knows whether the corpus changed or the layer did.
//
// Each claim this file makes was planted rather than argued. Running the child
// with RUNEWIDTH_EASTASIAN unset trips the U+2026 guard ("U+2026 measures 1
// cells, expected 2"). Deleting the RUNEWIDTH_EASTASIAN clause from
// glyphModeFromEnv makes the parent report mode "utf8" and 14550 overflows in
// the SELECTED run — the same 14550 the forced control reports, which is the
// two halves cross-checking each other. Emptying two fields of
// lipgloss.RoundedBorder() makes the census report 9 distinct glyphs. Putting
// an Ambiguous glyph into asciiBorder makes it report 4 ASCII glyphs and
// "┼ is 2 cells".

const (
	ambiWideEnv    = "WS_OUTPUT_AMBIWIDE"
	ambiWideResult = "AMBIWIDE-RESULT"
	ambiModeChosen = "selected"
	ambiModeForced = "forced-utf8"
)

// TestAmbiguousWideSweep is the parent: it re-executes this test binary twice
// under RUNEWIDTH_EASTASIAN=1 and reads the two children's verdicts.
func TestAmbiguousWideSweep(t *testing.T) {
	if role := os.Getenv(ambiWideEnv); role != "" {
		ambiguousWideChild(t, role)
		return
	}

	run := func(role string) (mode string, overflow, lines int) {
		cmd := exec.Command(os.Args[0], "-test.run=TestAmbiguousWideSweep", "-test.v")
		cmd.Env = append(os.Environ(), "RUNEWIDTH_EASTASIAN=1", ambiWideEnv+"="+role)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child %q failed: %v\n%s", role, err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			i := strings.Index(line, ambiWideResult)
			if i < 0 {
				continue
			}
			if _, err := fmt.Sscanf(line[i:], ambiWideResult+" mode=%s overflow=%d lines=%d",
				&mode, &overflow, &lines); err != nil {
				t.Fatalf("child %q: unparseable verdict %q: %v", role, line, err)
			}
			return mode, overflow, lines
		}
		t.Fatalf("child %q produced no verdict:\n%s", role, out)
		return "", 0, 0
	}

	mode, overflow, lines := run(ambiModeChosen)
	if mode != "ascii" {
		t.Errorf("under RUNEWIDTH_EASTASIAN=1 the layer selected glyph mode %q; §4.5 selects ASCII", mode)
	}
	if overflow != 0 {
		t.Errorf("selected glyph mode overflowed %d of %d lines under the Ambiguous-wide convention", overflow, lines)
	}
	t.Logf("Ambiguous-wide, mode selected by §4.5: %s, %d of %d lines overflow", mode, overflow, lines)

	// The control. Without it, "0 overflows" would be equally consistent with
	// the child never having had the convention applied at all.
	cmode, coverflow, clines := run(ambiModeForced)
	// The control's own mode is ASSERTED, not merely logged. If ambiModeForced
	// ever stopped matching the child's role check, the child would fall
	// through to glyphModeFromEnv, select ASCII, overflow zero — and the
	// clause below would fire with a true failure and a misleading cause,
	// blaming the convention for a broken role string.
	if cmode != "utf8" {
		t.Errorf("the control ran in glyph mode %q; it exists to FORCE utf8, and an ascii control "+
			"re-measures what the selected run already measured", cmode)
	}
	if coverflow == 0 {
		t.Errorf("forced UTF-8 glyphs overflowed 0 of %d lines under the Ambiguous-wide convention: "+
			"the subprocess is not measuring what §6.4 says it measures", clines)
	}
	t.Logf("Ambiguous-wide, glyph mode forced to %s: %d of %d lines overflow — this is what §4.5's mode selection avoids",
		cmode, coverflow, clines)
}

// ambiguousWideChild runs inside the subprocess, with RUNEWIDTH_EASTASIAN=1
// already honoured by x/ansi's init().
func ambiguousWideChild(t *testing.T, role string) {
	// Refuse to report anything unless the convention actually took effect.
	// A child that measured the narrow convention and printed "0 overflow"
	// would be the most convincing wrong answer in the suite.
	if w := ansi.StringWidth("…"); w != 2 {
		t.Fatalf("RUNEWIDTH_EASTASIAN=1 did not take effect: U+2026 measures %d cells, expected 2", w)
	}
	if w := ansi.StringWidth("─"); w != 2 {
		t.Fatalf("RUNEWIDTH_EASTASIAN=1 did not take effect: U+2500 measures %d cells, expected 2", w)
	}
	// Every state mark must still be ONE cell — §4.5's claim, and the reason
	// the six marks were chosen over the retired carriers (`● ○ · –  — →` are
	// Ambiguous; `⚡` is Wide).
	for _, st := range allStates {
		for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
			if w := ansi.StringWidth(stateMark(st, mode)); w != 1 {
				t.Errorf("under the Ambiguous-wide convention state %d mode %d is %d cells", st, mode, w)
			}
		}
	}

	// §6.4's per-glyph census, in BOTH directions. glyph_test.go's
	// TestGlyphWidths asserts the ASCII border set is one cell and one ASCII
	// byte per glyph, and says in its own comment that it declines the UTF-8
	// set because that set is Ambiguous by construction; acceptance_test.go's
	// assertGlyphWidths covers the state marks and the two markers and no
	// border at all. This is where "all eleven box-drawing glyphs is measured
	// under both Ambiguous conventions" is actually discharged.
	//
	// Measured on this tree, with border(GlyphUTF8) = lipgloss.RoundedBorder():
	// 13 non-empty fields carrying 11 DISTINCT glyphs — ─ │ ├ ┤ ┬ ┴ ┼ ╭ ╮ ╯ ╰
	// — every one of them 1 cell under the narrow convention and 2 under this
	// one. border(GlyphASCII) carries 3 distinct glyphs, `+ - |`, 1 cell under
	// both. The marker moves with the box drawing (… is 1 then 2); "..." stays
	// 3 under both.
	//
	// The count is asserted as well as the widths: a border whose fields were
	// emptied would satisfy a per-glyph loop that never ran.
	utf8Glyphs := fxUTF8BorderGlyphs()
	if len(utf8Glyphs) != 11 {
		t.Errorf("border(GlyphUTF8) carries %d distinct glyphs, §6.4 names eleven: %v", len(utf8Glyphs), utf8Glyphs)
	}
	for _, g := range utf8Glyphs {
		if w := ansi.StringWidth(g); w != 2 {
			t.Errorf("UTF-8 border glyph %q is %d cells under the Ambiguous-wide convention, expected 2; "+
				"if this is 1 the glyph is no longer Ambiguous and §4.5's mode selection needs revisiting", g, w)
		}
	}
	asciiGlyphs := fxASCIIBorderGlyphs()
	if len(asciiGlyphs) != 3 {
		t.Errorf("border(GlyphASCII) carries %d distinct glyphs, measured three (`+ - |`): %v", len(asciiGlyphs), asciiGlyphs)
	}
	for _, g := range asciiGlyphs {
		if w := ansi.StringWidth(g); w != 1 {
			t.Errorf("ASCII border glyph %q is %d cells under the Ambiguous-wide convention, expected 1; "+
				"the ASCII mode does not escape the convention and the whole design of §4.5 fails here", g, w)
		}
	}
	if w := ansi.StringWidth(marker(GlyphUTF8)); w != 2 {
		t.Errorf("the UTF-8 truncation marker is %d cells under the Ambiguous-wide convention, expected 2", w)
	}
	if w := ansi.StringWidth(marker(GlyphASCII)); w != 3 {
		t.Errorf("the ASCII truncation marker is %d cells, expected 3 under every convention", w)
	}

	mode := glyphModeFromEnv(os.Getenv)
	if role == ambiModeForced {
		mode = GlyphUTF8
	}
	name := "utf8"
	if mode == GlyphASCII {
		name = "ascii"
	}

	overflow, lines := 0, 0
	for w := MinWidth; w <= sweepMaxWidth; w++ {
		for _, fx := range fxCorpus() {
			// The fixture contract in alloc_policy_test.go allows a nil
			// render: that is how the allocator's own fixtures drive
			// assertAllocPolicy, which reads the allocator's input and
			// nothing else. Measured on this tree: all 8 of
			// allocFixtureCases() have a nil render, and 0 of fxCorpus()'s
			// 49 do — the two sets are disjoint today. The corpus is
			// appended to by later phases, and a nil reaching the call below
			// would panic rather than report.
			if fx.render == nil {
				continue
			}
			s := sweepStream(w, mode)
			for _, line := range strings.Split(fx.render(s), "\n") {
				lines++
				if ansi.StringWidth(ansi.Strip(line)) > w {
					overflow++
				}
			}
		}
	}
	fmt.Printf("%s mode=%s overflow=%d lines=%d\n", ambiWideResult, name, overflow, lines)
}

// fxUTF8BorderGlyphs and fxASCIIBorderGlyphs return the DISTINCT glyphs of a
// lipgloss.Border, deduplicated: Top and Bottom are both "─", Left and Right
// both "│", so the thirteen non-empty fields carry eleven distinct glyphs.
//
// The set is read from border(mode) rather than written out from §4.5, because
// what is being asserted here is a property of the Unicode tables — how wide
// the terminal draws whatever the layer emits — and not which glyphs the layer
// chose. glyph_test.go's TestGlyphWidths asserts the choice.
func fxUTF8BorderGlyphs() []string { return fxBorderGlyphs(border(GlyphUTF8)) }

func fxASCIIBorderGlyphs() []string { return fxBorderGlyphs(border(GlyphASCII)) }

func fxBorderGlyphs(b lipgloss.Border) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range []string{b.Top, b.Bottom, b.Left, b.Right,
		b.TopLeft, b.TopRight, b.BottomLeft, b.BottomRight,
		b.MiddleLeft, b.MiddleRight, b.Middle, b.MiddleTop, b.MiddleBottom} {
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

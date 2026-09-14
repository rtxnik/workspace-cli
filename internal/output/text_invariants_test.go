package output

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Primitive-level invariants for the cut primitives.
//
// The §6.1 width sweep is end-to-end: it can only see a cut defect that
// survives the allocator, the padding and the block geometry wrapped around
// it, and when it does go red it says a render overflowed, not which
// primitive let it. These are properties of cutAt and cutAtEnd THEMSELVES,
// stated over a corpus of hostile strings and every budget from 0 to 40, so
// they hold for inputs nobody thought to write a golden for.
//
// The measurement here is ansi.StringWidth and never the package's own W: a
// check that measured with the function under test would agree with a defect
// in it.

// cutCorpus is the hostile material. Each entry names the property that makes
// the string a distinct case for a cut that steps by rune.
var cutCorpus = []struct{ name, s string }{
	{"ascii", "golangci-lint, node, jq, yq, fzf, ripgrep"},
	{"empty", ""},
	{"emoji-presentation-single", "⚠️"},
	{"emoji-presentation-run", "⚠️✔️ℹ️⚠️✔️ℹ️⚠️✔️ℹ️⚠️✔️ℹ️"},
	{"emoji-presentation-in-prose", "⚠️ buildkit: ✔️ 3 of 5 layers cached, ℹ️ 2 rebuilt"},
	{"zwj-family", "👨‍👩‍👧‍👦 shared workspace 👩‍💻"},
	// Written as escapes on purpose: the decomposed forms are the point of
	// the fixture, and a copy through an editor that normalises to NFC would
	// silently turn three multi-rune clusters into single runes.
	{"combining-marks", "e\u0323\u0301cole cafe\u0301 nai\u0308ve"},
	{"cjk", "中文工作区，代理配置文件无法访问"},
	{"cjk-mixed-ascii", "ws list 中文工作区 api 日本語環境 web"},
	// `cafe\u0301` is DECOMPOSED, like `combining-marks` above and for the same
	// reason: a precomposed U+00E9 here is one cluster fewer and one boundary
	// violation fewer, and moves the two counts the logs below print from
	// 57/88 to 56/87.
	{"mixed-run", "api ⚠️ 中文 👨‍👩‍👧‍👦 cafe\u0301 [2001:db8::1]:443 ✔️"},
	{"keycap", "1️⃣ 2️⃣ 3️⃣"},
	{"flag-pair", "🇩🇪 de-fra-01 🇳🇱 nl-ams-02"},
	{"skin-tone", "👍🏽 ok 👍🏿 ok"},
	{"vs15-text-presentation", "⚠︎ text-presentation warning"},
	{"wide-only", strings.Repeat("中", 24)},
	{"emoji-only-long", strings.Repeat("⚠️", 24)},
}

const cutMaxBudget = 40

// ------------------------------------------------------- the planted control
//
// The invariants below are only worth anything if they can go red. The defect
// they exist to catch is stepping by rune while the measure keeps counting
// cells, so that defect is implemented HERE, in the test file, as a second
// implementation of the same two primitives — the bodies are cutAt and
// cutAtEnd with firstCell swapped for a rune decode and nothing else changed.
//
// Keeping the control in the test file rather than behind a switch in text.go
// means it stays permanently available: TestCutInvariantsRejectRuneStepping
// runs the same invariant bodies over it on every run and asserts that
// cut_budget, cut_boundary and cut_maximal all go red, so any of THOSE THREE
// losing the ability to fail is reported the day it happens. It is not a claim
// about the other two: cut_prefix and cut_utf8 hold for the rune-stepped
// control as well as for the shipped one, and no control in this file reddens
// them. They are NOT the same case, and one fixture does not cover both.
// cut_utf8 would redden under a byte-slicing control (`s[:w]`, the shape of
// the defect this layer replaces), which this file does not have. cut_prefix
// would not: a byte-slicing cut still returns a prefix of its source, and so
// does anything else that returns a SUBSTRING, so no cut whatsoever can
// falsify it — reddening cut_prefix needs a control that re-encodes, reorders
// or inserts.

type cutPair struct {
	name   string
	cut    func(s string, w int) string
	cutEnd func(s string, w int) string
}

// runeFirstCell is firstCell with the segmenter swapped for a rune decode.
//
// The width is taken with ansi.StringWidth rather than with this package's W,
// for the reason stated at the top of the file: a control that measured with
// the code under test would agree with a defect in it, and the defect this
// control exists to reproduce is a disagreement between the stepper and the
// measure.
func runeFirstCell(s string) (string, int) {
	_, n := utf8.DecodeRuneInString(s)
	return s[:n], ansi.StringWidth(s[:n])
}

func runeCutAt(s string, w int) string {
	if w <= 0 {
		return ""
	}
	used := 0
	for i := 0; i < len(s); {
		cluster, cw := runeFirstCell(s[i:])
		if used+cw > w {
			return s[:i]
		}
		i += len(cluster)
		used += cw
	}
	return s
}

func runeCutAtEnd(s string, w int) string {
	if w <= 0 {
		return ""
	}
	remaining := 0
	for i := 0; i < len(s); {
		cluster, cw := runeFirstCell(s[i:])
		remaining += cw
		i += len(cluster)
	}
	i := 0
	for i < len(s) && remaining > w {
		cluster, cw := runeFirstCell(s[i:])
		remaining -= cw
		i += len(cluster)
	}
	return s[i:]
}

// ------------------------------------------------------------- the recorder

// violations collects invariant failures by name, so the same sweep bodies can
// be run against the shipped primitives (expecting none) and against the
// planted ones (expecting specific ones).
type violations struct {
	count map[string]int
	first map[string]string
}

func newViolations() *violations {
	return &violations{count: map[string]int{}, first: map[string]string{}}
}

func (v *violations) add(invariant, format string, a ...any) {
	v.count[invariant]++
	if _, seen := v.first[invariant]; !seen {
		v.first[invariant] = fmt.Sprintf(format, a...)
	}
}

func (v *violations) names() []string {
	out := make([]string, 0, len(v.count))
	for name := range v.count {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// clusterOffsets returns the byte offsets at which grapheme clusters begin in
// s, plus len(s). It is computed with ansi.FirstGraphemeCluster directly rather
// than with the package's own stepper, so "the cut landed on a cluster
// boundary" is a claim about Unicode segmentation and not about the code under
// test agreeing with itself.
func clusterOffsets(s string) map[int]bool {
	out := map[int]bool{0: true, len(s): true}
	for i := 0; i < len(s); {
		cluster, _ := ansi.FirstGraphemeCluster(s[i:], ansi.GraphemeWidth)
		if cluster == "" {
			break
		}
		i += len(cluster)
		out[i] = true
	}
	return out
}

// checkCutInvariants applies every invariant to one (string, budget) pair.
//
// The invariants, and the concrete code change each one exists to catch:
//
//	cut_budget    — ansi.StringWidth(cut) <= w, and never W(cut): an invariant
//	                measured with the function under test would agree with a
//	                defect in it. Red when a cut steps by rune over a multi-rune
//	                cluster, or when a stepper stops subtracting what it ate.
//	cut_prefix    — cutAt returns a prefix of s and cutAtEnd a suffix of it.
//	                Red when a cut reorders, re-encodes or inserts anything.
//	cut_utf8      — the result decodes as UTF-8. Red when a cut slices bytes,
//	                which is what the measured defect it replaces does
//	                (`tools[:maxTools-1]`).
//	cut_boundary  — both ends land on grapheme-cluster boundaries. Red when a
//	                cut splits a cluster and leaves an orphaned U+FE0F or
//	                combining mark behind; a rune-stepped cut that happened to
//	                stay inside its budget still fails here.
//	cut_maximal   — the cut is the LONGEST prefix/suffix that fits. Red when a
//	                primitive is made conservative to buy budget-safety
//	                cheaply: `return ""` and `cutAt(s, w-1)` satisfy every
//	                invariant above and fail this one.
func checkCutInvariants(p cutPair, name, s string, w int, v *violations) {
	bounds := clusterOffsets(s)
	head := p.cut(s, w)
	tail := p.cutEnd(s, w)

	for _, c := range []struct{ fn, got string }{{"cutAt", head}, {"cutAtEnd", tail}} {
		if got := ansi.StringWidth(c.got); got > w {
			v.add("cut_budget", "%s(%s=%q, %d) = %q is %d cells, budget %d",
				c.fn, name, s, w, c.got, got, w)
		}
		if !utf8.ValidString(c.got) {
			v.add("cut_utf8", "%s(%s, %d) = %q is not valid UTF-8", c.fn, name, w, c.got)
		}
	}
	if !strings.HasPrefix(s, head) {
		v.add("cut_prefix", "cutAt(%s=%q, %d) = %q is not a prefix of the source", name, s, w, head)
	}
	if !strings.HasSuffix(s, tail) {
		v.add("cut_prefix", "cutAtEnd(%s=%q, %d) = %q is not a suffix of the source", name, s, w, tail)
	}

	// Boundary: the cut point, expressed as a byte offset into s.
	if !bounds[len(head)] {
		v.add("cut_boundary", "cutAt(%s, %d) = %q cut inside a grapheme cluster at byte %d",
			name, w, head, len(head))
	}
	if !bounds[len(s)-len(tail)] {
		v.add("cut_boundary", "cutAtEnd(%s, %d) = %q cut inside a grapheme cluster at byte %d",
			name, w, tail, len(s)-len(tail))
	}

	// Maximal: one more cluster on the open end would not have fitted.
	if len(head) < len(s) {
		next, _ := ansi.FirstGraphemeCluster(s[len(head):], ansi.GraphemeWidth)
		if ansi.StringWidth(head+next) <= w {
			v.add("cut_maximal", "cutAt(%s=%q, %d) = %q, but %q would still have fitted in %d cells",
				name, s, w, head, head+next, w)
		}
	}
	if len(tail) < len(s) {
		prevStart := 0
		for i := 0; i < len(s)-len(tail); {
			cluster, _ := ansi.FirstGraphemeCluster(s[i:], ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			prevStart = i
			i += len(cluster)
		}
		if ansi.StringWidth(s[prevStart:]) <= w {
			v.add("cut_maximal", "cutAtEnd(%s=%q, %d) = %q, but %q would still have fitted in %d cells",
				name, s, w, tail, s[prevStart:], w)
		}
	}
}

// sweepCutInvariants runs every invariant over the whole corpus and every
// budget in 0..cutMaxBudget, and returns the number of pairs it checked.
func sweepCutInvariants(p cutPair, v *violations) int {
	n := 0
	for _, c := range cutCorpus {
		for w := 0; w <= cutMaxBudget; w++ {
			checkCutInvariants(p, c.name, c.s, w, v)
			n++
		}
	}
	return n
}

func shippedCuts() cutPair { return cutPair{"grapheme-stepped", cutAt, cutAtEnd} }
func runeCuts() cutPair    { return cutPair{"rune-stepped", runeCutAt, runeCutAtEnd} }

// TestCutPrimitiveInvariants is the invariant sweep on the shipped primitives.
func TestCutPrimitiveInvariants(t *testing.T) {
	v := newViolations()
	n := sweepCutInvariants(shippedCuts(), v)
	for _, name := range v.names() {
		t.Errorf("%s: %d violation(s); first: %s", name, v.count[name], v.first[name])
	}
	t.Logf("checked %d (string, budget) pairs over %d strings x budgets 0..%d",
		n, len(cutCorpus), cutMaxBudget)
}

// TestCutCorpusIsPotent refuses to let the sweep above pass for the wrong
// reason. An invariant sweep over strings that never need cutting, or whose
// runes are all their own single-cell clusters, would be green against any
// primitive at all.
func TestCutCorpusIsPotent(t *testing.T) {
	multiRune, wide, cut := 0, 0, 0
	for _, c := range cutCorpus {
		for i := 0; i < len(c.s); {
			cluster, width := ansi.FirstGraphemeCluster(c.s[i:], ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			if utf8.RuneCountInString(cluster) > 1 {
				multiRune++
			}
			if width > 1 {
				wide++
			}
			i += len(cluster)
		}
		// Whether a (string, budget) pair truncates is MEASURED here rather
		// than asked of cutAt: a potency counter that called the function
		// under test would report the corpus as potent exactly when that
		// function was defective, which is the failure this whole file is
		// built to avoid.
		total := ansi.StringWidth(c.s)
		for w := 0; w <= cutMaxBudget; w++ {
			if total > w {
				cut++
			}
		}
	}
	if multiRune == 0 {
		t.Error("no multi-rune grapheme cluster in the corpus: a rune-stepped cut would pass every invariant")
	}
	if wide == 0 {
		t.Error("no cluster wider than one cell in the corpus: the budget invariant could not fail")
	}
	if cut == 0 {
		t.Error("no (string, budget) pair in the corpus actually truncates: the invariants are vacuous")
	}
	t.Logf("corpus potency: %d multi-rune clusters, %d clusters wider than one cell, "+
		"%d of %d (string, budget) pairs actually truncate",
		multiRune, wide, cut, len(cutCorpus)*(cutMaxBudget+1))
}

// TestCutInvariantsRejectRuneStepping is the permanent control: the same
// invariant bodies run over the rune-stepping primitives above, and cut_budget,
// cut_boundary and cut_maximal MUST all go red. Without it, "every invariant
// holds" is equally consistent with invariants that hold for any primitive
// whatsoever.
func TestCutInvariantsRejectRuneStepping(t *testing.T) {
	v := newViolations()
	sweepCutInvariants(runeCuts(), v)
	for _, want := range []string{"cut_budget", "cut_boundary", "cut_maximal"} {
		if v.count[want] == 0 {
			t.Errorf("with rune-stepped cuts, %s stayed green; that invariant cannot fail", want)
		}
	}
	for _, name := range v.names() {
		t.Logf("rune-stepped cuts: %-13s %d violation(s); first: %s", name, v.count[name], v.first[name])
	}
}

// TestCutMeasuredDefect pins the measurement that identified the defect, so
// the plan's claim is reproducible rather than remembered: U+26A0 U+FE0F has
// per-rune widths 1 and 0 and a cluster width of 2, so a rune-stepped cut
// spends one cell of budget on a two-cell glyph.
func TestCutMeasuredDefect(t *testing.T) {
	for _, c := range []struct {
		s string
		w int
	}{{"⚠️", 1}, {"⚠️✔️ℹ️", 3}} {
		got, bad := cutAt(c.s, c.w), runeCutAt(c.s, c.w)
		t.Logf("cutAt(%q, %d) = %q width %d | rune-stepped = %q width %d (budget %d)",
			c.s, c.w, got, ansi.StringWidth(got), bad, ansi.StringWidth(bad), c.w)
		if w := ansi.StringWidth(got); w > c.w {
			t.Errorf("cutAt(%q, %d) returned %d cells", c.s, c.w, w)
		}
		if w := ansi.StringWidth(bad); w <= c.w {
			t.Errorf("the rune-stepped control returned %d cells for a budget of %d; "+
				"it no longer reproduces the defect and the control is vacuous", w, c.w)
		}
	}
}

// ----------------------------------------------------------------- Sanitise

// §4.4: Sanitise strips CSI and OSC sequences and C0/C1 controls and PRESERVES
// tab and newline. Preserving newline is what makes a multi-line upstream
// error wrap as paragraphs; preserving tab is what allows a tab to reach a
// table cell, where ansi.StringWidth measures U+0009 at zero cells and the
// terminal does not — the defect Task 10 fixes at the renderer.
func TestSanitise(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "ws workspace create api", "ws workspace create api"},
		{"sgr", "\x1b[31mred\x1b[0m", "red"},
		{"cursor move", "before\x1b[2Aafter", "beforeafter"},
		{"line clear", "spinner\x1b[2K\rdone", "spinnerdone"},
		{"osc hyperlink", "\x1b]8;;https://example.com\x07text\x1b]8;;\x07", "text"},
		{"osc title rewrite", "\x1b]0;pwned\x07ok", "ok"},
		{"c0 controls", "a\x00b\x07c\x08d", "abcd"},
		{"del", "a\x7fb", "ab"},
		// A real C1 byte, not a placeholder: U+0085 NEL and U+009B (the
		// single-byte CSI introducer) are what a mis-decoded latin-1 stream
		// emits, and they are as capable of moving a cursor as an ESC pair.
		{"c1 controls", "a\u0085b\u009bc", "abc"},
		// A bare ESC that begins no valid sequence: the §4.8 shape, where a
		// child process's stderr is captured mid-write or a read ends inside a
		// sequence. The FIRST row pins ansi.Strip rather than the switch
		// beneath it; the second is a different case, described below.
		//
		// Measured by exhausting every string of length 1 to 3 over an alphabet
		// carrying each structural byte role — ESC, the CSI/OSC/DCS/SOS/PM/APC
		// introducers, the parameter, intermediate and final byte ranges, ST,
		// BEL, CAN, SUB, NUL, ordinary text and an invalid UTF-8 byte: ansi.Strip
		// consumes EVERY ESC and no input leaves one behind. Re-measured over a
		// second, independently chosen alphabet with the same result. The
		// absolute number of inputs is a property of the alphabet and is
		// deliberately not pinned here, because a reader who re-derives it with
		// any other alphabet would get a different one and could not tell
		// agreement from drift.
		//
		// So the C0 arm of Sanitise never sees 0x1b at all, and adding
		// `case r == 0x1b:` to the keep-list above is an EQUIVALENT MUTANT that
		// no fixture can kill. Do not add rows hoping to catch it; the arm is
		// unreachable for ESC by construction, not under-tested.
		//
		// The first shape is pinned because it is surprising and costs data:
		// ESC followed by a plain letter is a complete two-byte sequence, so
		// the LETTER IS CONSUMED WITH IT and "a\x1bb" sanitises to "a", not
		// "ab". A stray ESC in captured output eats the character after it.
		// That row goes red if the ansi.Strip call is dropped, which is what
		// makes it an assertion rather than a note.
		//
		// The second shape — ESC as the last byte, a read that ended inside a
		// sequence — is NOT falsifiable by any single change to Sanitise: drop
		// ansi.Strip and the C0 arm still removes the trailing ESC. It is kept
		// as a pin on x/ansi rather than on this package: if an upgrade ever
		// stopped consuming a trailing lone ESC, this row is the only thing
		// here that would notice.
		{"bare esc takes the next character with it", "a\x1bb", "a"},
		{"bare esc at end of string", "go\x1b", "go"},
		{"tab survives", "go\tnode\tpython", "go\tnode\tpython"},
		{"newline survives", "line one\nline two", "line one\nline two"},
		{"carriage return does not", "line one\rline two", "line oneline two"},
		{"non-ascii text survives", "中文 ⚠️ café", "中文 ⚠️ café"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sanitise(c.in); got != c.want {
				t.Errorf("Sanitise(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	// D-13: SanitiseInline is Sanitise for a surface that must stay on one
	// line, so the newline it preserves above FOLDS TO A SPACE here rather
	// than being dropped — the words either side of the break must not run
	// together. Its first caller is three tasks away; without this assertion
	// the primitive would ship undetected until then.
	if got := SanitiseInline("line one\nline two"); got != "line one line two" {
		t.Errorf("SanitiseInline(%q) = %q, want %q", "line one\nline two", got, "line one line two")
	}
}

// Sanitise must reduce a wrapped string to exactly what it produces for the
// payload alone: the wrapper contributes NOTHING. That is the property that
// stops the layer laundering control sequences captured from a child process
// into the operator's session (§4.4, §4.8), because it is a property of
// ansi.Strip removing the sequence payload rather than of the C0 branch
// dropping the ESC byte.
//
// The equality is stated because the two absolute checks beside it CANNOT
// carry that claim: ESC is below 0x20, so the C0 branch drops it for every
// input and neither check can see ansi.Strip go missing. Measured over the 48
// corpus x wrapper combinations, with ansi.Strip deleted from Sanitise: the
// equality fails 48 times, the ESC check 0, the UTF-8 check 0.
//
// All three are kept, because none is redundant with the others. The equality
// is a RELATIVE property and is blind to any defect that mangles both sides
// alike; measured, an emit that truncates multi-byte runes to a single byte is
// caught only by utf8.ValidString (36 of 48), and a Sanitise that appends an
// escape of its own to every result is caught only by the ESC check (48 of 48).
//
// PRECONDITION on the corpus, which is shared with the cut sweep and declared
// there as "the hostile material": no cutCorpus entry carries a C0, C1 or DEL
// byte (measured, 0 of 16), so no entry can complete, terminate or truncate a
// wrapper's escape sequence. A hostile row added later for CUT reasons that does
// carry one would surface here as a puzzling inequality — that would be this
// precondition breaking, not Sanitise.
func TestSanitiseWrapperContributesNothing(t *testing.T) {
	for _, c := range cutCorpus {
		for _, wrapper := range []string{
			"\x1b[1;31m%s\x1b[0m",
			"\x1b]8;;file:///etc/passwd\x07%s\x1b]8;;\x07",
			"%s\x1b[2J\x1b[H",
		} {
			in := fmt.Sprintf(wrapper, c.s)
			got := Sanitise(in)
			if got != Sanitise(c.s) {
				t.Errorf("Sanitise(%q) = %q, want the wrapper to contribute nothing: %q",
					in, got, Sanitise(c.s))
			}
			if strings.ContainsRune(got, 0x1b) {
				t.Errorf("Sanitise(%q) left an ESC byte: %q", in, got)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Sanitise(%q) = %q is not valid UTF-8", in, got)
			}
		}
	}
}

// ----------------------------------------------------- truncation and padding

// §4.3 step 4: each Trunc mode keeps the end that carries the information, and
// every mode stays inside the budget in both glyph modes. The ASCII marker is
// THREE cells, so a mode that reserves one cell for it overflows by two — which
// is exactly the arithmetic this asserts.
func TestTruncateModes(t *testing.T) {
	const s = "golangci-lint, node, jq, yq, fzf, ripgrep"
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		for _, tm := range []Trunc{TruncTail, TruncHead, TruncMid} {
			for w := 0; w <= 45; w++ {
				got := truncate(tm, s, w, mode)
				if ansi.StringWidth(got) > w {
					t.Errorf("truncate(%d, %d cells, mode %d) = %q is %d cells",
						tm, w, mode, got, ansi.StringWidth(got))
				}
				if !utf8.ValidString(got) {
					t.Errorf("truncate(%d, %d, mode %d) = %q is not valid UTF-8", tm, w, mode, got)
				}
			}
		}
	}
	// The information each mode preserves, at a budget with room for the marker.
	if got := truncate(TruncTail, s, 20, GlyphUTF8); !strings.HasPrefix(got, "golangci-lint") {
		t.Errorf("TruncTail lost the head: %q", got)
	}
	if got := truncate(TruncHead, s, 20, GlyphUTF8); !strings.HasSuffix(got, "ripgrep") {
		t.Errorf("TruncHead lost the tail: %q", got)
	}
	mid := truncate(TruncMid, s, 20, GlyphUTF8)
	if !strings.HasPrefix(mid, "golangci") || !strings.HasSuffix(mid, "ripgrep") {
		t.Errorf("TruncMid lost an end: %q", mid)
	}
	// A budget at or below the marker's own width leaves no room for it: the
	// cut is hard and the marker is not emitted, because emitting it would
	// cost more cells than the whole budget.
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		w := markerWidth(mode)
		if got := truncate(TruncTail, s, w, mode); strings.Contains(got, marker(mode)) {
			t.Errorf("mode %d: truncate at a budget of %d emitted the marker: %q", mode, w, got)
		}
	}
	// A string that already fits is returned untouched, marker or no marker.
	if got := truncate(TruncTail, "api", 10, GlyphUTF8); got != "api" {
		t.Errorf("truncate of a string that fits = %q, want %q", got, "api")
	}
}

// Pad and PadLeft measure display cells, not bytes and not runes: a cell
// holding ⚠️ is padded by two fewer spaces than a rune count would suggest,
// which is what keeps a column's field the width the allocator assigned it.
func TestPadMeasuresCells(t *testing.T) {
	for _, c := range cutCorpus {
		for w := 0; w <= cutMaxBudget; w++ {
			for _, p := range []struct {
				name string
				fn   func(string, int) string
			}{{"Pad", Pad}, {"PadLeft", PadLeft}} {
				got := p.fn(c.s, w)
				width := ansi.StringWidth(got)
				if ansi.StringWidth(c.s) >= w {
					if got != c.s {
						t.Errorf("%s(%s, %d) altered a string already at or over the width: %q", p.name, c.name, w, got)
					}
					continue
				}
				if width != w {
					t.Errorf("%s(%s=%q, %d) = %q is %d cells, want exactly %d", p.name, c.name, c.s, w, got, width, w)
				}
			}
		}
	}
	if got := Pad("api", 6); got != "api   " {
		t.Errorf("Pad(%q, 6) = %q", "api", got)
	}
	if got := PadLeft("api", 6); got != "   api" {
		t.Errorf("PadLeft(%q, 6) = %q", "api", got)
	}
}

// --------------------------------------------------------------------- Wrap

// §4.4 and §6.2: Wrap honours existing newlines as paragraph breaks, never
// emits a line wider than the budget, and HARD-BREAKS a run with no break
// opportunity rather than letting it overflow — §4.3's rule that the width
// contract outranks every other invariant applies to prose too.
func TestWrapNeverExceedsBudget(t *testing.T) {
	inputs := []string{
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\nIs the docker daemon running?",
		strings.Repeat("a", 200),
		"ws proxy profile import ./" + strings.Repeat("b", 120) + ".json",
		"api ⚠️ 中文 👨‍👩‍👧‍👦 café [2001:db8::1]:443 ✔️",
		strings.Repeat("中", 40),
		"",
	}
	for _, in := range inputs {
		for w := 1; w <= 60; w++ {
			for _, line := range Wrap(in, w) {
				if got := ansi.StringWidth(line); got > w {
					// The single exemption, and it is deliberate: one grapheme
					// cluster wider than the entire budget is emitted whole,
					// because a cluster is the smallest thing a terminal draws
					// and cutting inside it orphans a variation selector or a
					// combining mark. Anything else over budget is a defect,
					// so the exemption is asserted rather than assumed.
					cluster, _ := ansi.FirstGraphemeCluster(line, ansi.GraphemeWidth)
					if cluster != line {
						t.Errorf("Wrap(%q, %d) emitted a %d-cell line that is not a single cluster: %q",
							in, w, got, line)
					}
				}
				if !utf8.ValidString(line) {
					t.Errorf("Wrap(%q, %d) emitted invalid UTF-8: %q", in, w, line)
				}
			}
		}
	}
}

// Wrapping is not truncation: every character of the input survives once the
// breaks are collapsed. This is the assertion that goes red if Wrap is ever
// "simplified" into a clipTail.
func TestWrapLosesNothing(t *testing.T) {
	const in = "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n" +
		"Is the docker daemon running? See https://docs.docker.com/go/daemon/ for help."
	// Local to this test and deliberately not named `squash`: it splits on
	// strings.Fields' whitespace set, which is wider than the space-and-newline
	// fold the package itself uses elsewhere.
	squashLocal := func(s string) string { return strings.Join(strings.Fields(s), "") }
	for w := 10; w <= 120; w++ {
		got := squashLocal(strings.Join(Wrap(in, w), ""))
		if got != squashLocal(in) {
			t.Errorf("Wrap(_, %d) lost or reordered characters:\n got %q\nwant %q", w, got, squashLocal(in))
		}
	}
}

// A single grapheme cluster wider than the whole budget is emitted alone
// rather than cut into fragments: a cluster is the smallest thing a terminal
// draws, and cutting inside it orphans a variation selector or combining mark.
// The loop must also make progress — a budget of 1 against a 2-cell cluster is
// the shape that spins forever if the hard break is written naively.
func TestWrapEmitsOversizedClusterWhole(t *testing.T) {
	lines := Wrap("⚠️⚠️⚠️", 1)
	if len(lines) != 3 {
		t.Fatalf("Wrap(3 x 2-cell clusters, 1) = %d line(s): %q", len(lines), lines)
	}
	for i, l := range lines {
		if l != "⚠️" {
			t.Errorf("line %d = %q, want a whole cluster %q", i, l, "⚠️")
		}
	}
}

// The paragraph structure of a multi-line upstream error survives: two
// newline-separated paragraphs never share a line, whatever the budget.
func TestWrapKeepsParagraphs(t *testing.T) {
	got := Wrap("first paragraph\nsecond paragraph", 80)
	want := []string{"first paragraph", "second paragraph"}
	if len(got) != len(want) {
		t.Fatalf("Wrap of two paragraphs at 80 = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// wrapIndent's contract is stated in the FULL budget, not the inner one: every
// line it returns, indent included, is at most w cells. An indent deeper than
// the budget must still not overflow.
//
// Only the width half is asserted. The prefix half the draft carried — every
// line starts with indent spaces — is a tautology against a body that builds
// each line as prefix+l, so it could not have gone red for any implementation
// of the clamp.
func TestWrapIndentCountsTheIndent(t *testing.T) {
	const body = "Cannot connect to the Docker daemon at unix:///var/run/docker.sock."
	for w := 4; w <= 60; w++ {
		for indent := 0; indent <= 6; indent++ {
			for _, line := range wrapIndent(body, indent, w) {
				if got := ansi.StringWidth(line); got > w {
					t.Errorf("wrapIndent(indent=%d, w=%d) emitted a %d-cell line: %q", indent, w, got, line)
				}
			}
		}
	}
}

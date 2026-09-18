package output

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Display-width measurement, wrapping, truncation, padding and sanitising.
//
// Everything in this file measures display columns, never bytes and never
// runes, and every cut lands on a grapheme-cluster boundary. The measured
// defect it replaces slices bytes (`tools[:maxTools-1]`) and can emit invalid
// UTF-8.

// Trunc is a column's truncation mode.
//
// It is declared here rather than beside the column type it configures: the
// only function that switches on it is truncate, below, and the text layer
// owns the cut. A column carries a Trunc as a field and decides nothing about
// how the cut is made.
type Trunc int

const (
	TruncTail Trunc = iota
	TruncHead
	TruncMid
)

// W is the display width of a string in terminal columns.
func W(s string) int {
	return ansi.StringWidth(s)
}

// Sanitise strips CSI and OSC sequences and C0/C1 control characters, keeping
// tab and newline (§4.4).
//
// This is not cosmetic. §4.8 captures a child process's stderr and places it
// in Problem.Cause, and that text routinely carries ANSI: a failing Docker,
// xray or devpad invocation can emit cursor moves, line clears, OSC hyperlinks
// or title rewrites. ansi.StringWidth counts those as zero-width, so the width
// sweep passes while the terminal is being rewritten — the layer would be
// laundering arbitrary control sequences from a container into the operator's
// session. Raw bytes survive only in --json output, where they are data
// rather than instructions.
//
// Tab survives this step and does not survive the message path, and the two
// rules are not the layer disagreeing with itself. Sanitise decides what is
// SAFE to forward: a tab advances to the next tab stop and cannot move the
// cursor arbitrarily, clear the screen or rewrite the window title, so it is
// not stripped along with the other controls. What a block then DOES with the
// byte is the block's own rule, and prose reflows — Wrap re-joins each
// paragraph on single spaces, so a tab inside a message reaches the terminal
// as one space. The byte is kept here so that a caller which has a use for it
// still has it to use.
func Sanitise(s string) string {
	stripped := ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(stripped))
	for _, r := range stripped {
		switch {
		case r == '\t' || r == '\n':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f: // C0 and DEL
		case r >= 0x80 && r <= 0x9f: // C1
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SanitiseInline is Sanitise for text that must occupy exactly one line: a
// table cell, a column title, a caption clause (§4.4, D-13). Newlines are
// folded to a single space rather than stripped, so two words either side of
// a break do not run together.
func SanitiseInline(s string) string {
	return strings.ReplaceAll(Sanitise(s), "\n", " ")
}

// ------------------------------------------------------------- truncation
//
// Every cut in this file steps by GRAPHEME CLUSTER, never by rune, because
// the unit the budget contract of §4.3 is written in — the display cell — is
// a property of the cluster and not of the runes inside it. An
// emoji-presentation sequence such as ⚠️ (U+26A0 U+FE0F) is two runes whose
// per-rune widths are 1 and 0; a terminal draws it in two cells. A cut that
// stepped rune by rune would spend one cell of budget on a two-cell glyph and
// overflow, and it could also stop between the base character and its
// variation selector, emitting a cluster fragment.
//
// That is not a theoretical input. §4.8 fills Problem.Cause from a child
// process's captured stderr, and Docker, devpod and CI tooling emit ⚠️ ✔️ ℹ️
// in ordinary diagnostics. The ASCII glyph mode is no defence: §4.5 changes
// the glyphs THIS LAYER emits, not the user data flowing through it.
//
// The two primitives are hand-rolled on x/ansi's grapheme-cluster stepper
// rather than delegated to ansi.Truncate / ansi.TruncateLeft, because only
// the forward half of that pair is budget-safe: measured at v0.11.6,
// ansi.TruncateLeft(s, W(s)-w) keeps the whole cluster the cut lands inside,
// so the suffix it returns can be wider than the budget asked for (⚠️✔️ℹ️ at
// w=3 comes back as "✔️ℹ️", four cells). cutAtEnd must not overshoot, and one
// cut rule forwards with a different one backwards would be worse than
// either.

// firstCell returns the leading grapheme cluster of s and its display width.
//
// Segmentation comes from ansi.FirstGraphemeCluster with ansi.GraphemeWidth,
// which is the segmenter ansi.StringWidth itself measures with, so the
// stepper and the measure agree by construction. The width is taken with W
// rather than with the number FirstGraphemeCluster returns, so that every
// cell this package counts is counted by one function.
//
// It never returns an empty cluster for a non-empty s, so the loops below can
// step on its result without a progress guard of their own.
func firstCell(s string) (cluster string, width int) {
	cluster, _ = ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	if cluster == "" && s != "" {
		cluster = s[:1] // unreachable: the segmenter always consumes a byte
	}
	return cluster, W(cluster)
}

// cutAt returns the longest prefix of s whose display width is at most w.
func cutAt(s string, w int) string {
	if w <= 0 {
		return ""
	}
	used := 0
	for i := 0; i < len(s); {
		cluster, cw := firstCell(s[i:])
		if used+cw > w {
			return s[:i]
		}
		i += len(cluster)
		used += cw
	}
	// Never emit a cluster that does not fit: a single two-cell glyph in a
	// one-cell budget is dropped, because emitting it would overflow.
	return s
}

// cutAtEnd returns the longest suffix of s whose display width is at most w.
//
// It drops the fewest leading clusters that bring the remainder inside the
// budget, which is the same cut rule as cutAt read from the other end.
func cutAtEnd(s string, w int) string {
	if w <= 0 {
		return ""
	}
	remaining := 0
	for i := 0; i < len(s); {
		cluster, cw := firstCell(s[i:])
		remaining += cw
		i += len(cluster)
	}
	i := 0
	for i < len(s) && remaining > w {
		cluster, cw := firstCell(s[i:])
		remaining -= cw
		i += len(cluster)
	}
	return s[i:]
}

// clipTail keeps the head: "golangci-lint, nod…".
func clipTail(s string, w int, mode GlyphMode) string {
	if w <= 0 {
		return ""
	}
	if W(s) <= w {
		return s
	}
	mw := markerWidth(mode)
	if w <= mw {
		return cutAt(s, w) // no room for the marker: hard cut
	}
	return cutAt(s, w-mw) + marker(mode)
}

// clipHead keeps the tail: "…8a2e:370:7334]:443" — addresses, where the port
// must survive.
func clipHead(s string, w int, mode GlyphMode) string {
	if w <= 0 {
		return ""
	}
	if W(s) <= w {
		return s
	}
	mw := markerWidth(mode)
	if w <= mw {
		return cutAtEnd(s, w)
	}
	return marker(mode) + cutAtEnd(s, w-mw)
}

// clipMid keeps both ends: "aaaaaaaa…hhhhhhhh" — names.
func clipMid(s string, w int, mode GlyphMode) string {
	if w <= 0 {
		return ""
	}
	if W(s) <= w {
		return s
	}
	mw := markerWidth(mode)
	if w <= mw+1 {
		return clipTail(s, w, mode)
	}
	left := (w - mw) / 2
	right := w - mw - left
	return cutAt(s, left) + marker(mode) + cutAtEnd(s, right)
}

// truncate applies a column's Trunc mode (§4.3 step 4).
func truncate(mode Trunc, s string, w int, glyphs GlyphMode) string {
	switch mode {
	case TruncHead:
		return clipHead(s, w, glyphs)
	case TruncMid:
		return clipMid(s, w, glyphs)
	default:
		return clipTail(s, w, glyphs)
	}
}

// ---------------------------------------------------------------- padding

// Pad right-pads to exactly w display columns; a no-op when already wider.
//
// The measurement is W, which is ansi.StringWidth and therefore already sums
// grapheme clusters rather than runes: a cell holding ⚠️ is padded by two
// fewer spaces than a rune count would suggest, which is what keeps the
// column's field the width the allocator assigned it (§4.3).
func Pad(s string, w int) string {
	if d := w - W(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// PadLeft left-pads to exactly w display columns, for right-aligned columns.
func PadLeft(s string, w int) string {
	if d := w - W(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// ---------------------------------------------------------------- wrapping

// Wrap breaks s into lines no wider than w display columns. Existing newlines
// are honoured as paragraph breaks, which is what a multi-line upstream error
// needs.
//
// WHITESPACE INSIDE A PARAGRAPH IS NOT PRESERVED. Each paragraph is split on
// its whitespace and re-joined with one space, so every run of spaces or tabs
// collapses — at every width, including widths at which nothing wraps.
// Measured: Wrap("Name:    api", 80) returns ["Name: api"], and Wrap of
// "a<tab>b" at 80 returns ["a b"]. That is this primitive's job: it lays prose
// out against a budget, and a run it tried to carry would have to answer what
// becomes of a run straddling a break, and of leading whitespace on a
// continuation line. No characters are lost, only the spacing between them. A
// caller that aligns columns by padding its own text is relying on something
// this function does not offer, and belongs on a block type that owns its
// geometry instead. probeMessageRouting in message_test.go pins the collapse.
//
// A run of characters with no break opportunity — the 200-character
// unbreakable token of §6.2 — is hard-broken at the budget rather than
// allowed to overflow: §4.3's rule that the width contract outranks every
// other invariant applies to text blocks too, not only to tables.
func Wrap(s string, w int) []string {
	if w < 1 {
		w = 1 // defensive: an indent deeper than the budget must not overflow
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		start := len(out)
		line := ""
		for _, word := range strings.Fields(para) {
			for W(word) > w {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				head := cutAt(word, w)
				if head == "" {
					// A single grapheme cluster wider than the whole budget:
					// emit it alone so the loop always makes progress. A
					// cluster is the smallest thing a terminal draws, so it is
					// emitted whole — cutting inside it would leave an
					// orphaned variation selector or combining mark behind.
					head, _ = firstCell(word)
				}
				out = append(out, head)
				word = word[len(head):]
			}
			switch {
			case line == "":
				line = word
			case W(line)+1+W(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}

		// The pending line closes the paragraph. It is empty only when the
		// hard break above consumed the whole word — every cluster in it wider
		// than the budget — and an empty pending line is then a blank line the
		// caller never asked for: measured, Wrap("<3 x 2-cell clusters>", 1)
		// returned four lines, the last of them empty. An EMPTY PARAGRAPH is a
		// different thing and still yields its blank line, which is what keeps
		// the paragraph structure of a multi-line upstream error intact.
		if line != "" || len(out) == start {
			out = append(out, line)
		}
	}
	return out
}

// wrapIndent wraps body into w-indent columns and prefixes every line with
// indent spaces. Every returned line is at most w columns wide.
func wrapIndent(body string, indent, w int) []string {
	// The contract is stated in the FULL budget, so an indent that leaves no
	// room for content is clamped rather than allowed to push every line one
	// cell past w. Measured: without this, wrapIndent(body, 6, 6) emits
	// seven-cell lines.
	if indent < 0 {
		indent = 0
	}
	if indent >= w {
		indent = w - 1
		if indent < 0 {
			indent = 0
		}
	}
	prefix := strings.Repeat(" ", indent)
	lines := Wrap(body, w-indent)
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, prefix+l)
	}
	return out
}

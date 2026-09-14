package output

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// §4.5 Glyph mode.
//
// The ASCII set is not a state-only fallback. It is a glyph mode covering
// three families together — state marks, the truncation marker and the box
// drawing — because the truncation marker (U+2026) and all eleven rounded
// border glyphs (U+2500–U+256F) are East-Asian *Ambiguous*: one cell under
// the Western convention, two on a CJK-configured terminal. The borders are
// the bulk of the glyphs the layer emits, so restricting only the state
// vocabulary to width-1 runes fixes nothing.

// GlyphMode selects the glyph family a Stream renders with.
type GlyphMode int

const (
	// GlyphUTF8 keeps the box-drawing borders and the U+2026 marker.
	GlyphUTF8 GlyphMode = iota
	// GlyphASCII replaces every Ambiguous glyph with a width-1 ASCII one.
	GlyphASCII
)

// Truncation markers. The ASCII marker is three cells wide, which the
// allocator must reserve — see markerWidth and §4.3 step 5(b).
const (
	markerUTF8  = "…"
	markerASCII = "..."
)

// marker returns the truncation marker for a glyph mode.
func marker(mode GlyphMode) string {
	if mode == GlyphASCII {
		return markerASCII
	}
	return markerUTF8
}

// markerWidth is the display width of the truncation marker in this mode.
// It is also the floor of §4.3 step 5(b): a column narrower than its own
// marker could not place it and would either overflow or silently clip.
func markerWidth(mode GlyphMode) int { return ansi.StringWidth(marker(mode)) }

// asciiBorder is the ASCII counterpart of lipgloss.RoundedBorder():
// `- | + + + + + + + + +` for the eleven Ambiguous box-drawing glyphs.
var asciiBorder = lipgloss.Border{
	Top: "-", Bottom: "-", Left: "|", Right: "|",
	TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	MiddleLeft: "+", MiddleRight: "+", Middle: "+",
	MiddleTop: "+", MiddleBottom: "+",
}

// border returns the table border set for a glyph mode. Borders stay wherever
// they are safe and degrade to ASCII exactly where they would otherwise double
// (§4.5) — the owner's decision to keep box-drawing borders is preserved.
func border(mode GlyphMode) lipgloss.Border {
	if mode == GlyphASCII {
		return asciiBorder
	}
	return lipgloss.RoundedBorder()
}

// cjkLanguages are the language subtags that imply the East-Asian Ambiguous
// *wide* convention. On such a terminal the marker and every border glyph
// render at two cells while the layer measures them at one, so the mode has
// to change rather than the arithmetic (§4.5).
var cjkLanguages = map[string]bool{"ja": true, "ko": true, "zh": true}

// glyphModeFromEnv implements §4.5's selection rule: ASCII when the locale is
// not UTF-8, when the locale implies the Ambiguous-wide convention (a CJK
// language tag), or when WS_ASCII=1.
//
// RUNEWIDTH_EASTASIAN is included because it declares that convention
// explicitly; x/ansi reads it in init(), so a process running under it is
// measuring Ambiguous glyphs at two cells whatever the locale says.
func glyphModeFromEnv(getenv func(string) string) GlyphMode {
	if getenv("WS_ASCII") == "1" {
		return GlyphASCII
	}
	if v := getenv("RUNEWIDTH_EASTASIAN"); v == "1" || strings.EqualFold(v, "true") {
		return GlyphASCII
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		locale := getenv(key)
		if locale == "" {
			continue
		}
		if !isUTF8Locale(locale) || isCJKLocale(locale) {
			return GlyphASCII
		}
		return GlyphUTF8
	}
	// No locale set at all: assume the safe mode rather than the pretty one.
	return GlyphASCII
}

func isUTF8Locale(locale string) bool {
	up := strings.ToUpper(locale)
	return strings.Contains(up, "UTF-8") || strings.Contains(up, "UTF8")
}

// isCJKLocale reports whether the primary language subtag of a locale string
// such as "ja_JP.UTF-8", "zh-Hans-CN.UTF-8" or "ko_KR" is CJK.
func isCJKLocale(locale string) bool {
	tag := locale
	if i := strings.IndexAny(tag, ".@"); i >= 0 {
		tag = tag[:i]
	}
	if i := strings.IndexAny(tag, "_-"); i >= 0 {
		tag = tag[:i]
	}
	return cjkLanguages[strings.ToLower(tag)]
}

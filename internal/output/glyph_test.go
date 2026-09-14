package output

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// §4.5: the mode is selected when the locale is not UTF-8, when the locale
// implies the Ambiguous-wide convention (a CJK language tag), or when WS_ASCII=1.
// Measured before this test existed: glyphModeFromEnv had 0.0% coverage, so
// selecting the UTF-8 set on ja_JP passed the entire suite.
//
// glyphModeCases is declared at package scope and NOT inside the test,
// because Task 4's probeGlyphMode drives the same table: the contract
// mutation harness of Task 11 asks which assertion noticed `IgnoreCJKTag`,
// and it must ask it of the same cases this test runs, not of a second
// weaker copy. One table, two drivers.
var glyphModeCases = []struct {
	name string
	env  map[string]string
	want GlyphMode
}{
	{"empty environment", map[string]string{}, GlyphASCII},
	{"explicit opt-in", map[string]string{"WS_ASCII": "1", "LC_ALL": "en_US.UTF-8"}, GlyphASCII},
	{"ambiguous-wide opt-in", map[string]string{"RUNEWIDTH_EASTASIAN": "1", "LC_ALL": "en_US.UTF-8"}, GlyphASCII},
	// RUNEWIDTH_EASTASIAN also accepts "true" case-insensitively (strings.EqualFold);
	// the "1" row above never exercises that half of the condition.
	{"ambiguous-wide opt-in, true", map[string]string{"RUNEWIDTH_EASTASIAN": "TrUe", "LC_ALL": "en_US.UTF-8"}, GlyphASCII},
	{"utf-8 western", map[string]string{"LC_ALL": "en_US.UTF-8"}, GlyphUTF8},
	{"utf-8 western, lc_ctype", map[string]string{"LC_CTYPE": "en_GB.UTF-8"}, GlyphUTF8},
	{"utf-8 western, lang only", map[string]string{"LANG": "de_DE.utf8"}, GlyphUTF8},
	{"non-utf-8", map[string]string{"LC_ALL": "en_US.ISO-8859-1"}, GlyphASCII},
	{"japanese", map[string]string{"LC_ALL": "ja_JP.UTF-8"}, GlyphASCII},
	// isCJKLocale also strips an "@modifier" suffix; nothing above carries one.
	{"japanese with modifier", map[string]string{"LC_ALL": "ja_JP.UTF-8@cjknarrow"}, GlyphASCII},
	{"simplified chinese", map[string]string{"LC_ALL": "zh_CN.UTF-8"}, GlyphASCII},
	{"chinese with script tag", map[string]string{"LANG": "zh-Hans-CN.UTF-8"}, GlyphASCII},
	{"korean", map[string]string{"LC_CTYPE": "ko_KR.UTF-8"}, GlyphASCII},
	// Precedence: LC_ALL wins over LC_CTYPE wins over LANG.
	{"lc_all beats lang", map[string]string{"LC_ALL": "ja_JP.UTF-8", "LANG": "en_US.UTF-8"}, GlyphASCII},
	{"lc_ctype beats lang", map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "ja_JP.UTF-8"}, GlyphUTF8},
}

// glyphModeEnv turns one case's map into the getenv seam glyphModeFromEnv
// takes. Declared beside the table because Task 4's probe needs it too.
func glyphModeEnv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestGlyphModeFromEnv(t *testing.T) {
	for _, c := range glyphModeCases {
		t.Run(c.name, func(t *testing.T) {
			got := glyphModeFromEnv(glyphModeEnv(c.env))
			if got != c.want {
				t.Errorf("glyphModeFromEnv(%v) = %v, want %v", c.env, got, c.want)
			}
		})
	}
}

// isCJKLocale strips an optional "@modifier" suffix before splitting on
// "_"/"-" for the primary language subtag. "ja@cjknarrow" is the minimal
// input that actually exercises the "@" branch of that first split: it has
// no "." and no territory subtag, so nothing upstream of "@" truncates the
// tag first. A realistic locale such as "ja_JP.UTF-8@cjknarrow" (covered by
// glyphModeCases above) does NOT discriminate — its "." at index 5 is matched
// before the "@" at index 11, so the same result comes back whether or not
// "@" is in the cutset. Testing isCJKLocale directly here, rather than
// through glyphModeFromEnv, also sidesteps a second trap: a non-UTF-8 locale
// short-circuits glyphModeFromEnv's `!isUTF8Locale(locale) ||
// isCJKLocale(locale)` clause on its first disjunct regardless of what
// isCJKLocale returns.
var isCJKLocaleCases = []struct {
	name   string
	locale string
	want   bool
}{
	{"minimal cjk with modifier, no other separator", "ja@cjknarrow", true},
	{"full locale with codeset", "ja_JP.UTF-8", true},
	{"script-tagged locale", "zh-Hans-CN.UTF-8", true},
	{"lc_ctype-style, no codeset", "ko_KR", true},
	{"non-cjk", "en_US.UTF-8", false},
}

func TestIsCJKLocale(t *testing.T) {
	for _, c := range isCJKLocaleCases {
		t.Run(c.name, func(t *testing.T) {
			if got := isCJKLocale(c.locale); got != c.want {
				t.Errorf("isCJKLocale(%q) = %v, want %v", c.locale, got, c.want)
			}
		})
	}
}

// §4.5 and §6.4: every glyph the layer emits must be width 1 in its mode, and
// the ASCII marker is THREE cells — the number the allocator's step-5(b) floor
// depends on. At this task markerWidth is defined as exactly
// ansi.StringWidth(marker(mode)), so the consistency loop below cannot fail;
// the two literal assertions against 1 and 3 are what carry the test.
func TestGlyphWidths(t *testing.T) {
	if got := markerWidth(GlyphUTF8); got != 1 {
		t.Errorf("markerWidth(GlyphUTF8) = %d, want 1", got)
	}
	if got := markerWidth(GlyphASCII); got != 3 {
		t.Errorf("markerWidth(GlyphASCII) = %d, want 3", got)
	}
	for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
		if got := ansi.StringWidth(marker(mode)); got != markerWidth(mode) {
			t.Errorf("mode %v: marker %q measures %d but markerWidth says %d",
				mode, marker(mode), got, markerWidth(mode))
		}
	}
	// Every state mark is one cell in both modes.
	for _, st := range allStates {
		for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
			if got := ansi.StringWidth(stateMark(st, mode)); got != 1 {
				t.Errorf("state %v mode %v: mark %q is %d cells, want 1",
					st, mode, stateMark(st, mode), got)
			}
		}
	}
	// Every ASCII border glyph is a one-cell ASCII byte. §6.4 names "all eleven
	// box-drawing glyphs"; the UTF-8 set is Ambiguous by construction, which is
	// why the ASCII mode exists — so the assertion that carries weight is that
	// the ASCII set genuinely escapes it.
	b := border(GlyphASCII)
	for _, g := range []string{b.Top, b.Bottom, b.Left, b.Right,
		b.TopLeft, b.TopRight, b.BottomLeft, b.BottomRight,
		b.MiddleLeft, b.MiddleRight, b.Middle, b.MiddleTop, b.MiddleBottom} {
		if g == "" {
			continue
		}
		if ansi.StringWidth(g) != 1 {
			t.Errorf("ASCII border glyph %q is %d cells, want 1", g, ansi.StringWidth(g))
		}
		for _, r := range g {
			if r > 0x7f {
				t.Errorf("ASCII border glyph %q carries a non-ASCII rune %q", g, r)
			}
		}
	}
}

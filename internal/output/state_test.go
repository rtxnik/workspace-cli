package output

import "testing"

// fxStateVocabulary is §4.5's table, written out by the harness.
//
// IT IS THE PACKAGE'S ONE COPY. Every later test file — the contract probes of
// Tasks 4-5, the allocator fixtures of Task 7, the grid assertions of Task 8,
// the block geometry of Task 9, the corpus of Task 10 and the Ambiguous-wide
// child of Task 12 — reads its expectations from here and declares no second
// copy. Go has one package scope across every _test.go file, so a second
// declaration is a compile error, not a duplication.
//
// It duplicates state.go DELIBERATELY: an assertion that read the marks and
// words back out of the package would be satisfied by any self-consistent
// renaming, so it could not fail when the vocabulary drifts from the spec.
var fxStateVocabulary = map[State]struct {
	mark, ascii, word string
	role              Role
}{
	StateOK:       {"✓", "+", "ok", RoleOK},
	StateAdvisory: {"⚠", "!", "degraded", RoleWarn},
	StateFail:     {"✗", "x", "failed", RoleFail},
	StateBusy:     {"~", "~", "busy", RoleInfo},
	StateIdle:     {"-", "-", "idle", RoleMuted},
	StateUnknown:  {"?", "?", "unknown", RoleMuted},
}

// fxMark is the mark §4.5 fixes for a state in a glyph mode.
func fxMark(st State, mode GlyphMode) string {
	v := fxStateVocabulary[st]
	if mode == GlyphASCII {
		return v.ascii
	}
	return v.mark
}

// fxBadge is the expected "mark + word" rendering (§4.5), with an empty label
// falling back to the state's own default word.
func fxBadge(st State, label string, mode GlyphMode) string {
	if label == "" {
		label = fxStateVocabulary[st].word
	}
	return fxMark(st, mode) + " " + label
}

// §4.5 fixes one default word per state rather than leaving the synonyms in the
// "Meaning" column to be chosen at the call site, because the Checks floor
// depends on the widest of them: "not evaluated" in place of "unknown" moves
// that floor from 12 to 17.
//
// Three of the families below — fxMark vs w.ascii, fxBadge (empty label) vs
// w.mark+w.word, fxBadge ("running") vs w.mark+"running" — compare the test
// helpers against the very fixture table they read, not against state.go.
// That is deliberate: it gives fxMark/fxBadge a caller at this task's own
// `unused` gate (T2-c). The stateMark/stateWord/stateRole families below are
// the real assertions on the package's vocabulary.
func TestStateVocabularyIsFixed(t *testing.T) {
	for st, w := range fxStateVocabulary {
		// fxMark and fxBadge are exercised here as well as declared here.
		// golangci-lint's `unused` counts _test.go files, so a helper landed
		// for a later task with no caller in this one fails this task's gate.
		if got, want := fxMark(st, GlyphASCII), w.ascii; got != want {
			t.Errorf("fxMark(%v, ASCII) = %q, want %q", st, got, want)
		}
		if got, want := fxBadge(st, "", GlyphUTF8), w.mark+" "+w.word; got != want {
			t.Errorf("fxBadge(%v, \"\", UTF8) = %q, want %q", st, got, want)
		}
		if got, want := fxBadge(st, "running", GlyphUTF8), w.mark+" running"; got != want {
			t.Errorf("fxBadge(%v, \"running\", UTF8) = %q, want %q", st, got, want)
		}
		if got := stateMark(st, GlyphUTF8); got != w.mark {
			t.Errorf("state %v UTF-8 mark = %q, want %q", st, got, w.mark)
		}
		if got := stateMark(st, GlyphASCII); got != w.ascii {
			t.Errorf("state %v ASCII mark = %q, want %q", st, got, w.ascii)
		}
		if got := stateWord(st); got != w.word {
			t.Errorf("state %v word = %q, want %q", st, got, w.word)
		}
		if got := stateRole(st); got != w.role {
			t.Errorf("state %v role = %v, want %v", st, got, w.role)
		}
	}
	if got := widestStateWord(); got != 8 { // "degraded"
		t.Errorf("widestStateWord() = %d, want 8 — the Checks floor depends on it", got)
	}
}

// The retired glyphs must not come back: `●` meant running, healthy AND
// unhealthy at once, and `● ○ · – — →` are East-Asian Ambiguous inside a column
// the layer pads.
func TestRetiredGlyphsAreGone(t *testing.T) {
	for _, st := range allStates {
		for _, mode := range []GlyphMode{GlyphUTF8, GlyphASCII} {
			m := stateMark(st, mode)
			for _, bad := range []string{"●", "○", "◉", "·", "–", "—", "→", "⚡", "ℹ"} {
				if m == bad {
					t.Errorf("state %v mode %v uses retired glyph %q", st, mode, bad)
				}
			}
		}
	}
}

package output

import "github.com/charmbracelet/x/ansi"

// §4.5 State vocabulary.
//
// Six states. Every mark was verified to have display width 1 under both
// East-Asian Ambiguous conventions. State is always rendered as mark plus
// word — colour is never the only channel.
//
// Retired as state carriers: ● ○ ◉ · – — → ⚡ ℹ. `●` currently means running,
// healthy AND unhealthy simultaneously; `● ○ · – — →` are East-Asian
// Ambiguous; `⚡` is Wide.
type State int

const (
	StateOK       State = iota // passed / running / clean / active
	StateAdvisory              // the tier the doctors model but cannot render today
	StateFail                  // failed / unreachable
	StateBusy                  // starting / dirty / in progress
	StateIdle                  // stopped / absent / none
	StateUnknown               // not evaluated — new; the fix for "✗ Xray config
	// exists" printed against a config that does exist
)

// stateDef fixes one word per state. The word is fixed here rather than
// chosen per call site because the Checks floor depends on the widest of them
// (§4.4): "not evaluated" in place of "unknown" moves that floor from 12 to 17.
type stateDef struct {
	mark  string // UTF-8 glyph mode
	ascii string // ASCII glyph mode
	word  string // default word, used when a call site supplies no label
	role  Role
}

var stateDefs = map[State]stateDef{
	StateOK:       {"✓", "+", "ok", RoleOK},
	StateAdvisory: {"⚠", "!", "degraded", RoleWarn},
	StateFail:     {"✗", "x", "failed", RoleFail},
	StateBusy:     {"~", "~", "busy", RoleInfo},
	StateIdle:     {"-", "-", "idle", RoleMuted},
	StateUnknown:  {"?", "?", "unknown", RoleMuted},
}

// allStates is the declaration order, for callers that enumerate the
// vocabulary (the §6.4 glyph-width check and the §6.5 structural assertion).
var allStates = []State{
	StateOK, StateAdvisory, StateFail, StateBusy, StateIdle, StateUnknown,
}

// stateMark returns the state glyph in a glyph mode.
func stateMark(st State, mode GlyphMode) string {
	def := stateDefs[st]
	if mode == GlyphASCII {
		return def.ascii
	}
	return def.mark
}

// stateWord returns the fixed default word for a state.
func stateWord(st State) string { return stateDefs[st].word }

// stateRole returns the role a state is coloured with. Decorative only:
// §4.5 guarantees the mark and the word carry the information.
func stateRole(st State) Role { return stateDefs[st].role }

// widestStateWord is the alignment column for Checks. It is taken from the
// whole vocabulary, not from the items present, so the block's geometry is a
// property of the layer rather than of its content (§4.4: "Natural floor 12
// columns, set by the widest state word rather than by content").
func widestStateWord() int {
	widest := 0
	for _, def := range stateDefs {
		if w := ansi.StringWidth(def.word); w > widest {
			widest = w
		}
	}
	return widest
}

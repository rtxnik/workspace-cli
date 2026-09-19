package output

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
	StateUnknown               // not evaluated — new; the fix for "✗ Xray config exists" printed against a config that does exist
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
		if w := W(def.word); w > widest {
			widest = w
		}
	}
	return widest
}

// stateText is the canonical "mark + word" rendering. An empty label falls
// back to the state's default word (§4.5): `ws list` renders `✓ running`,
// `- stopped`, `- not created`, not `✓ ok`.
func stateText(st State, label string, mode GlyphMode) string {
	if label == "" {
		label = stateWord(st)
	}
	return stateMark(st, mode) + " " + label
}

// widestStateMark is the mark column. Every mark is width 1 in both modes and
// under both Ambiguous conventions, but it is measured rather than assumed so
// that a future addition to the vocabulary cannot silently break alignment.
// Its caller is Checks.Render in blocks.go, which sizes the badge column from
// the vocabulary rather than from the items present.
func widestStateMark(mode GlyphMode) int {
	widest := 0
	for _, st := range allStates {
		if w := W(stateMark(st, mode)); w > widest {
			widest = w
		}
	}
	return widest
}

// StateMark exposes this stream's glyph for a state, for call sites that
// build their own one-off line rather than a block.
//
// Nothing inside this layer calls it, and that is §4.5's seam rather than an
// oversight: the first caller is phase 2's migration of the status commands.
// An exported identifier is invisible to the `unused` linter, so it would ship
// unexercised — TestStreamStateHelpers in acceptance_test.go is the one thing
// that runs it.
func (s *Stream) StateMark(st State) string { return stateMark(st, s.mode) }

// StateText renders "mark + label" for this stream, defaulting the label to
// the state's fixed word. It has no caller inside the layer either; see
// StateMark above.
func (s *Stream) StateText(st State, label string) string {
	return stateText(st, label, s.mode)
}

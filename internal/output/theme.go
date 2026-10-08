package output

import "github.com/charmbracelet/lipgloss"

// §4.6 Palette.
//
// Colour applies to roles only. Ordinary text and table bodies use the
// terminal's default foreground, which is what makes the light-background
// case correct by construction instead of by maintaining a second palette.
//
// This file is the ONLY place in the repository that may name a colour
// value. §4.6's guard holds it to that: anti_drift (antidrift_test.go) fails
// on a hex colour literal in any non-test file outside theme.go, and on a
// theme.go that names none, so that it cannot pass over a palette that has
// gone; TestAntiDriftGuardCanFail is its control. The literals are those of
// roleColours below, and this comment names the guard's pattern only in
// words, so that it is not counted as one.
//
// The guard owes more than a search for that one string, which is why the
// literals and the lipgloss.Color call are kept together here: building the
// literal somewhere else and passing it in is the same drift by a longer
// route.

// Role is the closed set of semantic roles the layer can colour.
type Role int

const (
	RoleDefault Role = iota // terminal default — no SGR is emitted at all
	RoleOK
	RoleWarn
	RoleFail
	RoleInfo
	RoleMuted
	RoleAccent
)

// ColourLevel is the resolved colour capability of a Stream.
//
// §4.6 requires each role to declare its value explicitly per level rather
// than relying on termenv's automatic downsample, which was measured to
// invert the palette at 16 colours (Green renders yellow, Blue renders green).
type ColourLevel int

const (
	ColourNone ColourLevel = iota // no SGR: NO_COLOR, a pipe, or a dumb terminal
	Colour16
	Colour256
	ColourTrue
)

// roleColours holds the gruvbox hue for each role at each colour level, each
// written out as the lipgloss.Color it is painted with. The colour-literal
// guard (antidrift_test.go) fails when this file names no hex colour literal
// at all, and these six are the only ones it names, so the guard looks at the
// palette the layer paints with. RoleDefault is deliberately absent: it never
// emits SGR.
var roleColours = map[Role]struct{ trueColour, c256, c16 lipgloss.Color }{
	RoleOK:     {lipgloss.Color("#b8bb26"), lipgloss.Color("142"), lipgloss.Color("2")},
	RoleWarn:   {lipgloss.Color("#fabd2f"), lipgloss.Color("214"), lipgloss.Color("3")},
	RoleFail:   {lipgloss.Color("#fb4934"), lipgloss.Color("167"), lipgloss.Color("1")},
	RoleInfo:   {lipgloss.Color("#83a598"), lipgloss.Color("109"), lipgloss.Color("4")},
	RoleMuted:  {lipgloss.Color("#928374"), lipgloss.Color("245"), lipgloss.Color("8")},
	RoleAccent: {lipgloss.Color("#d3869b"), lipgloss.Color("175"), lipgloss.Color("5")},
}

// colourFor returns the terminal colour for a role at a colour level, and
// false when the role must be rendered with no SGR at all.
func colourFor(role Role, level ColourLevel) (lipgloss.TerminalColor, bool) {
	if role == RoleDefault || level == ColourNone {
		return nil, false
	}
	entry, ok := roleColours[role]
	if !ok {
		return nil, false
	}
	switch level {
	case ColourTrue:
		return entry.trueColour, true
	case Colour256:
		return entry.c256, true
	case Colour16:
		return entry.c16, true
	}
	return nil, false
}

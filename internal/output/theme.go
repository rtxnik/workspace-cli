package output

import "github.com/charmbracelet/lipgloss"

// Gruvbox Dark palette.
var (
	BG0 = lipgloss.Color("#282828")
	BG1 = lipgloss.Color("#3c3836")
	BG2 = lipgloss.Color("#504945")

	FG1  = lipgloss.Color("#ebdbb2")
	FG2  = lipgloss.Color("#d5c4a1")
	FG4  = lipgloss.Color("#a89984")
	Gray = lipgloss.Color("#928374")

	Red    = lipgloss.Color("#fb4934")
	Green  = lipgloss.Color("#b8bb26")
	Yellow = lipgloss.Color("#fabd2f")
	Blue   = lipgloss.Color("#83a598")
	Purple = lipgloss.Color("#d3869b")
	Aqua   = lipgloss.Color("#8ec07c")
	Orange = lipgloss.Color("#fe8019")

	RedDim    = lipgloss.Color("#cc241d")
	GreenDim  = lipgloss.Color("#98971a")
	BlueDim   = lipgloss.Color("#458588")
	OrangeDim = lipgloss.Color("#d65d0e")
)

// Semantic styles.
var (
	StyleSuccess = lipgloss.NewStyle().Foreground(Green)
	StyleError   = lipgloss.NewStyle().Foreground(Red)
	StyleWarning = lipgloss.NewStyle().Foreground(Yellow)
	StyleInfo    = lipgloss.NewStyle().Foreground(Blue)
	StyleDim     = lipgloss.NewStyle().Foreground(Gray)
	StyleAccent  = lipgloss.NewStyle().Foreground(Orange)
	StyleAqua    = lipgloss.NewStyle().Foreground(Aqua)
	StyleHeader  = lipgloss.NewStyle().Bold(true).Foreground(Blue)
	StyleSection = lipgloss.NewStyle().Bold(true).Foreground(FG1)
)

// StatusIcon returns a colored icon for the given status.
func StatusIcon(status string) string {
	switch status {
	case "running":
		return StyleSuccess.Render("●")
	case "stopped", "notcreated", "":
		return StyleDim.Render("○")
	case "busy", "starting":
		return StyleWarning.Render("◉")
	case "healthy":
		return StyleSuccess.Render("●")
	case "unhealthy":
		return StyleError.Render("●")
	default:
		return StyleDim.Render("○")
	}
}

// StatusText returns a colored "icon Status" string for the given status.
func StatusText(status string) string {
	switch status {
	case "running":
		return StyleSuccess.Render("● Running")
	case "stopped":
		return StyleDim.Render("○ Stopped")
	case "notcreated", "":
		return StyleDim.Render("○ NotCreated")
	case "busy":
		return StyleWarning.Render("◉ Busy")
	case "starting":
		return StyleWarning.Render("◉ Starting")
	case "healthy":
		return StyleSuccess.Render("● Healthy")
	case "unhealthy":
		return StyleError.Render("● Unhealthy")
	default:
		return StyleDim.Render("○ " + status)
	}
}

// §4.6 Palette.
//
// Colour applies to roles only. Ordinary text and table bodies use the
// terminal's default foreground, which is what makes the light-background
// case correct by construction instead of by maintaining a second palette.
//
// This file is the ONLY place in the package that may name a colour value.
// §4.6 REQUIRES a guard asserting that `lipgloss.Color("#` appears in no
// non-test file outside theme.go. That guard is not in this repository: it
// lands with the acceptance harness, where the source-scanning machinery it
// needs also lands. Until then the property holds by convention and nothing
// goes red when it is broken, so a colour literal added elsewhere is caught
// by review or not at all.
//
// The guard will owe more than a search for that one string, which is why the
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

// roleColours holds the gruvbox hue for each role at each colour level.
// RoleDefault is deliberately absent: it never emits SGR.
var roleColours = map[Role]struct{ trueColour, c256, c16 string }{
	RoleOK:     {"#b8bb26", "142", "2"},
	RoleWarn:   {"#fabd2f", "214", "3"},
	RoleFail:   {"#fb4934", "167", "1"},
	RoleInfo:   {"#83a598", "109", "4"},
	RoleMuted:  {"#928374", "245", "8"},
	RoleAccent: {"#d3869b", "175", "5"},
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
		return lipgloss.Color(entry.trueColour), true
	case Colour256:
		return lipgloss.Color(entry.c256), true
	case Colour16:
		return lipgloss.Color(entry.c16), true
	}
	return nil, false
}

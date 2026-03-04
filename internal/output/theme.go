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

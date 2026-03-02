package output

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
)

var (
	green  = lipgloss.Color("#22c55e")
	red    = lipgloss.Color("#ef4444")
	yellow = lipgloss.Color("#eab308")
	blue   = lipgloss.Color("#3b82f6")
	dim    = lipgloss.Color("#6b7280")

	SectionStyle = lipgloss.NewStyle().Bold(true).Foreground(blue)
	successStyle = lipgloss.NewStyle().Foreground(green)
	errorStyle   = lipgloss.NewStyle().Foreground(red)
	warnStyle    = lipgloss.NewStyle().Foreground(yellow)
	infoStyle    = lipgloss.NewStyle().Foreground(blue)
	detailStyle  = lipgloss.NewStyle().Foreground(dim)
)

func Info(msg string) {
	fmt.Fprintln(os.Stderr, infoStyle.Render("ℹ "+msg))
}

func Success(msg string) {
	fmt.Fprintln(os.Stderr, successStyle.Render("✓ "+msg))
}

func Warn(msg string) {
	fmt.Fprintln(os.Stderr, warnStyle.Render("⚠ "+msg))
}

func Detail(msg string) {
	fmt.Fprintln(os.Stderr, detailStyle.Render("  "+msg))
}

func Die(msg string) {
	fmt.Fprintln(os.Stderr, errorStyle.Render("✗ "+msg))
	os.Exit(1)
}

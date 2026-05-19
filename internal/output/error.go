package output

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ErrorDetail holds structured error information with context and suggestions.
type ErrorDetail struct {
	Title       string
	Context     map[string]string
	Suggestions []string
}

// RenderError formats a structured error as a bordered box with context
// and numbered suggestions.
func RenderError(e ErrorDetail) string {
	var b strings.Builder

	b.WriteString(StyleError.Bold(true).Render(e.Title))
	b.WriteString("\n")

	if len(e.Context) > 0 {
		for k, v := range e.Context {
			fmt.Fprintf(&b, "  %s %s\n",
				StyleDim.Render(k+":"),
				lipgloss.NewStyle().Foreground(FG1).Render(v))
		}
	}

	if len(e.Suggestions) > 0 {
		b.WriteString("\n")
		for i, s := range e.Suggestions {
			fmt.Fprintf(&b, "  %s %s\n",
				StyleAqua.Render(fmt.Sprintf("%d.", i+1)),
				s)
		}
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(RedDim).
		Padding(0, 1).
		Render(strings.TrimRight(b.String(), "\n"))
}

package output

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// NewTable creates a gruvbox-styled table with the given headers.
func NewTable(headers []string) *table.Table {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(Blue).Padding(0, 1)
	cellStyle := lipgloss.NewStyle().Padding(0, 1)

	return table.New().
		Headers(headers...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			if row%2 == 0 {
				return cellStyle.Foreground(FG1)
			}
			return cellStyle.Foreground(FG2)
		})
}

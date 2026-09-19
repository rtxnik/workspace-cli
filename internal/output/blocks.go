package output

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	ltable "github.com/charmbracelet/lipgloss/table"
)

// §4.4 Block types.
//
// Commands build a typed block and one renderer turns it into a view. Every
// Render takes the Stream it is destined for, because the budget, the colour
// level and the glyph mode are properties of that stream and of nothing else
// (§4.1).

// Fact is an ordered key/value pair. The blocks that carry Facts hold them in
// a slice and not a map because RenderError in error.go ranges over a map
// today and Go randomises map iteration, so the same failure prints its
// context in a different order every run.
type Fact struct{ K, V string }

// Remedy is a next step: a short label and a copy-pasteable command.
//
// The type is named Remedy and not Step because steps.go already declares
// `type Step struct{ Name string; Fn func() error }`, consumed by
// NewStepRunner at five call sites. This package gains the block types while
// StepRunner is still live, so a second Step would be an immediate compile
// failure; the existing type keeps its name until the spinner migration
// retires it.
type Remedy struct {
	Label string
	Cmd   string
}

// Table is a bordered grid whose columns are allocated from the stream's
// budget (§4.3). It is the answer to the command that built it, so it and its
// caption go to Out() (§4.7).
type Table struct {
	Cols    []Col
	Rows    [][]Cell
	Caption string // the renderer appends the "Hidden: …" clause

	// WideFlag is the flag that restores dropped columns, named in the
	// clause. EMPTY unless that flag actually exists on the command — the
	// caption must never advertise a flag the CLI does not have.
	WideFlag string
}

// NewTableBlock validates a Col set and returns the Table built from it.
//
// §4.3 rejects at construction a Col set whose forced chrome plus its
// un-droppable Min widths cannot fit MinWidth, so that case is reachable only
// through a programming error. The constructor is not named NewTable because
// table.go already exports `NewTable(headers []string) *table.Table`, live at
// six call sites under cmd/ until the table migration retires it.
func NewTableBlock(cols []Col, rows [][]Cell) (Table, error) {
	if err := validateCols(cols); err != nil {
		return Table{}, err
	}
	return Table{Cols: cols, Rows: rows}, nil
}

// ------------------------------------------------------------------- Table

// Render lays the table out against the stream's budget and returns it with
// its caption attached. A caption belongs to its table and goes to the same
// stream, always — today `ws list` splits them.
func (t Table) Render(s *Stream) string {
	budget := s.budget()
	a := Allocate(t.Cols, t.Rows, budget, s.mode)

	var out string
	if len(a.Kept) > 0 {
		out = t.renderGrid(s, a)
	}
	// Termination at n = 0 (§4.3): the table renders as its caption alone
	// with no box, because the chrome formula 3(n−1)+4 is undefined there.

	if caption := t.captionText(a); caption != "" {
		width := budget + mutants.CaptionWidth
		for _, line := range Wrap(caption, width) {
			if out != "" {
				out += "\n"
			}
			out += s.paint(RoleMuted, line)
		}
	}
	return out
}

// renderGrid draws the bordered grid at exactly the widths the allocator
// chose. Nothing is handed to lipgloss as a target width: passing the
// computed total as `.Width(total)` makes lipgloss re-fit the grid to
// whatever number it is given, absorbing an arithmetic error as silent
// content loss instead of overflow. §6.8's source-level scan holds the call
// to the one guarded occurrence below, because a call that looks harmless is
// how a toothless gate gets reintroduced.
func (t Table) renderGrid(s *Stream, a Alloc) string {
	headers := make([]string, 0, len(a.Kept))
	for _, i := range a.Kept {
		// title(), not the raw field: the heading the renderer draws is the
		// heading the allocator measured and the caption discloses (D-13).
		// Without the clip, a title longer than its allocation overflows the
		// column the allocator sized for the cells.
		headers = append(headers, Pad(clipTail(t.Cols[i].title(), a.Widths[i], s.mode), a.Widths[i]))
	}

	grid := ltable.New().
		Border(border(s.mode)). // §4.5: box drawing degrades with the glyph mode
		BorderStyle(s.Style(RoleDefault)).
		Headers(headers...).
		StyleFunc(func(row, col int) lipgloss.Style {
			// §4.6: colour applies to roles only, and the header row has no
			// role, so it carries no SGR of its own. The one cell of padding
			// on each side is the 2n half of the 3(n−1)+4 chrome.
			return s.Style(RoleDefault).Padding(0, 1)
		})
	if mutants.PinTableWidth {
		grid = grid.Width(a.Total)
	}

	for _, row := range t.Rows {
		cells := make([]string, 0, len(a.Kept))
		for _, i := range a.Kept {
			var cell Cell
			if i < len(row) {
				cell = row[i]
			}
			v := truncate(t.Cols[i].Trunc, cell.display(s.mode), a.Widths[i], s.mode)
			if t.Cols[i].Right {
				v = PadLeft(v, a.Widths[i])
			} else {
				v = Pad(v, a.Widths[i])
			}
			if mutants.PaintBeforeFit {
				// The defect, expressed exactly: paint the cell text, THEN fit
				// the painted string. Every width assertion in the suite is
				// blind to it, because ansi.Strip and ansi.StringWidth both
				// ignore SGR; TestStyleIsAppliedAfterAllocation reads where the
				// escapes fall instead.
				v = truncate(t.Cols[i].Trunc, s.paint(cell.Role, cell.display(s.mode)), a.Widths[i], s.mode)
				if t.Cols[i].Right {
					v = PadLeft(v, a.Widths[i])
				} else {
					v = Pad(v, a.Widths[i])
				}
				cells = append(cells, v)
				continue
			}
			// Style is applied to the padded result, never placed into the
			// cell before measurement (§4.3).
			cells = append(cells, s.paint(cell.Role, v))
		}
		grid.Row(cells...)
	}
	return grid.Render()
}

// captionText appends what the allocator actually did to the caller's
// caption, so the clause cannot go stale: a column relaxed under step 5(b) or
// dropped is named here (§4.3).
//
// The caller's caption is sanitised with Sanitise rather than SanitiseInline
// (D-13), because the caption is wrapped and a newline in it is a paragraph
// break the caller may legitimately want. The Dropped and Relaxed titles
// arrive already sanitised from Col.title(). WideFlag is a flag name this
// layer's own caller writes rather than upstream text, and is left alone.
func (t Table) captionText(a Alloc) string {
	if mutants.NoCaptionDisclosure {
		return Sanitise(t.Caption)
	}
	caption := Sanitise(t.Caption)
	add := func(clause string) {
		if caption != "" {
			caption += ". "
		}
		caption += clause
	}
	if len(a.Dropped) > 0 {
		clause := "Hidden: " + strings.Join(a.Dropped, ", ")
		if t.WideFlag != "" {
			clause += " (" + t.WideFlag + ")"
		}
		if mutants.HardcodedWideFlag {
			clause = "Hidden: " + strings.Join(a.Dropped, ", ") + " (--wide)"
		}
		add(clause)
	}
	if len(a.Relaxed) > 0 {
		add("Narrowed: " + strings.Join(a.Relaxed, ", "))
	}
	return caption
}

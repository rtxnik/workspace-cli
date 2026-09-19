package output

import (
	"fmt"
	"strconv"
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

// Problem is a rendered failure: an indented text block, not a box. It is
// about producing the answer rather than the answer itself, so it goes to
// Err() (§4.7).
type Problem struct {
	Title string   // wrapped with a hanging indent
	Cause string   // upstream text; sanitised, wrapped, hard-broken if unbreakable
	Facts []Fact   // ordered
	Steps []Remedy // numbered, copy-pasteable
}

// Empty is the one shape for "nothing here", always naming the next step
// (§4.9). It exits 0 and goes to stdout: "no workspaces yet" IS the answer to
// `ws list` (§4.7).
type Empty struct {
	Subject string
	Steps   []Remedy
}

// KV is a titled list of ordered pairs. A report body: Out() (§4.7).
type KV struct {
	Title string
	Pairs []Fact
}

// Check is one line of a Checks block. It carries no word of its own: the
// word comes from its State (§4.5). That is load-bearing for the block's
// floor, which is set by the widest word in the vocabulary rather than by
// content.
type Check struct {
	Name  string
	State State
	Note  string
}

// Checks is a titled list of state lines. A report body: Out() (§4.7).
type Checks struct {
	Title string
	Items []Check
}

// D-13, as the blocks below apply it: a caller's string is read exactly ONCE,
// into a local, through Sanitise, and everything downstream uses that local.
//
// §4.8 captures a child process's stderr into these blocks, and that text
// routinely carries cursor moves, line clears, OSC hyperlinks and title
// rewrites. ansi.StringWidth counts every one of them as zero-width, so the
// width sweep passes while the operator's terminal is being rewritten — the
// layer would be laundering arbitrary control sequences from a container into
// the operator's session. Raw bytes survive only in --json output, where they
// are data rather than instructions.
//
// Sanitise rather than SanitiseInline, because these surfaces are wrapped and
// a newline is a paragraph break the caller may legitimately want. The one
// exception is the Fact key on the ALIGNED path, which is padded rather than
// wrapped; a newline there would break its column, and §4.4 does not
// contemplate a multi-line key.
//
// Where a surface is MEASURED in one pass and RENDERED in another — the Fact
// key and the Remedy label both are — the same sanitised string serves both
// passes, so the column cannot be sized from one string and filled with
// another. The own-line predicate in renderRemedies is the same shape: it
// reads the sanitised width.
//
// That half is a GUARD, not a live fix, and the difference was measured rather
// than assumed. W measures with ansi.StringWidth, which already counts CSI,
// OSC and C0 as zero cells: on §6.7's escape-laden fixture W(raw) and W(Sanitise(raw)) are
// both 70, and on "ab\x01\x02cd\x07ef" both 6. Sizing the key column from the
// RAW string instead (planted, one match) leaves the whole package green. What
// sanitising actually buys is the CONTENT half above — the sequences never
// reach the terminal — and that half is covered: removing Sanitise from
// Problem.Cause reddens TestProblemCauseIsSanitised. The other ten now have
// escape-bearing fixtures of their own in corpus_test.go — problem, empty, kv
// and checks /esc-surfaces — and assertESCContainment is their detector:
// deleting the Sanitise from any one of the call sites below reddens it,
// measured 12 violations each and 24 where renderPairs or renderRemedies
// covers two surfaces at once. Counted at this append: 11 caller surfaces read through
// Sanitise across 13 call sites, the Fact key and the Remedy label being read
// once in their width pass and once in their render pass.

// ----------------------------------------------------------------- Problem

// Render draws a Problem: no right-hand border, and no top or bottom rule
// either. A border around unbounded upstream text — a Docker daemon sentence,
// xray's multi-line `-test` output — is precisely what turns overflow into
// shredded output; the audit measured such blocks at 90 to 227 columns.
//
// The declared order is Title, Cause, Facts, Steps, and nothing else is
// written. TestProblemGeometry asserts it by locating each section's first
// line by content rather than by counting lines, because the reference
// module's entire suite stayed green when its Facts and Steps were swapped.
// Planted here (one match, restored): swapping the two blocks below reddens
// that assertion with `@29 §4.4 orders Title, Cause, Facts, Steps; got title=0
// cause=2 facts=13 steps=7`.
func (p Problem) Render(s *Stream) string {
	budget := s.budget()
	var b strings.Builder

	// §4.4: title at column 0, continuation lines hanging-indented by 2.
	//
	// The WHOLE title is wrapped at budget-hang, because that is the width
	// every line after the first will actually occupy, and the first line is
	// then rendered unindented at that same width. Clipping the continuation
	// instead — which is what the committed reference module does — would lose
	// characters to a wrapping decision rather than to an unbreakable run, and
	// that is not what §4.4's truncate-over-hard-break precedence is about.
	// Wrapping at budget and RE-wrapping the remainder would be one cell wider
	// on line 1 and is rejected: rejoining wrapped lines inserts a space, which
	// a hard-broken unbreakable token must not acquire.
	//
	// budget() clamps to MinWidth, so budget-hang is at least MinWidth-hang and
	// Wrap is never handed a degenerate width. A single unbreakable token wider
	// than budget-hang is still hard-broken by Wrap itself, which is §4.4's
	// unconditional half and text.go's behaviour rather than this block's.
	//
	// The cost is one cell of the first line at every width; the gain is that
	// no title loses a character at any width, so the title can carry a
	// content-fidelity assertion.
	const hang = 2
	for i, line := range Wrap(Sanitise(p.Title), budget-hang) {
		if i == 0 {
			b.WriteString(s.paint(RoleFail, line) + "\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", hang) + s.paint(RoleFail, line) + "\n")
	}

	if p.Cause != "" {
		// Sanitised BEFORE wrapping, not after and not at all (§4.4).
		//
		// Removing Sanitise here reddens TestProblemCauseIsSanitised. Moving it
		// to AFTER the wrap does not redden anything: planted, the package
		// stays green, while the rendered Cause changes at all 172 swept widths
		// — an OSC-0 token collapses to an empty word and leaves a double space
		// behind ("the registry log  giving up" at budget 29). The width sweep
		// cannot see that, because ansi.StringWidth already counts the
		// sequences as zero, and the survivor assertion compares through
		// squash, which ignores whitespace. So the ORDER is an unguarded
		// invariant of this line and is stated here for the reader.
		for _, line := range wrapIndent(Sanitise(p.Cause), 2, budget) {
			b.WriteString(s.paint(RoleMuted, line) + "\n")
		}
	}
	if len(p.Facts) > 0 {
		b.WriteString("\n")
		b.WriteString(renderPairs(s, p.Facts, budget))
	}
	if len(p.Steps) > 0 {
		b.WriteString("\n")
		b.WriteString(renderRemedies(s, p.Steps, budget, true))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderPairs is the shared label/value column used by Problem.Facts and KV:
// 2sp + key padded to the widest key + 2sp + value, the value wrapped at a
// hanging indent aligned to the value column (§4.4).
func renderPairs(s *Stream, pairs []Fact, budget int) string {
	const indent, gap = 2, 2
	keyWidth := 0
	for _, f := range pairs {
		// The width pass and the render pass below both read Sanitise(f.K), so
		// the column cannot be sized from one string and filled with another.
		if w := W(Sanitise(f.K)); w > keyWidth {
			keyWidth = w
		}
	}
	valueIndent := indent + keyWidth + gap
	// §4.4: when budget − valueIndent < 12 the pair stacks — key on its own
	// line, value indented beneath. The 12 is a readability threshold for the
	// value column, and is NOT the Checks floor of the same size, which is set
	// by the widest word in §4.5's vocabulary.
	stacked := budget-valueIndent < 12

	var b strings.Builder
	for _, f := range pairs {
		key, value := Sanitise(f.K), Sanitise(f.V)
		if stacked {
			for _, line := range wrapIndent(key, indent, budget) {
				b.WriteString(s.paint(RoleMuted, line) + "\n")
			}
			for _, line := range wrapIndent(value, indent+2, budget) {
				b.WriteString(line + "\n")
			}
			continue
		}
		valueWidth := budget - valueIndent
		if mutants.PairsIndent {
			// The defect: the value is wrapped at the FULL budget while it is
			// printed at valueIndent, so every line past the first overflows
			// by the width of the key column.
			valueWidth = budget
		}
		lines := Wrap(value, valueWidth)
		b.WriteString(strings.Repeat(" ", indent) +
			s.paint(RoleMuted, Pad(key, keyWidth)) +
			strings.Repeat(" ", gap) + lines[0] + "\n")
		for _, line := range lines[1:] {
			b.WriteString(strings.Repeat(" ", valueIndent) + line + "\n")
		}
	}
	return b.String()
}

// renderRemedies renders "label  command": numbered for Problem
// (2sp + "N." + sp + label padded + 2sp + command), bare for Empty.
//
// §4.4: when a command will not fit its aligned slot it drops to its own line
// rather than being truncated. That buys copy-pasteability only so far. On its
// own line the command has budget − (indent + numberWidth) cells, so a command
// of C cells wraps below C + 5 columns for a single-digit list, and wrapping
// destroys copy-paste exactly as truncation would. The width contract wins:
// below the fold the command wraps. Commands should therefore be kept short,
// with a long remedy expressed as a short command plus prose in Problem.Cause.
func renderRemedies(s *Stream, steps []Remedy, budget int, numbered bool) string {
	const indent, gap = 2, 2
	labelWidth := 0
	for _, st := range steps {
		// Sanitised in the width pass as well as the render pass below.
		if w := W(Sanitise(st.Label)); w > labelWidth {
			labelWidth = w
		}
	}
	numberWidth := 0
	if numbered {
		numberWidth = W(strconv.Itoa(len(steps))) + 2 // "N." plus one space
	}
	cmdIndent := indent + numberWidth + labelWidth + gap

	var b strings.Builder
	for i, st := range steps {
		label, cmd := Sanitise(st.Label), Sanitise(st.Cmd)
		prefix := strings.Repeat(" ", indent)
		if numbered {
			prefix += Pad(fmt.Sprintf("%d.", i+1), numberWidth)
		}
		ownLine := budget-cmdIndent < 8 || W(cmd) > budget-cmdIndent
		if ownLine {
			for j, line := range Wrap(label, budget-indent-numberWidth) {
				if j == 0 {
					b.WriteString(prefix + line + "\n")
				} else {
					b.WriteString(strings.Repeat(" ", indent+numberWidth) + line + "\n")
				}
			}
			// Wrapped, not truncated: §4.4's precedence rule says the width
			// contract wins, and a hard break beats silently losing the tail of
			// a command the operator is meant to paste.
			for _, line := range wrapIndent(cmd, indent+numberWidth, budget) {
				b.WriteString(s.paint(RoleAccent, line) + "\n")
			}
			continue
		}
		b.WriteString(prefix + Pad(label, labelWidth) + strings.Repeat(" ", gap) +
			s.paint(RoleAccent, cmd) + "\n")
	}
	return b.String()
}

// ------------------------------------------------------------------- Empty

// Render draws §4.9's one shape for an empty result:
//
//	No workspaces yet.
//	  Create one    ws new <name>
//	  See profiles  ws profiles
//
// The remedies are NOT numbered. The shape differs from Problem's on purpose:
// an empty result is not a list of ordered recovery steps.
func (e Empty) Render(s *Stream) string {
	budget := s.budget()
	var b strings.Builder
	for _, line := range Wrap("No "+Sanitise(e.Subject)+" yet.", budget) {
		b.WriteString(line + "\n")
	}
	b.WriteString(renderRemedies(s, e.Steps, budget, false))
	return strings.TrimRight(b.String(), "\n")
}

// ---------------------------------------------------------------------- KV

// Render draws the title, then the pairs through the same column renderPairs
// gives Problem.Facts.
func (k KV) Render(s *Stream) string {
	budget := s.budget()
	var b strings.Builder
	if k.Title != "" {
		for _, line := range Wrap(Sanitise(k.Title), budget) {
			b.WriteString(s.paint(RoleAccent, line) + "\n")
		}
	}
	b.WriteString(renderPairs(s, k.Pairs, budget))
	return strings.TrimRight(b.String(), "\n")
}

// ------------------------------------------------------------------ Checks

// Render draws the title, then one line per item — 2sp + mark + sp + word,
// then the name, then the note wrapped at a hanging indent (§4.4).
//
// The badge column is sized from the whole vocabulary through widestStateMark
// and widestStateWord, not from c.Items, so the block's geometry is a property
// of the layer rather than of its content: the natural floor of 12 columns is
// 2 + 1 + 1 + 8, set by the widest state word (`degraded`). A content-sized
// column would re-align between two runs of the same command — `ws proxy
// doctor` would indent its names at column 8 with everything passing and at
// column 14 with one check degraded.
func (c Checks) Render(s *Stream) string {
	budget := s.budget()
	var b strings.Builder
	if c.Title != "" {
		for _, line := range Wrap(Sanitise(c.Title), budget) {
			b.WriteString(s.paint(RoleAccent, line) + "\n")
		}
	}

	const indent, gap = 2, 2
	markWidth := widestStateMark(s.mode)
	wordWidth := widestStateWord()
	nameIndent := indent + markWidth + 1 + wordWidth + gap

	for _, item := range c.Items {
		badge := s.paint(stateRole(item.State),
			Pad(stateText(item.State, "", s.mode), markWidth+1+wordWidth))
		lines := Wrap(Sanitise(item.Name), budget-nameIndent)
		b.WriteString(strings.Repeat(" ", indent) + badge +
			strings.Repeat(" ", gap) + lines[0] + "\n")
		for _, line := range lines[1:] {
			b.WriteString(strings.Repeat(" ", nameIndent) + line + "\n")
		}
		if item.Note != "" {
			for _, line := range wrapIndent(Sanitise(item.Note), nameIndent, budget) {
				b.WriteString(s.paint(RoleMuted, line) + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

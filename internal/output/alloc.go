package output

import (
	"fmt"
	"sort"
)

// §4.3 The column allocator.
//
// Terminal width is an explicit resource. A column spec carries a priority
// and a floor; the allocator distributes the budget across them. The width
// contract of §6.1 outranks every other invariant in this file: no render may
// exceed its budget, and step 5 is the guarantee that a legal move always
// exists.

// ColKind lets the allocator recognise a state column. A rule the type system
// cannot express is a rule two implementers will implement differently
// (§4.3): shaving a state column below `mark + space + shortest word` leaves a
// bare mark, which re-creates exactly the ambiguity §4.5 exists to remove.
type ColKind int

const (
	ColPlain ColKind = iota
	ColState
)

// Col is one column of a table.
type Col struct {
	Title  string
	Prio   int // 1 = never dropped by step 2; higher numbers are dropped first
	Min    int // display columns below which this column carries no information
	Trunc  Trunc
	Right  bool // right-align (counts, deltas)
	Atomic bool // truncating destroys the meaning → drop instead of squeezing
	Kind   ColKind
}

// title is the column heading as the layer draws it. Every reader of Col.Title
// goes through here — naturalWidths below, the renderer, and the
// Dropped/Relaxed clause — so measurement, render and caption cannot disagree
// about what the heading is (D-13).
func (c Col) title() string { return SanitiseInline(c.Title) }

// Cell is plain text plus a semantic role. Style is applied after allocation,
// so the allocator always measures unstyled text (§4.3, §4.4).
//
// A state cell additionally carries its State rather than a pre-rendered
// glyph: the mark depends on the Stream's glyph mode (§4.5), which is not
// known at construction time, and §4.1 makes the Stream the owner of that
// property.
type Cell struct {
	Text string // plain text; for a state cell, the domain label (may be empty)
	Role Role

	state   State
	isState bool
}

// Text builds a plain cell with RoleDefault (§4.4).
func Text(s string) Cell { return Cell{Text: s} }

// Mark builds a state cell rendering as "<mark> <label>"; an empty label uses
// the state's default word (§4.4, §4.5). This is the only API by which a table
// cell carries state, so `ws list` renders `✓ running`, `- stopped`,
// `- not created` rather than the literal strings a call site would otherwise
// type by hand.
func Mark(st State, label string) Cell {
	return Cell{Text: label, Role: stateRole(st), state: st, isState: true}
}

// display is the text this cell renders: the state vocabulary for a state
// cell, the caller's text otherwise — sanitised, because §4.8 fills cells from
// captured upstream output and CSI/OSC measure zero cells (D-13). The
// allocator measures this string and the renderer draws it; sanitising in only
// one of the two places would make the width the allocator computed disagree
// with the string the renderer produced.
func (c Cell) display(mode GlyphMode) string {
	if c.isState {
		return stateText(c.state, SanitiseInline(c.Text), mode)
	}
	return SanitiseInline(c.Text)
}

// Alloc is the allocator's decision for one render. The renderer must produce
// a grid exactly Total columns wide with each kept column exactly Widths[i]
// wide; §6.1's paired assertion reads these fields back out of the render,
// because lipgloss absorbs an arithmetic error as silent content loss rather
// than as overflow.
type Alloc struct {
	Natural []int    // natural width of every column, dropped ones included
	Widths  []int    // allocated width per column; -1 means dropped
	Kept    []int    // indices of kept columns, left to right
	Dropped []string // titles of dropped columns, in column order
	Relaxed []string // titles of columns taken below their Min by step 5(b)
	Chrome  int      // 3(n-1)+4 for n kept columns; 0 at n = 0
	Total   int      // Chrome + Σ Widths[kept]; never greater than the budget
}

// chromeFor is the border and padding cost of n columns: 3(n−1)+4, rearranged.
// It is undefined at n = 0, where the table renders as its caption alone with
// no box — the guard is not defensive, because relax's step (c) can
// legitimately reach it.
func chromeFor(n int) int {
	if n <= 0 {
		return 0
	}
	return 3*n + 1 - mutants.ChromeOff
}

// naturalWidths is the width each column would take if nothing were scarce:
// the widest of its title and its cells.
func naturalWidths(cols []Col, rows [][]Cell, mode GlyphMode) []int {
	natural := make([]int, len(cols))
	for i, c := range cols {
		natural[i] = W(c.title())
		for _, row := range rows {
			if i < len(row) {
				if w := W(row[i].display(mode)); w > natural[i] {
					natural[i] = w
				}
			}
		}
	}
	return natural
}

// Allocate distributes budget across cols, following §4.3 in order.
//
// The glyph mode is a parameter because step 5(b)'s floor is the truncation
// marker's width in the active mode — 3 cells in ASCII — and because a state
// cell's natural width depends on which mark it renders.
func Allocate(cols []Col, rows [][]Cell, budget int, mode GlyphMode) Alloc {
	// §4.2: below MinWidth the allocator clamps and lets the terminal wrap.
	// The clamp lives here, not only in the Stream constructor, so every path
	// into the allocator gets it.
	budget = clampBudget(budget)

	n := len(cols)
	natural := naturalWidths(cols, rows, mode)
	kept := make([]bool, n)
	for i := range kept {
		kept[i] = true
	}

	countKept := func() int {
		c := 0
		for _, k := range kept {
			if k {
				c++
			}
		}
		return c
	}
	// floorOf is the narrowest this column can become without entering step 5.
	// An Atomic column cannot be squeezed at all, so its floor is its natural
	// width.
	floorOf := func(i int) int {
		if cols[i].Atomic {
			return natural[i]
		}
		if cols[i].Min < natural[i] {
			return cols[i].Min
		}
		return natural[i]
	}
	sumKept := func(f func(int) int) int {
		s := 0
		for i := range cols {
			if kept[i] {
				s += f(i)
			}
		}
		return s
	}
	naturalOf := func(i int) int { return natural[i] }

	// 1. Natural fit. If the natural widths plus chrome fit, render at them:
	//    the table does not stretch to fill.
	fitsNatural := sumKept(naturalOf)+chromeFor(countKept()) <= budget

	// 2. Drop. Dropping is the LAST resort before shrinking, not the first:
	//    the test is against the remaining columns' Min widths, so a column is
	//    dropped only when shrinking every non-atomic column to its Min still
	//    would not fit. Wording this against the natural sum instead was
	//    implemented and measured, and it sheds whole columns rather than
	//    shaving cells off one: over the allocator fixtures of
	//    alloc_policy_test.go at widths 29–200 in both glyph modes, 118
	//    column-drops become 500.
	//
	//    `ws profiles` is where the loss shows. Under the shipped wording
	//    something shrinks at every width from 30 to 126; under the
	//    natural-sum wording nothing shrinks at any width in 29–200, because
	//    a column always goes first. Measured on that table:
	//
	//	@30  shipped [7 -1 16] total 30 | natural-sum [7 -1 -1] total 11
	//	@80  shipped [7 28 35] total 80 | natural-sum [7 -1 61] total 75
	//
	//    At 30 the natural-sum wording leaves a bare list of names; at 80 it
	//    hides BASE IMAGE at a width where the whole table fits.
	//
	//    The predicate is the Min sum, never the natural sum, and it is a
	//    function value rather than a constant so that mutants.go's
	//    DropAgainstNatural plants the rejected wording exactly: the
	//    measurement above is then a property the suite re-derives rather than
	//    a remembered result.
	dropTest := floorOf
	if mutants.DropAgainstNatural {
		dropTest = naturalOf
	}
	if !fitsNatural {
		for sumKept(dropTest)+chromeFor(countKept()) > budget {
			drop, prio := -1, 1
			for i := range cols {
				// Highest Prio wins; among equal Prio the rightmost drops
				// first, which `>=` over ascending i gives.
				if kept[i] && cols[i].Prio > 1 && cols[i].Prio >= prio {
					drop, prio = i, cols[i].Prio
				}
			}
			if drop < 0 {
				break // only Prio ≤ 1 left: stop dropping, continue to step 3
			}
			kept[drop] = false
		}
	}

	a := Alloc{Natural: natural, Widths: make([]int, n)}
	for i := range cols {
		if kept[i] {
			a.Widths[i] = natural[i]
			a.Kept = append(a.Kept, i)
		} else {
			a.Widths[i] = -1
		}
	}
	a.Chrome = chromeFor(len(a.Kept))
	total := a.Chrome + sumKept(func(i int) int { return a.Widths[i] })

	// 3. Shrink. Distribute the deficit across the remaining non-atomic
	//    columns, proportionally to their slack above Min, never below Min.
	if total > budget {
		shrinkToFit(&a, cols, total-budget)
		total = a.Chrome + sumKept(func(i int) int { return a.Widths[i] })
	}

	// 5. Deadlock. The trigger is the allocated total after step 3 still
	//    exceeding the budget — not merely Σ Min + chrome > budget, which has
	//    a hole: a kept Atomic column whose natural width exceeds its Min
	//    cannot be shrunk by step 3, so a table where Σ Min + chrome fits but
	//    the atomic column at natural width does not would reach step 4 with a
	//    deficit no step is allowed to absorb.
	if total > budget {
		total = relax(&a, cols, budget, total, mode)
	}

	// Report dropped columns in column order, whichever step dropped them, so
	// the caption clause is deterministic.
	for i := range cols {
		if a.Widths[i] < 0 {
			a.Dropped = append(a.Dropped, cols[i].title())
		}
	}

	// §4.3's two disclosure lists are disjoint. Step 5(b) narrows a column and
	// records it; step 5(c) may then drop that same column. A caption naming
	// it under both "Narrowed:" and "Hidden:" is not two facts, it is one fact
	// stated twice and contradicted once — and the hiding is the one the
	// operator can act on.
	//
	// The grid arithmetic is untouched here: Widths, Kept, Chrome and Total
	// are neither read nor written, so no width-shaped figure moves. Only the
	// caption text does, on the shapes that reach the overlap.
	if len(a.Relaxed) > 0 && len(a.Dropped) > 0 {
		dropped := make(map[string]bool, len(a.Dropped))
		for _, title := range a.Dropped {
			dropped[title] = true
		}
		survivors := a.Relaxed[:0]
		for _, title := range a.Relaxed {
			if !dropped[title] {
				survivors = append(survivors, title)
			}
		}
		a.Relaxed = survivors
	}

	a.Total = total
	return a
}

// shrinkToFit is §4.3 step 3. Apportionment is largest-remainder with each
// part capped at that column's slack, so the parts sum to the deficit exactly:
// flooring everywhere under-shrinks and still overflows, ceiling everywhere
// breaches Min. Ties in the largest-remainder step are broken leftmost-first,
// so the layout is deterministic; without a stated tie-break two
// implementations shrink different columns and §6.1 cannot adjudicate.
func shrinkToFit(a *Alloc, cols []Col, deficit int) {
	type share struct{ index, slack int }
	var shares []share
	totalSlack := 0
	for _, i := range a.Kept {
		if cols[i].Atomic {
			continue // truncating an atomic column destroys its meaning
		}
		if slack := a.Widths[i] - cols[i].Min; slack > 0 {
			shares = append(shares, share{i, slack})
			totalSlack += slack
		}
	}
	if totalSlack == 0 {
		return
	}
	take := deficit
	if take > totalSlack {
		take = totalSlack
	}

	type part struct{ index, base, remainder int }
	parts := make([]part, 0, len(shares))
	assigned := 0
	for _, s := range shares {
		numerator := take * s.slack
		base := numerator / totalSlack
		parts = append(parts, part{s.index, base, numerator % totalSlack})
		assigned += base
	}
	// parts is in left-to-right order and the sort is stable, so equal
	// remainders keep that order: leftmost-first.
	//
	// The sort call does NOT carry that guarantee at the sizes a CLI table
	// reaches — Go's pdqsort falls back to insertion sort below 12 elements
	// and detects an already-ordered run above it, so sort.Slice returns the
	// same vector on every shape the suite exercises. The slice being BUILT in
	// column order is what carries it, and only a hand-computed golden can
	// adjudicate that; alloc_policy_test.go has one.
	sort.SliceStable(parts, func(x, y int) bool {
		return parts[x].remainder > parts[y].remainder
	})
	for k := 0; k < len(parts) && assigned < take; k++ {
		parts[k].base++
		assigned++
	}
	for _, p := range parts {
		a.Widths[p.index] -= p.base
	}
}

// relax is §4.3 step 5. The invariants above are relaxed in this fixed order,
// each only as far as needed, and it always terminates: (c) can drop every
// column, after which the table renders as its caption alone with no box.
func relax(a *Alloc, cols []Col, budget, total int, mode GlyphMode) int {
	// (a) Squeeze Atomic columns down to their Min, left to right.
	squeezeAtomics := func() {
		if mutants.NoAtomicSqueeze {
			return
		}
		for _, i := range a.Kept {
			if total <= budget {
				return
			}
			if !cols[i].Atomic {
				continue
			}
			for a.Widths[i] > cols[i].Min && a.Widths[i] > 1 && total > budget {
				a.Widths[i]--
				total--
			}
		}
	}

	// (b) Shrink the widest kept column below its Min, to a floor of
	//     max(1, markerWidth(mode)). The floor is the truncation marker's
	//     width in the active glyph mode, not 1: in ASCII mode the marker is
	//     "..." (3 cells), so a column allocated 1 or 2 cells could not place
	//     it and would either overflow or silently clip.
	//
	//     A state column is EXEMPT and is dropped under (c) instead.
	shrinkBelowMin := func() {
		floor := markerWidth(mode)
		if mutants.RelaxFloorOne {
			floor = 1
		}
		if floor < 1 {
			floor = 1
		}
		relaxed := make(map[int]bool)
		for total > budget {
			widest, widestW := -1, floor
			for _, i := range a.Kept {
				if cols[i].Kind == ColState && !mutants.NoStateExemption {
					continue
				}
				if a.Widths[i] > widestW {
					widest, widestW = i, a.Widths[i]
				}
			}
			if widest < 0 {
				break // nothing left that (b) may touch
			}
			a.Widths[widest]--
			relaxed[widest] = true
			total--
		}
		for i := range cols {
			if relaxed[i] {
				a.Relaxed = append(a.Relaxed, cols[i].title())
			}
		}
	}

	// The order is fixed by §4.3 and is itself load-bearing: squeezing the
	// atomics first is what keeps a column from going below its Min while
	// slack the allocator is allowed to take still exists elsewhere.
	if mutants.RelaxOrderSwapped {
		shrinkBelowMin()
		squeezeAtomics()
	} else {
		squeezeAtomics()
		shrinkBelowMin()
	}

	// (c) Drop kept columns right-to-left, Prio 1 included, until the row
	//     fits. Termination at n = 0 is legal: chrome is undefined there and
	//     the table degrades to its caption.
	for total > budget && len(a.Kept) > 0 {
		last := a.Kept[len(a.Kept)-1]
		a.Kept = a.Kept[:len(a.Kept)-1]
		a.Widths[last] = -1
		a.Chrome = chromeFor(len(a.Kept))
		total = a.Chrome
		for _, i := range a.Kept {
			total += a.Widths[i]
		}
	}
	return total
}

// validateCols implements §4.3's construction-time rejection: a Col set whose
// forced chrome plus its un-droppable Min widths cannot fit MinWidth is
// rejected rather than rendered, so that case is reachable only through a
// programming error.
//
// The error strings name the caller's own Col.Title rather than the sanitised
// heading: an error naming the column the caller declared is more useful than
// one naming a scrubbed version of it, and it never reaches a terminal through
// this layer.
func validateCols(cols []Col) error {
	forced, count := 0, 0
	for i, c := range cols {
		if c.Min < 1 {
			return fmt.Errorf("output: column %d (%q) has Min %d; Min must be at least 1", i, c.Title, c.Min)
		}
		if c.Prio <= 1 {
			forced += c.Min
			count++
		}
	}
	if count > 0 {
		if need := forced + chromeFor(count); need > MinWidth {
			return fmt.Errorf(
				"output: un-droppable columns need %d display columns (chrome %d + Σ Min %d) but MinWidth is %d",
				need, chromeFor(count), forced, MinWidth)
		}
	}
	return nil
}

package output

// TEST SCAFFOLDING — NOT PRODUCTION BEHAVIOUR.
//
// Every deliberate-defect switch in this package lives in this one file. The
// zero value of `mutants` is the shipped behaviour, so a production build
// never takes a mutated branch; the mutation harness sets one field at a time,
// re-runs the assertions, and asserts that they FAIL. A suite whose mutants
// all survive is a suite that proves nothing, so the harness is part of the
// deliverable rather than an afterthought.
//
// Three rules govern this file.
//
//  1. Nothing outside mutants.go may declare a behaviour switch. The switches
//     are the mutation harness's only lever, and a second lever somewhere else
//     is a defect class the harness cannot enumerate.
//
//  2. Allocator VARIANTS are not switches. §4.3 as finally adjudicated is the
//     only allocator, and no acceptance test may encode a known failure or
//     assert that the shipped code is wrong.
//
//  3. Only two kinds of file may assign `mutants`, and each must restore the
//     zero value on every path out of the assignment.
//
//     (a) A file carrying `//go:build mutation` — the mutation harness proper.
//
//     (b) A CONTROL TEST: an untagged test that plants one switch at a time to
//     show that a named assertion body goes red, so that "this check catches
//     X" is a measurement rather than a claim. A control test registers the
//     restore with `t.Cleanup` BEFORE its first plant and restores the zero
//     value between plants, so that neither a `t.Fatal` nor the next
//     iteration of its loop can run against a switched-on defect. The
//     controls are carved out here BY NAME, and a task that adds one adds its
//     name to this list in the same commit:
//
//     TestAllocatorMutantsRedenTheDirectChecks — alloc_policy_test.go
//     TestTableMutantsRedenTheBlockChecks — disclosure_test.go
//     TestStyleIsAppliedAfterAllocation — disclosure_test.go
//
//     An assignment from anywhere else is the leak this rule exists to
//     forbid: it turns a deliberate defect into the shipped behaviour for the
//     rest of the run, with `go test -race ./...` green and nothing to say so.
//
// This file is NEVER build-tagged (decision D-11). Production code reads
// these switches — measured as the table block lands: 6 references in
// alloc.go and 5 in blocks.go, and more as each later task re-threads the
// switches it held back — so tagging the declaration out breaks the ordinary
// build. The tag
// goes on the mutation harness and only there. The cost in the shipped binary
// is one zero-valued struct.
type mutantSwitches struct {
	// ------------------------------------------------------------ §4.3 chrome
	ChromeOff int // chromeFor undercounts the border and padding by this many cells

	// ----------------------------------------------------------- §4.3 step 2
	DropAgainstNatural bool // step 2 worded against the natural sum instead of the Min sum

	// --------------------------------------------------- §4.3 step 5 (relax)
	NoAtomicSqueeze   bool // step 5(a) removed: an Atomic column keeps its natural width
	RelaxOrderSwapped bool // step 5(b) runs before step 5(a)
	RelaxFloorOne     bool // step 5(b)'s floor drops from max(1, markerWidth(mode)) back to 1
	NoStateExemption  bool // the ColState exemption is removed from step 5(b)

	// ------------------------------------------------------ §4.4 Table.Render
	// PinTableWidth hands the allocator's computed total to lipgloss as
	// `.Width(total)`. lipgloss then re-fits the grid to that number,
	// absorbing an arithmetic error as silent content loss instead of
	// overflow — measured over this package's 2,752-render table sweep, a
	// chrome off-by-one produces 0 overflowing lines with this on and 5,174
	// without it. On its own it is byte-identical to the clean render, which
	// is exactly why the call is dangerous.
	PinTableWidth bool
	// NoCaptionDisclosure suppresses the "Hidden: …" / "Narrowed: …" clause, so
	// the table drops or squeezes a column without saying so.
	NoCaptionDisclosure bool
	// HardcodedWideFlag writes " (--wide)" into the clause regardless of
	// Table.WideFlag — the defect accepted review finding #12 exists to
	// prevent, which survives any corpus in which every table that drops a
	// column either declares a flag or is a degenerate Col literal.
	HardcodedWideFlag bool
	// CaptionWidth widens the caption's wrap budget by this many cells.
	CaptionWidth int
	// PaintBeforeFit applies the cell's Role BEFORE truncation and padding
	// rather than after, inverting §4.3's "style is applied after allocation,
	// never before". Every width assertion in the suite is blind to the
	// ORDER — ansi.Strip and ansi.StringWidth both ignore SGR — which is why
	// it needs an assertion that reads where the escapes fall rather than how
	// wide the line is.
	PaintBeforeFit bool
}

// mutants is the live set. Its zero value is the shipped behaviour.
var mutants mutantSwitches

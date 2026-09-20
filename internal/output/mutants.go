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
//  3. Only three kinds of file may assign `mutants`, and each must restore the
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
//     (c) A CHILD-PROCESS SEAM: applyDieChildMutant in message_test.go, and
//     nothing else. probeDie re-execs the test binary, so a switch planted in
//     the parent does not reach the process under measurement; the child
//     re-applies it from an environment variable at the top of
//     TestDieChildProcess. It carries no restore because there is nothing to
//     restore to — Die exits the child, and the assignment cannot outlive the
//     process it is made in. That is the whole of the exemption: an untagged
//     file assigning `mutants` in a process it is about to end.
//
//     An assignment from anywhere else is the leak this rule exists to
//     forbid: it turns a deliberate defect into the shipped behaviour for the
//     rest of the run, with `go test -race ./...` green and nothing to say so.
//
// This file is NEVER build-tagged (decision D-11). Production code branches
// on these switches directly — measured with the last of them wired: 27
// references over six files, alloc.go 6, blocks.go 6, stream.go 5, glyph.go 4,
// text.go 3, message.go 3 — so tagging the DECLARATION out breaks the ordinary
// build rather than the harness. The tag goes on the mutation harness and only
// there. The cost in the shipped binary is one zero-valued struct.
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
	// prevent. On this corpus caption_discloses kills it either way: 10
	// violations from the degenerate unflagged literals alone, 28 once a
	// constructor-built unflagged table is in the corpus. So the fixture that
	// adds that table gives the kill an honest case; it is not what produces
	// the kill.
	HardcodedWideFlag bool
	// CaptionWidth widens the caption's wrap budget by this many cells.
	CaptionWidth int
	// PaintBeforeFit applies the cell's Role BEFORE truncation and padding
	// rather than after, inverting §4.3's "style is applied after allocation,
	// never before". Every width assertion in the suite is blind to the
	// ORDER — ansi.Strip and ansi.StringWidth both ignore SGR — which is why
	// it needs an assertion that reads where the escapes fall rather than how
	// wide the line is. That is not the same as the corpus being blind to the
	// SWITCH: a painted cell that has to be CUT comes back short of its
	// allocation. The mechanism, measured rather than inferred — cutAt steps
	// with ansi.FirstGraphemeCluster, which returns the ESC byte itself at
	// width 0 and then hands back every byte of the parameter string as an
	// ordinary ONE-CELL cluster, so the budget is spent on the escape. On a
	// painted "degraded": clipTail(plain, 5) is "degr…" at 5 cells,
	// clipTail(painted, 5) is "\x1b[38;…" at 1.
	//
	// TWO DIFFERENT SWEEPS MEASURE THIS, and an earlier form of this comment
	// ran them together: it quoted the table sweep's numbers under the name
	// "the full 29..200 sweep". That named one sweep while this package had
	// only one; the 49-fixture corpus then landed and took the name with it,
	// leaving the sentence pointing at the wrong run. Both, measured on this
	// tree:
	//
	//	the table sweep TestTableGridPairing covers — 2,752 renders, widths
	//	29..200 over eight table fixtures in both glyph modes — goes red on
	//	130 of them, 70 through the abbreviation-width clause and 60 through
	//	gridFields' bordered-row check;
	//
	//	the corpus sweep TestMutationHarness runs — 17,200 renders over 50
	//	fixtures, and that pair moves with every fixture the corpus gains;
	//	TestAcceptanceSweep prints it at run time — reports grid_pairing 1465
	//	violations and state_mark_and_word 681, first `table/list @ 29
	//	(mode 0): "STATUS" allocated 9 cells but rendered 1 ("…")`. Those
	//	three did NOT move when the corpus gained its right-aligned fixture,
	//	which has no state column and is never truncated. grid_pairing is a
	//	NAME rather than one body: assertGridPairing and assertStateStructure
	//	both report through gridFields, so 1465 is the total across the two.
	//
	// TestTableMutantsRedenTheBlockChecks plants it and requires that red.
	PaintBeforeFit bool

	// ------------------------------------------------ §4.3 / §6.1 width
	RuneWidth             bool // W counts runes instead of display cells
	RuneSegmentation      bool // the cut primitives step by rune, not by grapheme cluster
	NoEllipsisReserve     bool // truncation reserves no room for the marker
	ASCIIMarkerMismeasure bool // "..." emitted while one cell is reserved for it
	PairsIndent           bool // KV and Fact values wrapped at the full budget, ignoring the indent
	NoMessageWrap         bool // the message helpers emit one unwrapped line — what they did before this layer

	// ------------------------------------------------------- §4.4 wrapping
	TruncateInsteadOfWrap bool // Wrap emits one truncated line

	// ------------------------------------------- §4.7 / §4.2 / §4.5 contract
	MessagesToStdout      bool // the message helpers write to stdout
	DieUnwrapped          bool // Die alone stops wrapping; its mark, role and sanitising are unchanged
	ColumnsRejectBelowMin bool // a COLUMNS below MinWidth is rejected instead of clamped
	ProbeWrongFd          bool // newStream probes fd 0 instead of its own fd
	NoStreamMemo          bool // Out()/Err() rebuild a Stream on every call
	ColourProbedOnStdout  bool // every stream's colour level resolved from stdout
	IgnoreCJKTag          bool // the CJK branch of §4.5's selection rule dropped
}

// mutants is the live set. Its zero value is the shipped behaviour.
var mutants mutantSwitches

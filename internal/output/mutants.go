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
//
//     An assignment from anywhere else is the leak this rule exists to
//     forbid: it turns a deliberate defect into the shipped behaviour for the
//     rest of the run, with `go test -race ./...` green and nothing to say so.
//
// This file is NEVER build-tagged (decision D-11). Production code reads
// these switches — measured as this file lands: 6 references in 1 non-test
// file, alloc.go, and more as each later task re-threads the switches it held
// back — so tagging the declaration out breaks the ordinary build. The tag
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
}

// mutants is the live set. Its zero value is the shipped behaviour.
var mutants mutantSwitches

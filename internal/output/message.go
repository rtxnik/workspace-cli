package output

import (
	"fmt"
	"os"
	"strings"
)

// The five message helpers.
//
// Their signatures are unchanged — 137 call sites across 18 files depend on
// them and recompile untouched. What changes is the body: the correct stream,
// the correct fd for colour detection, and wrapping to that stream's budget.
// §4.7 assigns Info, Success, Warn and Detail to stderr: stdout is the
// answer, stderr is everything about producing it.

// messageShape is the prefix, role and hanging indent of one helper.
type messageShape struct {
	state    State // the mark to prefix with
	hasState bool
	indent   int  // literal indent when there is no mark
	role     Role // the whole line carries this role; colour is decoration only
}

var (
	// Info carries no glyph. §4.5 retires `ℹ` — it is East-Asian Ambiguous
	// and frequently presented as an emoji two cells wide — and the six-state
	// vocabulary has no "information" state to borrow a mark from.
	shapeInfo    = messageShape{role: RoleInfo}
	shapeSuccess = messageShape{state: StateOK, hasState: true, role: RoleOK}
	shapeWarn    = messageShape{state: StateAdvisory, hasState: true, role: RoleWarn}
	shapeDetail  = messageShape{indent: 2, role: RoleMuted}
	shapeFail    = messageShape{state: StateFail, hasState: true, role: RoleFail}
)

// Info reports progress. stderr.
func Info(msg string) { emit(Err(), shapeInfo, msg) }

// Success reports that a step completed. stderr.
func Success(msg string) { emit(Err(), shapeSuccess, msg) }

// Warn reports a recoverable problem. stderr.
func Warn(msg string) { emit(Err(), shapeWarn, msg) }

// Detail is continuation prose under another message. stderr.
func Detail(msg string) { emit(Err(), shapeDetail, msg) }

// Die reports a fatal problem and exits.
//
// The signature and the os.Exit are kept deliberately: §4.8 retires Die in
// favour of returning an error, but only once every one of its 53 call sites
// has moved, and none of them is followed by a return. Making Die return here
// would drop 53 error branches into code that is unreachable today.
//
// Die is on its way out: return an error from RunE instead, and §4.8's single
// error-print point renders it as one Problem. Die is deleted by the PR that
// moves its last caller.
//
// That notice is prose and not the machine-readable "Deprecated:" marker, and
// the difference was measured rather than assumed: with the marker in place,
// `golangci-lint run` reported 53 SA1019 issues — one per call site §4.8
// deliberately keeps until phase 1 — and a clean linter run is one of this
// plan's repository gates. The marker belongs to the PR that starts moving
// those call sites, where the flags are the work list rather than noise.
func Die(msg string) {
	emit(Err(), shapeFail, msg)
	os.Exit(1)
}

// emit writes one message to a stream.
//
// The write error is discarded deliberately and only here. §4.7's rule that a
// failed write is an error, not a shrug, is about stdout — the artifact a
// script consumes, which travels the error-returning WriteJSON path. These
// helpers write chatter to stderr with no way to return anything to their 137
// call sites, and reporting a failed stderr write on stderr is not a thing
// that can work.
func emit(s *Stream, shape messageShape, msg string) {
	_, _ = fmt.Fprintln(s, renderMessage(s, shape, msg))
}

// renderMessage lays a message out against the stream's budget and returns it
// without writing. It is separate from emit so §6.1 can sweep every helper at
// every width from MinWidth to 200 — the helpers are phase 0's entire
// deliverable and the dominant call volume, and a sweep over block types
// alone stays green while they remain the single unwrapped Fprintln they are
// today.
func renderMessage(s *Stream, shape messageShape, msg string) string {
	// Message text is frequently an upstream error string, so it travels the
	// same sanitising path as Problem.Cause (§4.4).
	msg = Sanitise(msg)

	prefix := strings.Repeat(" ", shape.indent)
	if shape.hasState {
		prefix = stateMark(shape.state, s.mode) + " "
	}

	budget := s.budget()
	hang := strings.Repeat(" ", W(prefix))
	lines := Wrap(msg, budget-W(prefix))
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			out = append(out, s.paint(shape.role, prefix+line))
			continue
		}
		out = append(out, s.paint(shape.role, hang+line))
	}
	return strings.Join(out, "\n")
}

package output

import (
	"fmt"
	"strings"
)

// The message helpers.
//
// When phase 0 replaced their bodies it kept their signatures, and all 137
// call sites across 18 files recompiled untouched. What changed was the body:
// the correct stream, the correct fd for colour detection, and wrapping to
// that stream's budget.
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

// Fail reports a fatal problem on stderr and returns. It does not exit: the
// root's error protocol in cmd/root.go prints through Fail every error that
// reaches it with a message, and chooses the exit code itself.
func Fail(msg string) {
	if mutants.FailUnwrapped {
		// The defect §6.1 cannot otherwise see: the fail shape is swept only
		// through renderMessage, so a change made here — printing the message
		// as one unwrapped line — leaves the whole corpus green. probeFail in
		// message_test.go is what reaches it, and it has to carry this switch
		// across a process boundary to do so.
		//
		// THE BRANCH IS DELIBERATELY SINGLE-AXIS. It is renderMessage's own
		// output with NoMessageWrap on, for the fail shape alone: the mark, the
		// role and the sanitising all survive and only the wrap goes. An
		// earlier form dropped s.paint too, which made one switch stand for two
		// changes — invisible in probeFail, whose child writes to a pipe at
		// ColourNone where paint emits nothing, and exactly the ambiguity this
		// harness exists to catch everywhere else.
		s := Err()
		_, _ = fmt.Fprintln(s, s.paint(shapeFail.role, stateMark(shapeFail.state, s.mode)+" "+Sanitise(msg)))
		return
	}
	emit(Err(), shapeFail, msg)
}

// emit writes one message to a stream.
//
// The write error is discarded deliberately and only here. §4.7's rule that a
// failed write is an error, not a shrug, is about stdout — the artifact a
// script consumes, which travels the error-returning WriteJSON path. These
// helpers write chatter to stderr with no way to return anything to their
// call sites, and reporting a failed stderr write on stderr is not a thing
// that can work.
func emit(s *Stream, shape messageShape, msg string) {
	if mutants.MessagesToStdout {
		s = Out() // §4.7 inverted: chatter written onto the answer's stream
	}
	_, _ = fmt.Fprintln(s, renderMessage(s, shape, msg))
}

// renderMessage lays a message out against the stream's budget and returns it
// without writing. It is separate from emit so §6.1 can sweep every helper at
// every width from MinWidth to 200.
//
// The helpers earn that place in the corpus: they are phase 0's entire
// deliverable and the dominant call volume, and a sweep over block types
// alone stayed green for as long as each of them was the one unwrapped
// Fprintln it was before this file existed.
func renderMessage(s *Stream, shape messageShape, msg string) string {
	// Message text is frequently an upstream error string, so it travels the
	// same sanitising path as Problem.Cause (§4.4).
	msg = Sanitise(msg)

	prefix := strings.Repeat(" ", shape.indent)
	if shape.hasState {
		prefix = stateMark(shape.state, s.mode) + " "
	}
	if mutants.NoMessageWrap {
		// One unwrapped line: what the five helpers did before this file
		// existed, and the reason the sweep reaches them at all.
		return s.paint(shape.role, prefix+msg)
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

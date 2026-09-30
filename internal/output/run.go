package output

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// §4.7 The step runner.
//
// Run is the only progress display: a spinner and one live line on a
// terminal, a start line and a result line anywhere else. While a task runs,
// the only bytes written to the process's streams are the runner's frame:
// the message helpers queue, and a child under the runner writes to the
// task's Log.
//
// One owner. The outermost Run owns the frame, the queue and the ticker for
// its whole duration; a Run called inside a task neither draws nor drains,
// and its lines join the owner's queue. Each task passes through three
// states:
//
//   - Running. The ticker redraws the frame. A message helper appends to the
//     queue under the queue's lock and returns. No lock is held while
//     anything is written, and neither the log nor a message helper calls
//     the other.
//   - Draining. The owner stops the ticker and waits for it to exit, erases
//     the frame and writes the result line. Then it takes the queue under
//     its lock and writes what it took straight to the stream, never back
//     through the queueing path, until it finds the queue empty — and closes
//     the gate in that same locked step, so a message that arrives later is
//     written directly and after the drained ones.
//   - Closed. The next task starts, or Run returns.

// Task is one step the runner shows progress for. Run receives the step's
// log: what a child or the task writes there feeds the live line and the
// tail.
type Task struct {
	Title string
	Run   func(log *Log) error
}

// TaskError is the error Run returns for a failed task.
type TaskError struct {
	Title string   // the task that failed
	Err   error    // what it returned
	Tail  []string // the last lines of its log, raw
}

// Error is Err's message: no message changes.
func (e *TaskError) Error() string { return e.Err.Error() }

// Unwrap is Err, so errors.Is and errors.As see through a TaskError.
func (e *TaskError) Unwrap() error { return e.Err }

// AsProblem is the Problem the root prints for a failed task. When Err's
// chain carries a Problem of its own — a task that returned a ProblemError,
// or a nested Run's TaskError — it is that Problem, with this task's tail as
// its cause only when its cause is empty: the root takes the outermost
// carrier, and without this rule an outer TaskError would hide an inner
// Problem's facts, steps and tail. Otherwise it is Err's message as the
// title and the tail, joined, as the cause.
func (e *TaskError) AsProblem() Problem {
	cause := strings.Join(e.Tail, "\n")
	if p, ok := ProblemOf(e.Err); ok {
		if p.Cause == "" {
			p.Cause = cause
		}
		return p
	}
	return Problem{Title: e.Err.Error(), Cause: cause}
}

// Run runs tasks in order on Err() and stops at the first failure, which it
// returns as a *TaskError.
func Run(tasks ...Task) error {
	return newRunner(Err()).run(tasks)
}

// runner is one call of Run. Its clock and its ticker are seams: the tests
// drive the frame with a clock and ticks of their own.
type runner struct {
	s     *Stream
	now   func() time.Time
	tick  func() (ticks <-chan time.Time, stop func())
	hooks runnerHooks
}

// runnerHooks are the points at which a test interleaves with the owner.
// Each is nil in production.
type runnerHooks struct {
	redrawn   func() // the ticker goroutine has written a redraw
	drainTook func() // the owner has taken a batch of the queue, and not yet written it
}

// frameInterval is how often the frame is redrawn.
const frameInterval = 100 * time.Millisecond

func newRunner(s *Stream) *runner {
	return &runner{
		s:   s,
		now: time.Now,
		tick: func() (<-chan time.Time, func()) {
			t := time.NewTicker(frameInterval)
			return t.C, t.Stop
		},
	}
}

// owner is what the outermost Run holds for its whole duration: the queue and
// its gate, and the running task's log for a Run nested inside it.
var owner struct {
	mu    sync.Mutex
	r     *runner
	open  bool                     // the gate: a line queues while a task runs
	queue []func(s *Stream) string // each renders one line on the owner's stream
	log   *Log
}

// enqueue holds line for the owner's drain while a task runs, and reports
// whether it did. The message helpers call it before they write.
func enqueue(line func(s *Stream) string) bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.open {
		return false
	}
	owner.queue = append(owner.queue, line)
	return true
}

func (r *runner) run(tasks []Task) error {
	owner.mu.Lock()
	outer := owner.r
	if outer == nil {
		owner.r = r
	}
	log := owner.log
	owner.mu.Unlock()
	if outer != nil {
		r.now = outer.now
		return r.runNested(tasks, log)
	}
	defer func() {
		owner.mu.Lock()
		owner.r, owner.open, owner.queue, owner.log = nil, false, nil, nil
		owner.mu.Unlock()
	}()
	for i, t := range tasks {
		err := r.runTask(t)
		if err == nil {
			continue
		}
		// A task whose log could not be made did not start: it is listed with
		// the tasks that did not run.
		notRun := tasks[i+1:]
		if _, ran := err.(*TaskError); !ran {
			notRun = tasks[i:]
		}
		for _, rest := range notRun {
			r.writeLine(stepLine(r.s, StateIdle, rest.Title, ""))
		}
		return err
	}
	return nil
}

// runTask runs one task as the owner.
func (r *runner) runTask(t Task) error {
	log, err := newLog()
	if err != nil {
		return err
	}
	defer log.close()

	start := r.now()
	if !r.s.IsTTY() {
		r.writeLine(stepLine(r.s, StateBusy, t.Title, ""))
	}
	owner.mu.Lock()
	owner.open, owner.log = true, log
	owner.mu.Unlock()

	var f *frame
	if r.s.IsTTY() {
		f = r.startFrame(t.Title, start, log)
	}

	// A panic takes the same path out as a failure: the result line and the
	// queue are written, then the panic goes on.
	finished := false
	defer func() {
		if finished {
			return
		}
		p := recover()
		r.drain(f, stepLine(r.s, StateFail, t.Title, elapsedText(r.now().Sub(start))))
		if p != nil {
			panic(p)
		}
	}()
	runErr := t.Run(log)
	finished = true

	st := StateOK
	if runErr != nil {
		st = StateFail
	}
	r.drain(f, stepLine(r.s, st, t.Title, elapsedText(r.now().Sub(start))))
	if runErr != nil {
		return &TaskError{Title: t.Title, Err: runErr, Tail: log.Tail()}
	}
	return nil
}

// drain ends a task: the frame goes, the result line is written, then the
// queue, batch by batch, until the owner finds it empty and closes the gate.
func (r *runner) drain(f *frame, result string) {
	if f != nil {
		close(f.stop)
		<-f.done
		r.writeRaw(f.erase())
	}
	r.writeLine(result)
	for {
		owner.mu.Lock()
		batch := owner.queue
		owner.queue = nil
		if len(batch) == 0 {
			owner.open, owner.log = false, nil
			owner.mu.Unlock()
			return
		}
		owner.mu.Unlock()
		if r.hooks.drainTook != nil {
			r.hooks.drainTook()
		}
		for _, line := range batch {
			r.writeLine(line(r.s))
		}
	}
}

// runNested runs the tasks of a Run called inside a task. It draws no frame
// and writes nothing itself: its lines join the owner's queue, and its tasks
// write to the running task's log.
func (r *runner) runNested(tasks []Task, log *Log) error {
	for i, t := range tasks {
		start := r.now()
		err := t.Run(log)
		st, title := StateOK, t.Title
		if err != nil {
			st = StateFail
		}
		elapsed := elapsedText(r.now().Sub(start))
		r.queueLine(func(s *Stream) string { return stepLine(s, st, title, elapsed) })
		if err == nil {
			continue
		}
		for _, rest := range tasks[i+1:] {
			title := rest.Title
			r.queueLine(func(s *Stream) string { return stepLine(s, StateIdle, title, "") })
		}
		return &TaskError{Title: t.Title, Err: err, Tail: log.Tail()}
	}
	return nil
}

// queueLine queues a nested Run's line, or writes it when the gate has
// closed under it.
func (r *runner) queueLine(line func(s *Stream) string) {
	if !enqueue(line) {
		r.writeLine(line(r.s))
	}
}

func (r *runner) writeLine(line string) { _, _ = fmt.Fprintln(r.s, line) }

func (r *runner) writeRaw(text string) { _, _ = io.WriteString(r.s, text) }

// ------------------------------------------------------------------ frame

// frame is the two lines a running task occupies on a terminal. The owner
// draws it first and erases it last; in between only the ticker goroutine
// touches it, and the owner waits for that goroutine to exit before it
// erases, so its fields are never shared.
type frame struct {
	r     *runner
	title string
	start time.Time
	log   *Log
	n     int // the spinner's frame
	drawn int // the lines on screen: 0, 1 or 2
	stop  chan struct{}
	done  chan struct{}
}

func (r *runner) startFrame(title string, start time.Time, log *Log) *frame {
	f := &frame{r: r, title: title, start: start, log: log, stop: make(chan struct{}), done: make(chan struct{})}
	r.writeRaw(f.redraw())
	ticks, stopTicker := r.tick()
	go func() {
		defer close(f.done)
		defer stopTicker()
		for {
			select {
			case <-f.stop:
				return
			case <-ticks:
				f.n++
				r.writeRaw(f.redraw())
				if r.hooks.redrawn != nil {
					r.hooks.redrawn()
				}
			}
		}
	}()
	return f
}

// redraw is the bytes that replace the frame on screen with its current
// state: \r, then ESC[1A when the second line was drawn, then ESC[J, then the
// lines. The cursor is not hidden, so nothing has to be restored if the
// process dies.
func (f *frame) redraw() string {
	lines := frameLines(f.r.s, spinnerGlyph(f.r.s.mode, f.n), f.title,
		elapsedText(f.r.now().Sub(f.start)), f.log.liveLine())
	out := f.erase() + strings.Join(lines, "\n")
	f.drawn = len(lines)
	return out
}

// erase is the bytes that clear the frame, leaving the cursor at the start of
// its first line.
func (f *frame) erase() string {
	var b strings.Builder
	if f.drawn > 0 {
		b.WriteString("\r")
		if f.drawn == 2 {
			b.WriteString("\x1b[1A")
		}
		b.WriteString("\x1b[J")
	}
	f.drawn = 0
	return b.String()
}

// frameLines lays the frame out: the spinner glyph, the title and the elapsed
// time on the first line and, once the log holds a line, that line under it,
// cleaned by SanitiseInline — a child's output carries escapes that would
// move the cursor under the frame. Both lines are laid out against budget −
// 1: a line exactly as wide as the terminal leaves the cursor of some
// terminals on the next row, and the redraw would then climb one row short.
func frameLines(s *Stream, glyph, title, elapsed, live string) []string {
	width := s.budget() - 1
	room := width - W(glyph) - 1 - 2 - W(elapsed)
	lines := []string{s.paint(RoleInfo, glyph) + " " +
		clipTail(SanitiseInline(title), room, s.mode) + "  " + s.paint(RoleMuted, elapsed)}
	if live != "" {
		lines = append(lines, "  "+s.paint(RoleMuted, clipTail(SanitiseInline(live), width-2, s.mode)))
	}
	return lines
}

// spinnerFrames are the spinner's glyphs in the UTF-8 glyph mode: braille
// patterns, East-Asian Neutral, so one cell under both conventions.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerFramesASCII are its glyphs in the ASCII glyph mode.
var spinnerFramesASCII = []string{"|", "/", "-", `\`}

func spinnerGlyph(mode GlyphMode, n int) string {
	frames := spinnerFrames
	if mode == GlyphASCII {
		frames = spinnerFramesASCII
	}
	return frames[n%len(frames)]
}

// ------------------------------------------------------------------- lines

// stepLine renders a step's line the way a message of its state is: the
// state's mark, the title wrapped at the budget with a hanging indent of 2,
// in the state's role. suffix — the time, muted — follows the title's last
// line two spaces after it when it fits there, and takes a line of its own
// when it does not.
func stepLine(s *Stream, st State, title, suffix string) string {
	prefix := stateMark(st, s.mode) + " "
	hang := strings.Repeat(" ", W(prefix))
	width := s.budget() - W(prefix)
	role := stateRole(st)
	lines := Wrap(Sanitise(title), width)
	out := make([]string, 0, len(lines)+1)
	for i, line := range lines {
		lead := hang
		if i == 0 {
			lead = prefix
		}
		out = append(out, s.paint(role, lead+line))
	}
	if suffix != "" {
		if last := len(out) - 1; W(lines[last])+2+W(suffix) <= width {
			out[last] += "  " + s.paint(RoleMuted, suffix)
		} else {
			out = append(out, hang+s.paint(RoleMuted, suffix))
		}
	}
	return strings.Join(out, "\n")
}

// elapsedText is a step's time: 0.4s and 12.3s under a minute, 2m13s under an
// hour, then 1h04m. Each unit is cut, never rounded, so 59.96s reads 59.9s.
func elapsedText(d time.Duration) string {
	switch {
	case d < 0:
		d = 0
		fallthrough
	case d < time.Minute:
		tenths := int64(d / (100 * time.Millisecond))
		return fmt.Sprintf("%d.%ds", tenths/10, tenths%10)
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int64(d/time.Minute), int64(d%time.Minute/time.Second))
	default:
		return fmt.Sprintf("%dh%02dm", int64(d/time.Hour), int64(d%time.Hour/time.Minute))
	}
}

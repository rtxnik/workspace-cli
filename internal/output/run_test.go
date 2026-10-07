package output

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeClock is the runner's clock in these tests: it moves only when a test
// moves it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testRunner is a runner over s whose clock is c and whose ticks are the
// ones a test sends on ticks.
func testRunner(s *Stream, c *fakeClock, ticks chan time.Time) *runner {
	return &runner{
		s:    s,
		now:  c.now,
		tick: func() (<-chan time.Time, func()) { return ticks, func() {} },
	}
}

// syncBuffer is a bytes.Buffer the ticker goroutine and the owner can both
// write to.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func ok(*Log) error { return nil }

// TestRunWritesPlainLinesOffATerminal: off a terminal each task gets a start
// line and a result line, and not one ESC byte is written.
func TestRunWritesPlainLinesOffATerminal(t *testing.T) {
	var buf syncBuffer
	c := &fakeClock{}
	r := testRunner(NewStreamAt(&buf, 80, false, ColourNone, false), c, nil)
	err := r.run([]Task{
		{Title: "Checking workspace", Run: ok},
		{Title: "Starting container", Run: func(*Log) error {
			c.advance(2*time.Minute + 13*time.Second)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "~ Checking workspace\n" +
		"✓ Checking workspace  0.0s\n" +
		"~ Starting container\n" +
		"✓ Starting container  2m13s\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestRunStopsAtTheFirstFailure: the failed task's result line carries no
// error text, each task that did not run gets a "-" line, and the error is a
// *TaskError holding the task's title, its error and its log's last lines.
func TestRunStopsAtTheFirstFailure(t *testing.T) {
	var buf syncBuffer
	c := &fakeClock{}
	r := testRunner(NewStreamAt(&buf, 80, false, ColourNone, false), c, nil)
	cause := errors.New("devpod up: exit status 1")
	err := r.run([]Task{
		{Title: "Checking workspace", Run: ok},
		{Title: "Starting container", Run: func(log *Log) error {
			_, _ = fmt.Fprintln(log, "[12:01:31] info up: step two")
			_, _ = fmt.Fprint(log, "[12:01:32] fatal up: denied")
			c.advance(12300 * time.Millisecond)
			return cause
		}},
		{Title: "Fixing routes", Run: func(*Log) error {
			t.Error("a task ran after a failure")
			return nil
		}},
	})
	want := "~ Checking workspace\n" +
		"✓ Checking workspace  0.0s\n" +
		"~ Starting container\n" +
		"✗ Starting container  12.3s\n" +
		"- Fixing routes\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	var te *TaskError
	if !errors.As(err, &te) {
		t.Fatalf("run returned %T %v; want a *TaskError", err, err)
	}
	if te.Title != "Starting container" || !errors.Is(err, cause) || err.Error() != cause.Error() {
		t.Errorf("TaskError{%q, %v}; want the failed task's title and error", te.Title, te.Err)
	}
	if want := []string{"[12:01:31] info up: step two", "[12:01:32] fatal up: denied"}; !reflect.DeepEqual(te.Tail, want) {
		t.Errorf("Tail = %q; want %q", te.Tail, want)
	}
}

// TestRunDrawsAFrameOnATerminal: on a terminal the runner draws the frame and
// redraws it at each tick — the spinner, the title and the elapsed time, then
// the log's last line under them once there is one — and replaces it with
// the result line, byte for byte.
func TestRunDrawsAFrameOnATerminal(t *testing.T) {
	var buf syncBuffer
	c := &fakeClock{}
	ticks := make(chan time.Time)
	redrawn := make(chan struct{})
	r := testRunner(NewStreamAt(&buf, 40, true, ColourNone, false), c, ticks)
	r.hooks.redrawn = func() { redrawn <- struct{}{} }
	tick := func(d time.Duration) {
		c.advance(d)
		ticks <- time.Time{}
		<-redrawn
	}
	err := r.run([]Task{{Title: "Starting container", Run: func(log *Log) error {
		tick(100 * time.Millisecond)
		_, _ = fmt.Fprintln(log, "info Building image with BuildKit")
		tick(time.Minute + 11900*time.Millisecond)
		_, _ = fmt.Fprint(log, "step 1/3\rstep 2/3")
		tick(0)
		return nil
	}}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "⠋ Starting container  0.0s" +
		"\r\x1b[J⠙ Starting container  0.1s" +
		"\r\x1b[J⠹ Starting container  1m12s\n  info Building image with BuildKit" +
		"\r\x1b[1A\x1b[J⠸ Starting container  1m12s\n  step 2/3" +
		"\r\x1b[1A\x1b[J✓ Starting container  1m12s\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestRunDrawsNoFrameWhereItCannotRedraw: a terminal whose width is not
// known — a pty reporting 0 columns — or is under MinWidth, and one that
// says it does not move its cursor (TERM=dumb), get the plain lines: the
// frame's redraw climbs one row, so a line that wraps or a cursor that does
// not move leaves every frame behind. The narrow terminal is resolved the
// way the process's streams are, through newStream, where its width is
// clamped up to MinWidth.
func TestRunDrawsNoFrameWhereItCannotRedraw(t *testing.T) {
	dumb := NewStreamAt(nil, 80, true, ColourNone, false)
	dumb.dumb = true
	f, err := os.CreateTemp(t.TempDir(), "stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	utf8env := func(k string) string {
		if k == "LANG" {
			return "en_US.UTF-8"
		}
		return ""
	}
	narrow := newStream(f, utf8env, func(uintptr) (int, error) { return MinWidth - 4, nil })
	narrow.tty = true // f is no terminal; the width is what is under test
	if narrow.width != MinWidth {
		t.Fatalf("the narrow stream resolved to width %d; want it clamped to %d", narrow.width, MinWidth)
	}
	for _, c := range []struct {
		name string
		s    *Stream
	}{
		{"a width not known", NewStreamAt(nil, WidthUnbounded, true, ColourNone, false)},
		{"a width under the minimum", narrow},
		{"TERM=dumb", dumb},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf syncBuffer
			c.s.w = &buf
			r := testRunner(c.s, &fakeClock{}, make(chan time.Time))
			if err := r.run([]Task{{Title: "Starting container", Run: ok}}); err != nil {
				t.Fatalf("run: %v", err)
			}
			if want := "~ Starting container\n✓ Starting container  0.0s\n"; buf.String() != want {
				t.Errorf("got\n%q\nwant\n%q", buf.String(), want)
			}
		})
	}
}

// TestAStreamKnowsADumbTerminal: TERM=dumb is read with the rest of the
// stream's environment, and an ordinary TERM is not dumb.
func TestAStreamKnowsADumbTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	probe := func(uintptr) (int, error) { return 80, nil }
	for term, want := range map[string]bool{"dumb": true, "xterm-256color": false, "": false} {
		getenv := func(k string) string {
			if k == "TERM" {
				return term
			}
			return ""
		}
		if got := newStream(f, getenv, probe).dumb; got != want {
			t.Errorf("TERM=%q: dumb %t; want %t", term, got, want)
		}
	}
}

// TestStdStreamKeepsADumbTerminal: the process's streams, rebuilt over a
// late-bound writer, keep what the probe read of TERM.
func TestStdStreamKeepsADumbTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	t.Setenv("TERM", "dumb")
	if !newStdStream(f, true).dumb {
		t.Error("the process's stderr stream lost TERM=dumb")
	}
}

// TestStdStreamKeepsANarrowTerminal: the process's streams keep that the
// width they were resolved from — COLUMNS=20 here — is under MinWidth,
// although the width itself is clamped up to it.
func TestStdStreamKeepsANarrowTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	t.Setenv("COLUMNS", "20")
	s := newStdStream(f, true)
	if !s.narrow || s.width != MinWidth {
		t.Errorf("COLUMNS=20: narrow %t, width %d; want narrow at width %d", s.narrow, s.width, MinWidth)
	}
	t.Setenv("COLUMNS", "80")
	if newStdStream(f, true).narrow {
		t.Error("COLUMNS=80 resolved as narrow")
	}
}

// TestRunFrameFollowsTheGlyphMode: in the ASCII glyph mode the spinner and
// the marks are ASCII.
func TestRunFrameFollowsTheGlyphMode(t *testing.T) {
	var buf syncBuffer
	c := &fakeClock{}
	ticks := make(chan time.Time)
	redrawn := make(chan struct{})
	r := testRunner(NewStreamAt(&buf, 40, true, ColourNone, true), c, ticks)
	r.hooks.redrawn = func() { redrawn <- struct{}{} }
	_ = r.run([]Task{{Title: "Stopping workspace", Run: func(*Log) error {
		ticks <- time.Time{}
		<-redrawn
		return errors.New("boom")
	}}})
	want := "| Stopping workspace  0.0s" +
		"\r\x1b[J/ Stopping workspace  0.0s" +
		"\r\x1b[Jx Stopping workspace  0.0s\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestRunQueuesMessagesUntilTheResultLine: a message a task writes is held
// while the task runs and written after its result line, in order, and
// before the lines of the tasks a failure left unrun — on a terminal and off
// it, so the order of the lines does not depend on the mode.
func TestRunQueuesMessagesUntilTheResultLine(t *testing.T) {
	tasks := func() []Task {
		return []Task{
			{Title: "Deleting workspace", Run: func(*Log) error {
				Warn("devpod delete: exit status 1")
				Detail("[12:01:32] fatal delete: denied")
				Info("still removing the directory")
				return errors.New("remove: permission denied")
			}},
			{Title: "Cleaning up", Run: ok},
		}
	}
	for _, tty := range []bool{false, true} {
		var buf syncBuffer
		r := testRunner(NewStreamAt(&buf, 80, tty, ColourNone, false), &fakeClock{}, make(chan time.Time))
		_ = r.run(tasks())
		got := buf.String()
		result := strings.Index(got, "✗ Deleting workspace  0.0s\n")
		queued := strings.Index(got, "⚠ devpod delete: exit status 1\n"+
			"  [12:01:32] fatal delete: denied\n"+
			"still removing the directory\n")
		notRun := strings.Index(got, "- Cleaning up\n")
		if result < 0 || queued < 0 || notRun < 0 || result >= queued || queued >= notRun {
			t.Errorf("tty %t: want the result line, then the queue in order, then the unrun task:\n%q", tty, got)
		}
		if !strings.HasSuffix(got, "- Cleaning up\n") {
			t.Errorf("tty %t: something was written after the unrun task's line:\n%q", tty, got)
		}
	}
}

// TestRunWritesDirectlyOutsideATask: with no task running, a message is
// written at once, not queued.
func TestRunWritesDirectlyOutsideATask(t *testing.T) {
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 80, false, ColourNone, false), &fakeClock{}, nil)
	_ = r.run([]Task{{Title: "One", Run: ok}})
	if enqueue(func(*Stream) string { return "late" }) {
		t.Error("a line was queued after Run returned; it would never be written")
	}
}

// TestRunFlushesTheQueueWhenATaskPanics: a panic inside a task still writes
// the result line and the queue, and still reaches the caller.
func TestRunFlushesTheQueueWhenATaskPanics(t *testing.T) {
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 80, true, ColourNone, false), &fakeClock{}, make(chan time.Time))
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = r.run([]Task{{Title: "Building proxy image", Run: func(*Log) error {
			Warn("recipe drift ignored")
			panic("boom")
		}}})
	}()
	if recovered != "boom" {
		t.Fatalf("the panic did not reach the caller: recovered %v", recovered)
	}
	want := "⠋ Building proxy image  0.0s\r\x1b[J✗ Building proxy image  0.0s\n⚠ recipe drift ignored\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if enqueue(func(*Stream) string { return "late" }) {
		t.Error("the gate was left open after the panic")
	}
}

// TestNestedRunJoinsTheQueue: a Run called inside a task draws no second
// frame and writes nothing itself; its lines join the owner's queue, after
// the outer task's result line, and its failure renders through the root
// with its own Problem and tail.
func TestNestedRunJoinsTheQueue(t *testing.T) {
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 60, true, ColourNone, false), &fakeClock{}, make(chan time.Time))
	inner := &ProblemError{P: Problem{Title: "Switch to \"backup\" failed", Steps: []Remedy{{"Restore previous", "ws proxy profile use primary"}}},
		Err: errors.New("switch to \"backup\" failed (previous=\"primary\"): restart: exit status 1")}
	err := r.run([]Task{{Title: "Switching profile", Run: func(log *Log) error {
		return Run(
			Task{Title: "Validate target profile", Run: ok},
			Task{Title: "Restart dev-proxy", Run: func(log *Log) error {
				_, _ = fmt.Fprintln(log, "Error response from daemon: container is restarting")
				return inner
			}},
			Task{Title: "Wait for liveness", Run: ok},
		)
	}}})
	want := "⠋ Switching profile  0.0s\r\x1b[J✗ Switching profile  0.0s\n" +
		"✓ Validate target profile  0.0s\n" +
		"✗ Restart dev-proxy  0.0s\n" +
		"- Wait for liveness\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	p, ok := ProblemOf(err)
	if !ok || p.Title != inner.P.Title || !reflect.DeepEqual(p.Steps, inner.P.Steps) ||
		p.Cause != "Error response from daemon: container is restarting" {
		t.Errorf("the root would print %+v; want the inner Problem with the inner tail as its cause", p)
	}
}

// TestNestedRunOrdersItsLinesAsTheOwner: a nested Run's lines come out in
// the owner's order — on a terminal each task's result line and then its
// messages, off one its start line first — so a warning stays under the step
// it belongs to.
func TestNestedRunOrdersItsLinesAsTheOwner(t *testing.T) {
	for _, c := range []struct {
		tty  bool
		want string
	}{
		{false, "~ Switching profile\n✓ Switching profile  0.0s\n" +
			"~ Validate target profile\n✓ Validate target profile  0.0s\n⚠ checked\n" +
			"~ Restart dev-proxy\n✓ Restart dev-proxy  0.0s\n⚠ restarted\n"},
		{true, "⠋ Switching profile  0.0s\r\x1b[J✓ Switching profile  0.0s\n" +
			"✓ Validate target profile  0.0s\n⚠ checked\n" +
			"✓ Restart dev-proxy  0.0s\n⚠ restarted\n"},
	} {
		var buf syncBuffer
		r := testRunner(NewStreamAt(&buf, 60, c.tty, ColourNone, false), &fakeClock{}, make(chan time.Time))
		err := r.run([]Task{{Title: "Switching profile", Run: func(*Log) error {
			return Run(
				Task{Title: "Validate target profile", Run: func(*Log) error { Warn("checked"); return nil }},
				Task{Title: "Restart dev-proxy", Run: func(*Log) error { Warn("restarted"); return nil }},
			)
		}}})
		if err != nil {
			t.Fatalf("tty %t: run: %v", c.tty, err)
		}
		if got := buf.String(); got != c.want {
			t.Errorf("tty %t: got\n%q\nwant\n%q", c.tty, got, c.want)
		}
	}
}

// TestConcurrentNestedRunsKeepTheirOrder: two nested Runs inside one task,
// off a terminal, both started before either finishes: each result line
// comes after its own start line and before its own messages. A result line
// holds the place its task reserved when it started, not a position counted
// in a queue the other Run has grown since.
func TestConcurrentNestedRunsKeepTheirOrder(t *testing.T) {
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 60, false, ColourNone, false), &fakeClock{}, nil)
	err := r.run([]Task{{Title: "Switching profile", Run: func(*Log) error {
		aIn, bIn, aDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			aDone <- Run(Task{Title: "Validate target profile", Run: func(*Log) error {
				close(aIn)
				<-bIn
				Warn("checked")
				return nil
			}})
		}()
		<-aIn
		var errA error
		errB := Run(Task{Title: "Restart dev-proxy", Run: func(*Log) error {
			close(bIn)
			errA = <-aDone // the first Run finishes while this one runs
			Warn("restarted")
			return nil
		}})
		return errors.Join(errA, errB)
	}}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "~ Switching profile\n✓ Switching profile  0.0s\n" +
		"~ Validate target profile\n✓ Validate target profile  0.0s\n" +
		"~ Restart dev-proxy\n✓ Restart dev-proxy  0.0s\n" +
		"⚠ checked\n⚠ restarted\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestANestedRunWithoutARunningTaskGetsALog: a Run that finds an owner with
// no task running — started on a goroutine of its own between two tasks, or
// inside a task a signal has drained — is given a log of its own rather
// than none.
func TestANestedRunWithoutARunningTaskGetsALog(t *testing.T) {
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 60, false, ColourNone, false), &fakeClock{}, nil)
	err := r.runNested([]Task{{Title: "Restart dev-proxy", Run: func(log *Log) error {
		_, _ = fmt.Fprintln(log, "Error response from daemon")
		return errors.New("restart: exit status 1")
	}}}, nil, r)
	var te *TaskError
	if !errors.As(err, &te) || !reflect.DeepEqual(te.Tail, []string{"Error response from daemon"}) {
		t.Errorf("runNested returned %#v; want the task's TaskError with its own tail", err)
	}
}

// TestATaskErrorWithoutAnError: a TaskError built with no Err — by hand, in
// a test double — reads as its task failing rather than panicking in the
// root.
func TestATaskErrorWithoutAnError(t *testing.T) {
	te := &TaskError{Title: "Starting container"}
	if got := te.Error(); got != "Starting container failed" {
		t.Errorf("Error() = %q; want %q", got, "Starting container failed")
	}
	if got := te.AsProblem(); got.Title != "Starting container failed" {
		t.Errorf("AsProblem() = %+v; want the same title", got)
	}
}

// TestTaskErrorAsProblem: the Problem a failed task renders as.
func TestTaskErrorAsProblem(t *testing.T) {
	tail := []string{"step two", "fatal: denied"}
	withCause := Problem{Title: "Failed to start proxy", Cause: "image not found"}
	noCause := Problem{Title: "Failed to start proxy", Steps: []Remedy{{"Rebuild image", "ws proxy rebuild"}}}
	inner := &TaskError{Title: "inner", Err: errors.New("inner failed"), Tail: []string{"inner line"}}
	for _, c := range []struct {
		name string
		err  error
		want Problem
	}{
		{"a plain error: its message and the tail",
			errors.New("devpod stop: exit status 1"),
			Problem{Title: "devpod stop: exit status 1", Cause: "step two\nfatal: denied"}},
		{"a carried Problem with a cause keeps it",
			&ProblemError{P: withCause, Err: errors.New("x")}, withCause},
		{"a carried Problem without a cause takes the tail",
			fmt.Errorf("up: %w", &ProblemError{P: noCause, Err: errors.New("x")}),
			Problem{Title: noCause.Title, Cause: "step two\nfatal: denied", Steps: noCause.Steps}},
		{"a nested TaskError keeps its own tail",
			inner, Problem{Title: "inner failed", Cause: "inner line"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := (&TaskError{Title: "outer", Err: c.err, Tail: tail}).AsProblem()
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("AsProblem = %+v; want %+v", got, c.want)
			}
		})
	}
}

// TestLogKeepsTheLastLines: the tail and the live line.
func TestLogKeepsTheLastLines(t *testing.T) {
	newTestLog := func(t *testing.T, text string) *Log {
		t.Helper()
		l, err := newLog()
		if err != nil {
			t.Fatalf("newLog: %v", err)
		}
		t.Cleanup(l.close)
		_, _ = fmt.Fprint(l, text)
		return l
	}
	// 25 finished lines and one still being written: the tail is the last 20
	// of the 26.
	var many strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&many, "line %d\n", i)
	}
	many.WriteString("line 26")
	wantMany := make([]string, 0, tailLines)
	for i := 7; i <= 26; i++ {
		wantMany = append(wantMany, fmt.Sprintf("line %d", i))
	}
	long := "a" + strings.Repeat("é", 600) // 1201 bytes: the cut at 1024 falls inside the 512th é
	for _, c := range []struct {
		name, text string
		tail       []string
		live       string
	}{
		{"empty", "", nil, ""},
		{"a line still being written", "one\ntwo", []string{"one", "two"}, "two"},
		{"a redraw keeps what followed the last carriage return", "10%\r20%\r30%\n", []string{"30%"}, "30%"},
		{"a trailing carriage return is not a redraw", "45%\r", []string{"45%"}, "45%"},
		{"CRLF", "one\r\ntwo\r\n", []string{"one", "two"}, "two"},
		{"blank lines do not count", "one\n\n   \n\x1b[2K\n", []string{"one"}, "one"},
		{"both are raw", "\x1b[31mred\x1b[0m\tx\n", []string{"\x1b[31mred\x1b[0m\tx"}, "\x1b[31mred\x1b[0m\tx"},
		{"only the last 20 lines", many.String(), wantMany, "line 26"},
		{"a line is cut at 1 KiB on a rune boundary", long + "\n", []string{"a" + strings.Repeat("é", 511)}, "a" + strings.Repeat("é", 511)},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newTestLog(t, c.text)
			if got := l.Tail(); !reflect.DeepEqual(got, c.tail) && (len(got) != 0 || len(c.tail) != 0) {
				t.Errorf("Tail = %q; want %q", got, c.tail)
			}
			if got := l.liveLine(); got != c.live {
				t.Errorf("liveLine = %q; want %q", got, c.live)
			}
			// What the log holds is bounded too, not only what Tail returns.
			if len(l.lines) > tailLines {
				t.Errorf("the log holds %d finished lines; it keeps at most %d", len(l.lines), tailLines)
			}
		})
	}
}

// TestLogKeepsItsStateAcrossReads: what Log carries from one read to the
// next — a line still being written, a carriage return, a line already cut
// at 1 KiB — gives the same lines as one read would, whatever falls between
// two reads.
func TestLogKeepsItsStateAcrossReads(t *testing.T) {
	long := "a" + strings.Repeat("é", 600) // 1201 bytes: the cut at 1024 falls inside the 512th é
	cut := "a" + strings.Repeat("é", 511)
	for _, c := range []struct {
		name   string
		pieces []string // written one by one, the log read after each
		tail   []string
	}{
		{"a rune split between two reads", []string{long[:700], long[700:] + "\n"}, []string{cut}},
		{"a cut line goes on in the next read", []string{long[:1100], long[1100:] + "\nnext\n"}, []string{cut, "next"}},
		{"a redraw split between two reads", []string{"10%\r", "20%\n"}, []string{"20%"}},
		{"CRLF split between two reads", []string{"one\r", "\ntwo\n"}, []string{"one", "two"}},
		{"a line longer than one read", []string{strings.Repeat("x", 40000) + "\n"}, []string{strings.Repeat("x", lineCap)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, err := newLog()
			if err != nil {
				t.Fatalf("newLog: %v", err)
			}
			t.Cleanup(l.close)
			for _, p := range c.pieces {
				_, _ = fmt.Fprint(l, p)
				_ = l.liveLine()
			}
			got := l.Tail()
			if !reflect.DeepEqual(got, c.tail) {
				t.Errorf("Tail = %q; want %q", got, c.tail)
			}
			for _, line := range got {
				if !utf8.ValidString(line) {
					t.Errorf("a kept line is not valid UTF-8: %q", line)
				}
			}
		})
	}
}

// TestLogReadsWhatWasThereWhenAsked: a read takes what the file held when it
// began, not what a child keeps appending while it reads — a child that
// writes faster than the log is parsed would otherwise hold the ticker in
// one read for good, and with it the result line and a signal's drain.
func TestLogReadsWhatWasThereWhenAsked(t *testing.T) {
	l, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	t.Cleanup(l.close)
	chunk := strings.Repeat("y\n", 2048)
	_, _ = fmt.Fprint(l, chunk)
	reads := 0
	l.readHook = func() {
		// The child writes another chunk each time the log has read one.
		if reads++; reads < 100 {
			_, _ = fmt.Fprint(l, chunk)
		}
	}
	if line := l.liveLine(); line != "y" {
		t.Errorf("liveLine = %q; want %q", line, "y")
	}
	if reads > 2 {
		t.Errorf("one read of the log read %d times while the file grew; want it to stop at what was there", reads)
	}
}

// TestLogSkipsToTheEndOfABacklog: a read parses no more than readWindow of
// what is new, from the end — the tail and the live line are the log's last
// lines — so the time a read takes does not grow with how long a fast child
// has been writing. The line the window starts inside is not kept.
func TestLogSkipsToTheEndOfABacklog(t *testing.T) {
	l, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	t.Cleanup(l.close)
	_, _ = fmt.Fprint(l, strings.Repeat("x", 100)+"\n"+strings.Repeat("y\n", 1<<19)+"last\n")
	reads := 0
	l.readHook = func() { reads++ }
	if line := l.liveLine(); line != "last" {
		t.Errorf("liveLine = %q; want %q", line, "last")
	}
	if most := readWindow/(32*1024) + 1; reads > most {
		t.Errorf("the read parsed %d chunks of a 1 MiB backlog; want at most %d, the window's", reads, most)
	}
	tail := l.Tail()
	if len(tail) != tailLines || tail[len(tail)-1] != "last" || tail[0] != "y" {
		t.Errorf("Tail = %q; want %d lines ending in %q", tail, tailLines, "last")
	}
	for _, line := range tail {
		if line != "y" && line != "last" {
			t.Errorf("a line cut by the window was kept: %q", line)
		}
	}

	// Lines longer than the window holds twenty of: the line the window
	// starts inside would be kept as its padding alone, without its number.
	l2, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	t.Cleanup(l2.close)
	for i := 0; i < 40; i++ {
		_, _ = fmt.Fprintf(l2, "L%02d %s\n", i, strings.Repeat("p", 32*1024))
	}
	tail = l2.Tail()
	if len(tail) == 0 || !strings.HasPrefix(tail[len(tail)-1], "L39 ") {
		t.Fatalf("Tail ends %q; want the last line, L39", tail)
	}
	for _, line := range tail {
		if !strings.HasPrefix(line, "L") {
			t.Errorf("a line cut by the window was kept: %.20q…", line)
		}
	}
}

// TestLogIsAnUnlinkedFile: the log is a regular file that no longer has a
// name, and a child given File writes to it directly.
func TestLogIsAnUnlinkedFile(t *testing.T) {
	l, err := newLog()
	if err != nil {
		t.Fatalf("newLog: %v", err)
	}
	defer l.close()
	if _, err := os.Stat(l.File().Name()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the log's file is still at %s: %v", l.File().Name(), err)
	}
	child := exec.Command("sh", "-c", "echo out; echo err >&2")
	child.Stdout, child.Stderr = l.File(), l.File()
	if err := child.Run(); err != nil {
		t.Fatalf("child: %v", err)
	}
	if got, want := l.Tail(), []string{"out", "err"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Tail = %q; want %q", got, want)
	}
}

// TestRunWithoutATaskLog: when no task log can be made — $TMPDIR is missing,
// read-only or full — the tasks run all the same with their output
// discarded, and one warning says why: most tasks never read their log, and
// none is worth not stopping a workspace for.
func TestRunWithoutATaskLog(t *testing.T) {
	t.Setenv("TMPDIR", "/nonexistent/ws-run-test")
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 80, false, ColourNone, false), &fakeClock{}, nil)
	ran := 0
	err := r.run([]Task{
		{Title: "Stopping workspace", Run: func(log *Log) error {
			ran++
			_, _ = fmt.Fprintln(log, "not kept")
			return nil
		}},
		{Title: "Starting container", Run: func(log *Log) error {
			ran++
			_, _ = fmt.Fprintln(log, "not kept either")
			return errors.New("devpod up: exit status 1")
		}},
	})
	if ran != 2 {
		t.Errorf("%d of the 2 tasks ran; want both", ran)
	}
	var te *TaskError
	if !errors.As(err, &te) || te.Title != "Starting container" || len(te.Tail) != 0 {
		t.Errorf("run returned %#v; want the failed task's TaskError, with no tail", err)
	}
	got := buf.String()
	warning := "⚠ Step output is not kept: create the task log: "
	steps := "~ Stopping workspace\n✓ Stopping workspace  0.0s\n~ Starting container\n✗ Starting container  0.0s\n"
	if !strings.HasPrefix(got, warning) || !strings.HasSuffix(got, "\n"+steps) || strings.Count(got, "⚠") != 1 {
		t.Errorf("got\n%q\nwant one warning that starts %q, then\n%q", got, warning, steps)
	}
}

// TestElapsedText: the time a step line carries.
func TestElapsedText(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0s"},
		{400 * time.Millisecond, "0.4s"},
		{12345 * time.Millisecond, "12.3s"},
		{59960 * time.Millisecond, "59.9s"},
		{time.Minute, "1m00s"},
		{2*time.Minute + 13*time.Second, "2m13s"},
		{59*time.Minute + 59*time.Second + 999*time.Millisecond, "59m59s"},
		{time.Hour, "1h00m"},
		{time.Hour + 4*time.Minute + 59*time.Second, "1h04m"},
		{-time.Second, "0.0s"},
	} {
		if got := elapsedText(c.d); got != c.want {
			t.Errorf("elapsedText(%v) = %q; want %q", c.d, got, c.want)
		}
	}
}

// TestStepLineLayout: a step line wraps its title as a message does and puts
// its time two spaces after the title's last line, or on a line of its own.
func TestStepLineLayout(t *testing.T) {
	s := NewStreamAt(&bytes.Buffer{}, 30, false, ColourNone, false)
	for _, c := range []struct {
		name, title, suffix, want string
	}{
		{"fits", "Starting container", "2m13s", "✓ Starting container  2m13s"},
		{"wraps, the time after the last line", "Building proxy image with xray-core v26.2.6", "0.4s",
			"✓ Building proxy image with\n  xray-core v26.2.6  0.4s"},
		{"wraps, the time after a short last line", "Building the proxy image once more", "12.3s",
			"✓ Building the proxy image\n  once more  12.3s"},
		{"wraps, the time after another short last line", "Waiting for dev-proxy liveness", "12.3s",
			"✓ Waiting for dev-proxy\n  liveness  12.3s"},
		// Width 28 after the mark: a last line of 21 cells, two spaces and the
		// time's five fill it exactly; one cell more and the time takes a line
		// of its own.
		{"the time fits the last line exactly", "Building the proxy image once more for the day", "12.3s",
			"✓ Building the proxy image\n  once more for the day  12.3s"},
		{"one cell too many: the time on its own line", "Building the proxy image once more for the days", "12.3s",
			"✓ Building the proxy image\n  once more for the days\n  12.3s"},
		{"no time", "Fixing workspace routes", "", "✓ Fixing workspace routes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := stepLine(s, StateOK, c.title, c.suffix); got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
	own := stepLine(s, StateFail, strings.Repeat("x", 27), "12.3s")
	if want := "✗ " + strings.Repeat("x", 27) + "\n  12.3s"; own != want {
		t.Errorf("a title filling its last line: got\n%s\nwant\n%s", own, want)
	}
}

// TestFrameCleansTheLiveLine: the frame shows the log's line without its
// escapes — no ESC byte and no payload reaches the terminal under the frame.
func TestFrameCleansTheLiveLine(t *testing.T) {
	s := NewStreamAt(&bytes.Buffer{}, 80, true, ColourNone, false)
	lines := frameLines(s, "⠋", "Starting container", "0.0s", "\x1b[2Kinfo \x1b]0;pwned\x07building\x1b[1A")
	if len(lines) != 2 || lines[1] != "  info building" {
		t.Errorf("the live line is %q; want %q", lines[len(lines)-1], "  info building")
	}
}

// TestFrameLinesAreCutOneCellShort: both frame lines are laid out against
// the budget less one cell.
func TestFrameLinesAreCutOneCellShort(t *testing.T) {
	for w := MinWidth; w <= 200; w++ {
		s := NewStreamAt(&bytes.Buffer{}, w, true, ColourNone, false)
		lines := frameLines(s, "⠋", fxToken200, "1m12s", fxToken200)
		for i, line := range lines {
			if got := W(line); got != w-1 {
				t.Fatalf("@%d frame line %d is %d cells; want %d, one less than the budget: %q", w, i+1, got, w-1, line)
			}
		}
	}
}

// TestRunDrainOrdersALateMessage: a message that reaches the queue while the
// owner is writing a batch is written in the next batch, and a message that
// arrives once the gate has closed is written directly — after every drained
// one.
func TestRunDrainOrdersALateMessage(t *testing.T) {
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 80, false, ColourNone, false), &fakeClock{}, nil)
	batches := 0
	r.hooks.drainTook = func() {
		if batches++; batches == 1 {
			Info("arrived while the first batch was taken")
		}
	}
	_ = r.run([]Task{{Title: "Recreating container", Run: func(*Log) error {
		Info("queued in the task")
		return nil
	}}})
	_, direct := capture(t, func() { Info("written after Run returned") })
	want := "~ Recreating container\n✓ Recreating container  0.0s\n" +
		"queued in the task\narrived while the first batch was taken\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if direct != "written after Run returned\n" {
		t.Errorf("a message after Run returned was not written directly: %q", direct)
	}
	if batches != 2 {
		t.Errorf("the drain took %d batches; want 2", batches)
	}
}

// blockingWriter blocks the write that contains trigger until release is
// closed, and records every write in order.
type blockingWriter struct {
	syncBuffer
	trigger string
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.trigger) {
		w.once.Do(func() {
			close(w.blocked)
			<-w.release
		})
	}
	return w.syncBuffer.Write(p)
}

// TestRunWaitsForTheTickerBeforeTheResultLine: a redraw in flight when the
// task returns is finished before the frame is erased, so the result line
// never lands inside a frame.
func TestRunWaitsForTheTickerBeforeTheResultLine(t *testing.T) {
	w := &blockingWriter{trigger: "⠙", blocked: make(chan struct{}), release: make(chan struct{})}
	c := &fakeClock{}
	ticks := make(chan time.Time)
	r := testRunner(NewStreamAt(w, 40, true, ColourNone, false), c, ticks)
	draining := make(chan struct{})
	r.hooks.draining = func() { close(draining) }
	done := make(chan error)
	go func() {
		done <- r.run([]Task{{Title: "Starting proxy", Run: func(*Log) error {
			ticks <- time.Time{} // the redraw that blocks
			<-w.blocked
			return nil
		}}})
	}()
	// The redraw is blocked and the owner has begun its drain: from here,
	// nothing may be written until the redraw is.
	waitOrFail(t, draining, "the owner's drain")
	select {
	case <-done:
		t.Fatal("run returned while a redraw was still being written")
	case <-time.After(50 * time.Millisecond):
	}
	if got := w.String(); strings.Contains(got, "✓") {
		t.Fatalf("the result line was written while a redraw was still being written: %q", got)
	}
	close(w.release)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "⠋ Starting proxy  0.0s\r\x1b[J⠙ Starting proxy  0.0s\r\x1b[J✓ Starting proxy  0.0s\n"
	if got := w.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestRunUnderLoad runs the ticker and the log against a busy child, for
// -race: a child printing without pause into the log while the task writes
// a warning in a loop and the frame redraws every millisecond. Every warning
// comes out once, in order, after the result line. The interleavings of the
// queue, the gate and a signal are driven one by one by the tests above.
func TestRunUnderLoad(t *testing.T) {
	var buf syncBuffer
	r := &runner{
		s:   NewStreamAt(&buf, 80, true, ColourNone, false),
		now: time.Now,
		tick: func() (<-chan time.Time, func()) {
			tk := time.NewTicker(time.Millisecond)
			return tk.C, tk.Stop
		},
	}
	const warnings = 500
	err := r.run([]Task{{Title: "Starting container", Run: func(log *Log) error {
		child := exec.Command("sh", "-c", "while :; do echo tick; done")
		child.Stdout, child.Stderr = log.File(), log.File()
		if err := child.Start(); err != nil {
			return err
		}
		for i := 0; i < warnings; i++ {
			Warn(fmt.Sprintf("warning %d", i))
			if i%50 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
		_ = child.Process.Kill()
		_ = child.Wait()
		return nil
	}}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := buf.String()
	result := strings.Index(got, "✓ Starting container")
	if result < 0 {
		t.Fatalf("no result line:\n%s", got)
	}
	after := got[result:]
	last := -1
	for i := 0; i < warnings; i++ {
		line := fmt.Sprintf("⚠ warning %d\n", i)
		at := strings.Index(after, line)
		if at < 0 || at < last {
			t.Fatalf("warning %d is missing or out of order after the result line", i)
		}
		if n := strings.Count(after, line); n != 1 {
			t.Fatalf("warning %d came out %d times; want once", i, n)
		}
		last = at
	}
	if n := strings.Count(got, "⚠ warning"); n != warnings {
		t.Errorf("%d warnings came out; want %d", n, warnings)
	}
	if strings.Contains(got[:result], "warning") {
		t.Error("a warning was written while the task ran")
	}
}

// --------------------------------------------- the runner's contract probes

// runnerProbes is the step runner's contract, planted against by
// TestContractMutationHarness in mutation_test.go and run clean by
// TestRunnerContract.
func runnerProbes() []contractProbe {
	return []contractProbe{probeRunnerStreams, probeRunnerTTYGate, probeRunnerQueue, probeRunnerResultLine}
}

// TestRunnerContract runs the runner's probes on the shipped code.
func TestRunnerContract(t *testing.T) {
	for _, p := range runnerProbes() {
		t.Run(p.name, func(t *testing.T) {
			r := newResults()
			p.run(t, r)
			r.report(t)
		})
	}
}

// probeRunnerStreams: on a terminal the frame and the lines go to the
// runner's stream — stderr — and nothing to stdout.
var probeRunnerStreams = contractProbe{
	name: "runner_streams",
	spec: "§4.7",
	what: "the runner draws its frame and writes its lines on stderr, and writes nothing to stdout",
	run: func(t *testing.T, r *results) string {
		stdout, stderr := capture(t, func() {
			ticks, redrawn := make(chan time.Time), make(chan struct{})
			rn := testRunner(NewStreamAt(stdWriter{err: true}, 80, true, ColourNone, false), &fakeClock{}, ticks)
			rn.hooks.redrawn = func() { redrawn <- struct{}{} }
			_ = rn.run([]Task{{Title: "Stopping workspace", Run: func(*Log) error {
				ticks <- time.Time{}
				<-redrawn
				return nil
			}}})
		})
		if stdout != "" {
			r.fail("runner_streams", "the runner wrote %q to stdout; stdout is the answer's", stdout)
		}
		if !strings.Contains(stderr, "\r\x1b[J⠙ Stopping workspace") || !strings.HasSuffix(stderr, "✓ Stopping workspace  0.0s\n") {
			r.fail("runner_streams", "stderr does not hold the redrawn frame and the result line: %q", stderr)
		}
		return fmt.Sprintf("stdout=%q stderr=%q", stdout, stderr)
	},
}

// probeRunnerTTYGate: off a terminal the runner writes plain lines and not
// one ESC byte.
var probeRunnerTTYGate = contractProbe{
	name: "runner_tty_gate",
	spec: "§4.7",
	what: "off a terminal the runner writes a start line and a result line and no escape byte",
	run: func(t *testing.T, r *results) string {
		var buf syncBuffer
		rn := testRunner(NewStreamAt(&buf, 80, false, ColourNone, false), &fakeClock{}, nil)
		_ = rn.run([]Task{{Title: "Stopping workspace", Run: ok}})
		out := buf.String()
		if n := strings.Count(out, "\x1b"); n > 0 {
			r.fail("runner_tty_gate", "off a terminal the runner wrote %d ESC bytes: %q", n, out)
		}
		if want := "~ Stopping workspace\n✓ Stopping workspace  0.0s\n"; out != want {
			r.fail("runner_tty_gate", "off a terminal the runner wrote %q; want %q", out, want)
		}
		return out
	},
}

// probeRunnerQueue: a message written during a task comes out after the
// task's result line, on the process's stderr.
var probeRunnerQueue = contractProbe{
	name: "runner_queue",
	spec: "§4.7",
	what: "a message a task writes is written after the task's result line",
	run: func(t *testing.T, r *results) string {
		_, stderr := capture(t, func() {
			rn := testRunner(NewStreamAt(stdWriter{err: true}, 80, false, ColourNone, false), &fakeClock{}, nil)
			_ = rn.run([]Task{{Title: "Deleting workspace", Run: func(*Log) error {
				Warn("devpod delete: exit status 1")
				return nil
			}}})
		})
		result := strings.Index(stderr, "✓ Deleting workspace")
		warning := strings.Index(stderr, "devpod delete: exit status 1")
		if result < 0 || warning < result {
			r.fail("runner_queue", "the warning is not after the task's result line: %q", stderr)
		}
		return stderr
	},
}

// probeRunnerResultLine: a failed task's result line carries no error text;
// the root prints the error, once.
var probeRunnerResultLine = contractProbe{
	name: "runner_result_line",
	spec: "§4.7 / §4.8",
	what: "a failed task's result line carries no error text",
	run: func(t *testing.T, r *results) string {
		var buf syncBuffer
		rn := testRunner(NewStreamAt(&buf, 80, false, ColourNone, false), &fakeClock{}, nil)
		_ = rn.run([]Task{{Title: "Stopping workspace", Run: func(*Log) error {
			return errors.New("devpod stop: exit status 1")
		}}})
		out := buf.String()
		if strings.Contains(out, "exit status 1") {
			r.fail("runner_result_line", "the result line carries the error text, which the root prints: %q", out)
		}
		return out
	},
}

package output

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// interruptibleRunner is a terminal runner whose signals are the channel it
// returns and whose die reports the signal on died and returns.
func interruptibleRunner(buf *syncBuffer) (r *runner, sigint chan os.Signal, died chan os.Signal) {
	return signalledRunner(NewStreamAt(buf, 60, true, ColourNone, false))
}

// signalledRunner is a runner over s whose signals are the channel it returns
// and whose die reports the signal on died and returns.
func signalledRunner(s *Stream) (r *runner, sigs chan os.Signal, died chan os.Signal) {
	r = testRunner(s, &fakeClock{}, make(chan time.Time))
	sigs, died = make(chan os.Signal, 2), make(chan os.Signal, 2)
	r.signals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	r.die = func(sig os.Signal) { died <- sig }
	return r, sigs, died
}

func waitOrFail(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// waitForDeath is the signal die was called with.
func waitForDeath(t *testing.T, died <-chan os.Signal, what string) os.Signal {
	t.Helper()
	select {
	case sig := <-died:
		return sig
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// TestInterruptDrainsTheRunningTask: SIGINT while a task runs drains it with
// the interrupted line and its queue, then dies; no later task starts, and
// the owner, once the task returns, reports the interrupt.
func TestInterruptDrainsTheRunningTask(t *testing.T) {
	var buf syncBuffer
	r, sigint, died := interruptibleRunner(&buf)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error)
	go func() {
		done <- r.run([]Task{
			{Title: "Starting container", Run: func(*Log) error {
				Warn("queued before the interrupt")
				close(started)
				<-release
				return nil
			}},
			{Title: "Fixing routes", Run: func(*Log) error {
				t.Error("a task started after the interrupt")
				return nil
			}},
		})
	}()
	waitOrFail(t, started, "the task to start")
	sigint <- os.Interrupt
	if sig := waitForDeath(t, died, "die"); sig != os.Interrupt {
		t.Errorf("die was called with %v; want the signal caught", sig)
	}
	close(release)
	if err := <-done; !errors.Is(err, errInterrupted) {
		t.Errorf("run returned %v; want the interrupt", err)
	}
	want := "⠋ Starting container  0.0s\r\x1b[J" +
		"✗ Starting container  interrupted after 0.0s\n" +
		"⚠ queued before the interrupt\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestInterruptWaitsForTheOwnersDrain: SIGINT that arrives once the owner
// has begun its drain waits for it, writes no second result line, and dies.
func TestInterruptWaitsForTheOwnersDrain(t *testing.T) {
	var buf syncBuffer
	r, sigint, died := interruptibleRunner(&buf)
	waiting := make(chan struct{}, 1)
	r.hooks.watcherWaits = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	r.hooks.draining = func() {
		sigint <- os.Interrupt
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			t.Error("the interrupt did not wait for the owner's drain")
		}
	}
	err := r.run([]Task{{Title: "Stopping workspace", Run: func(*Log) error {
		Warn("written by the owner's drain")
		return nil
	}}})
	waitForDeath(t, died, "die")
	if err != nil {
		t.Errorf("run returned %v; the task had finished before the interrupt", err)
	}
	want := "⠋ Stopping workspace  0.0s\r\x1b[J" +
		"✓ Stopping workspace  0.0s\n" +
		"⚠ written by the owner's drain\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestASignalBetweenTasksStartsNothing: a signal caught while the owner
// drains a task that another follows starts no further task and draws none
// of it — whichever of the owner and the interrupt takes the lock first once
// the drain is written.
func TestASignalBetweenTasksStartsNothing(t *testing.T) {
	var buf syncBuffer
	r, sigint, died := interruptibleRunner(&buf)
	waiting := make(chan struct{}, 1)
	r.hooks.watcherWaits = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	first := true
	r.hooks.draining = func() {
		if !first {
			return
		}
		first = false
		sigint <- os.Interrupt
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			t.Error("the interrupt did not wait for the owner's drain")
		}
	}
	err := r.run([]Task{
		{Title: "Stopping workspace", Run: ok},
		{Title: "Starting container", Run: func(*Log) error {
			t.Error("a task started after the signal")
			return nil
		}},
	})
	waitForDeath(t, died, "die")
	if !errors.Is(err, errInterrupted) {
		t.Errorf("run returned %v; want the interrupt", err)
	}
	if want := "⠋ Stopping workspace  0.0s\r\x1b[J✓ Stopping workspace  0.0s\n"; buf.String() != want {
		t.Errorf("got\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestAClaimAfterASignalIsRefused: once a signal is caught no task is
// claimed, even while the interrupt still waits for the owner's drain to be
// written — the order in which the two take the lock afterwards does not
// matter.
func TestAClaimAfterASignalIsRefused(t *testing.T) {
	var buf syncBuffer
	r, _, died := interruptibleRunner(&buf)
	r.cond, r.dead = sync.NewCond(&r.mu), make(chan struct{})
	waiting := make(chan struct{}, 1)
	r.hooks.watcherWaits = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	r.state = taskDraining
	go r.interrupt(os.Interrupt)
	waitOrFail(t, waiting, "the interrupt to wait for the drain")
	// The drain is written; the interrupt has not taken the lock again.
	r.mu.Lock()
	r.state = taskClosed
	r.mu.Unlock()
	if r.claim() {
		t.Error("a task was claimed after a signal was caught")
	}
	r.mu.Lock()
	r.state = taskClosed
	r.cond.Broadcast()
	r.mu.Unlock()
	waitForDeath(t, died, "die")
}

// TestASignalWhileATaskStartsWaitsForItsFrame: a signal that comes while the
// owner writes a task's first frame waits for it, then erases it and writes
// the interrupted line — the frame is never left on screen.
func TestASignalWhileATaskStartsWaitsForItsFrame(t *testing.T) {
	var buf syncBuffer
	r, sigint, died := interruptibleRunner(&buf)
	waiting := make(chan struct{}, 1)
	r.hooks.watcherWaits = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	r.hooks.starting = func() {
		sigint <- os.Interrupt
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			t.Error("the signal did not wait for the task's start")
		}
	}
	release, done := make(chan struct{}), make(chan error)
	go func() {
		done <- r.run([]Task{{Title: "Starting container", Run: func(*Log) error {
			<-release
			return nil
		}}})
	}()
	waitForDeath(t, died, "die")
	close(release)
	if err := <-done; !errors.Is(err, errInterrupted) {
		t.Errorf("run returned %v; want the interrupt", err)
	}
	if want := "⠋ Starting container  0.0s\r\x1b[J✗ Starting container  interrupted after 0.0s\n"; buf.String() != want {
		t.Errorf("got\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestASecondSignalEndsAStuckDrain: while the first signal's drain is stuck
// behind a stream that does not take it, a second signal ends the process
// once the grace is out.
func TestASecondSignalEndsAStuckDrain(t *testing.T) {
	w := &blockingWriter{trigger: "interrupted after", blocked: make(chan struct{}), release: make(chan struct{})}
	r, sigs, died := signalledRunner(NewStreamAt(w, 60, true, ColourNone, false))
	r.grace = 50 * time.Millisecond
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error)
	go func() {
		done <- r.run([]Task{{Title: "Starting container", Run: func(*Log) error {
			close(started)
			<-release
			return nil
		}}})
	}()
	waitOrFail(t, started, "the task to start")
	sigs <- os.Interrupt
	waitOrFail(t, w.blocked, "the interrupted line to block")
	sigs <- syscall.SIGTERM
	if sig := waitForDeath(t, died, "the second signal's death"); sig != syscall.SIGTERM {
		t.Errorf("die was called with %v first; want the second signal, once the grace is out", sig)
	}
	close(w.release)
	waitForDeath(t, died, "the first signal's death")
	close(release)
	if err := <-done; !errors.Is(err, errInterrupted) {
		t.Errorf("run returned %v; want the interrupt", err)
	}
}

// TestACompanionSignalLetsTheDrainFinish: a second signal right after the
// first — one event sending two — waits for the drain, and the process dies
// of the first.
func TestACompanionSignalLetsTheDrainFinish(t *testing.T) {
	var buf syncBuffer
	r, sigs, died := interruptibleRunner(&buf)
	r.grace = 5 * time.Second
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error)
	go func() {
		done <- r.run([]Task{{Title: "Starting container", Run: func(*Log) error {
			Warn("queued before the signals")
			close(started)
			<-release
			return nil
		}}})
	}()
	waitOrFail(t, started, "the task to start")
	sigs <- syscall.SIGTERM
	sigs <- syscall.SIGHUP
	if sig := waitForDeath(t, died, "die"); sig != syscall.SIGTERM {
		t.Errorf("die was called with %v first; want the first signal, once its drain was written", sig)
	}
	close(release)
	if err := <-done; !errors.Is(err, errInterrupted) {
		t.Errorf("run returned %v; want the interrupt", err)
	}
	want := "⠋ Starting container  0.0s\r\x1b[J✗ Starting container  interrupted after 0.0s\n⚠ queued before the signals\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestASignalAsRunFinishesIsNotDropped: a signal already delivered when Run
// finishes is handled — the process dies of it — rather than lost to the
// watcher's exit. Both are ready at once, so a watcher that chose between
// them would drop about one in two; it runs 64 times.
func TestASignalAsRunFinishesIsNotDropped(t *testing.T) {
	for i := 0; i < 64; i++ {
		var buf syncBuffer
		r, _, died := interruptibleRunner(&buf)
		r.cond, r.dead = sync.NewCond(&r.mu), make(chan struct{})
		sigs, quit := make(chan os.Signal, 1), make(chan struct{})
		sigs <- syscall.SIGHUP
		close(quit)
		r.watch(sigs, quit)
		select {
		case sig := <-died:
			if sig != syscall.SIGHUP {
				t.Fatalf("die was called with %v; want the signal delivered", sig)
			}
		default:
			t.Fatalf("run %d: a signal delivered as Run finished was dropped", i)
		}
	}
}

// TestASignalOffATerminalDrainsTheQueue: off a terminal a signal is caught
// too: the task's queue is written after its interrupted line, in plain
// lines, and the process dies of that signal.
func TestASignalOffATerminalDrainsTheQueue(t *testing.T) {
	var buf syncBuffer
	r, sigs, died := signalledRunner(NewStreamAt(&buf, 60, false, ColourNone, false))
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error)
	go func() {
		done <- r.run([]Task{{Title: "Rebuilding proxy", Run: func(*Log) error {
			Warn("queued before the signal")
			close(started)
			<-release
			return nil
		}}})
	}()
	waitOrFail(t, started, "the task to start")
	sigs <- syscall.SIGTERM
	if sig := waitForDeath(t, died, "die"); sig != syscall.SIGTERM {
		t.Errorf("die was called with %v; want the signal caught", sig)
	}
	close(release)
	if err := <-done; !errors.Is(err, errInterrupted) {
		t.Errorf("run returned %v; want the interrupt", err)
	}
	want := "~ Rebuilding proxy\n✗ Rebuilding proxy  interrupted after 0.0s\n⚠ queued before the signal\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

const (
	sigintChildEnv  = "WS_TEST_RUN_SIGINT_CHILD"
	sigintMarkerEnv = "WS_TEST_RUN_SIGINT_MARKER"
)

// TestRunSIGINTChild is the far side of TestSIGINT: a runner on a terminal
// stream over the real stderr, with the real signals, whose task starts a
// child that traps SIGINT, keeps writing for a second and then writes a
// marker file. WS_TEST_RUN_SIGINT_CHILD=pipe gives the child a pipe instead
// of the task's log, =plain runs the runner off a terminal, and =after runs
// one task to its end and then waits five seconds with Run returned.
func TestRunSIGINTChild(t *testing.T) {
	mode := os.Getenv(sigintChildEnv)
	if mode == "" {
		t.Skip("child half of TestSIGINT; runs only in the subprocess")
	}
	if mode == "after" {
		r := newRunner(NewStreamAt(stdWriter{err: true}, 80, true, ColourNone, false))
		if err := r.run([]Task{{Title: "Checking workspace", Run: ok}}); err != nil {
			fmt.Fprintf(os.Stderr, "RUN-ERROR %v\n", err)
			os.Exit(3)
		}
		fmt.Println("READY")
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	r := newRunner(NewStreamAt(stdWriter{err: true}, 80, mode != "plain", ColourNone, false))
	err := r.run([]Task{{Title: "Starting container", Run: func(log *Log) error {
		child := exec.Command("sh", "-c", `trap 'echo cleaning up' INT
i=0
while [ $i -lt 20 ]; do echo "line $i"; i=$((i+1)); sleep 0.05; done
echo done > "$WS_TEST_RUN_SIGINT_MARKER"`)
		child.Stdout, child.Stderr = log.File(), log.File()
		// The child's first line says its trap is set: SIGINT before that
		// would end it however it writes.
		trapped := func() {
			for len(log.Tail()) == 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if mode == "pipe" {
			pr, pw, err := os.Pipe()
			if err != nil {
				return err
			}
			defer func() { _ = pr.Close() }()
			child.Stdout, child.Stderr = pw, pw
			defer func() { _ = pw.Close() }()
			trapped = func() { _, _ = bufio.NewReader(pr).ReadString('\n') }
		}
		if err := child.Start(); err != nil {
			return err
		}
		trapped()
		Warn("queued before the interrupt")
		fmt.Println("READY")
		return child.Wait()
	}}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "RUN-ERROR %v\n", err)
		os.Exit(3)
	}
	os.Exit(0)
}

// runSIGINTChild starts TestRunSIGINTChild in a process group of its own,
// sends the group SIGINT — what a terminal does on Ctrl-C — once the task is
// running, and returns how the process ended, its stderr, and whether the
// child it started wrote its marker.
func runSIGINTChild(t *testing.T, mode string, ignored bool) (status syscall.WaitStatus, stderr string, survived bool) {
	t.Helper()
	return runSignalChild(t, mode, ignored, syscall.SIGINT, true, false)
}

// runSignalChild is runSIGINTChild with the signal, whether it goes to the
// whole group or to the process alone — what `kill <pid>` does — and
// whether stderr's reader is gone before it is sent, as `| tee` is after a
// Ctrl-C.
func runSignalChild(t *testing.T, mode string, ignored bool, sig syscall.Signal, group, readerGone bool) (status syscall.WaitStatus, stderr string, survived bool) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "survived")
	var cmd *exec.Cmd
	if ignored {
		// A process started with SIGINT ignored — `ws … &` from a script.
		cmd = exec.Command("sh", "-c", `trap '' INT; exec "$0" -test.run='^TestRunSIGINTChild$'`, os.Args[0])
	} else {
		cmd = exec.Command(os.Args[0], "-test.run=^TestRunSIGINTChild$")
	}
	cmd.Env = append(os.Environ(), sigintChildEnv+"="+mode, sigintMarkerEnv+"="+marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var errBuf syncBuffer
	var errPipe io.ReadCloser
	if readerGone {
		if errPipe, err = cmd.StderrPipe(); err != nil {
			t.Fatalf("stderr pipe: %v", err)
		}
	} else {
		cmd.Stderr = &errBuf
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if sc.Text() == "READY" {
				close(ready)
				break
			}
		}
		for sc.Scan() {
		}
	}()
	waitOrFail(t, ready, "the task to start")
	if readerGone {
		_ = errPipe.Close()
	}
	target := cmd.Process.Pid
	if group {
		target = -target
	}
	if err := syscall.Kill(target, sig); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("the process did not end after SIGINT; stderr:\n%s", errBuf.String())
	}
	status = cmd.ProcessState.Sys().(syscall.WaitStatus)
	if mode == "after" {
		return status, errBuf.String(), false
	}
	// The child writes for a second after SIGINT and then its marker.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return status, errBuf.String(), true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return status, errBuf.String(), false
}

// interruptedTail is how the runner's stderr ends after SIGINT: the result
// line and the queue, after the last frame.
var interruptedTail = regexp.MustCompile(`\r\x1b\[(1A\x1b\[)?J✗ Starting container  interrupted after \d+\.\ds\n` +
	`⚠ queued before the interrupt\n$`)

// interruptedPlain is how it ends off a terminal: the start line, the result
// line and the queue, and nothing else.
var interruptedPlain = regexp.MustCompile(`^~ Starting container\n✗ Starting container  interrupted after \d+\.\ds\n` +
	`⚠ queued before the interrupt\n$`)

// TestSIGINT: SIGINT on a terminal drains the running task with the
// interrupted line and its queue, and the process dies of the signal; the
// child it interrupted keeps writing to the log, which is a file, and ends
// on its own. A child given a pipe instead dies of SIGPIPE with the runner —
// the control that shows the marker can be missing. Off a terminal the
// queue is drained the same way, in plain lines; so it is for SIGTERM sent
// to the process alone. Once Run has returned SIGINT kills as it always
// did. A process started with SIGINT ignored catches nothing, and its task
// runs to the end.
func TestSIGINT(t *testing.T) {
	t.Run("the log", func(t *testing.T) {
		status, stderr, survived := runSIGINTChild(t, "log", false)
		if !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Errorf("the process ended with %v; want death by SIGINT\n%s", status, stderr)
		}
		if !interruptedTail.MatchString(stderr) {
			t.Errorf("stderr does not end with the interrupted line and the queue:\n%q", stderr)
		}
		if !survived {
			t.Error("the child did not write its marker after the runner died")
		}
	})
	t.Run("control: a pipe", func(t *testing.T) {
		status, stderr, survived := runSIGINTChild(t, "pipe", false)
		if !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Errorf("the process ended with %v; want death by SIGINT\n%s", status, stderr)
		}
		if survived {
			t.Error("a child writing to a pipe outlived the runner; the marker check cannot see a SIGPIPE")
		}
	})
	t.Run("off a terminal", func(t *testing.T) {
		status, stderr, _ := runSIGINTChild(t, "plain", false)
		if !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Errorf("the process ended with %v; want death by SIGINT\n%s", status, stderr)
		}
		if !interruptedPlain.MatchString(stderr) {
			t.Errorf("stderr is not the start line, the interrupted line and the queue:\n%q", stderr)
		}
	})
	t.Run("SIGTERM to the process", func(t *testing.T) {
		status, stderr, _ := runSignalChild(t, "log", false, syscall.SIGTERM, false, false)
		if !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Errorf("the process ended with %v; want death by SIGTERM\n%s", status, stderr)
		}
		if !interruptedTail.MatchString(stderr) {
			t.Errorf("stderr does not end with the interrupted line and the queue:\n%q", stderr)
		}
	})
	t.Run("off a terminal, the pipe's reader gone", func(t *testing.T) {
		// The drain's writes fail with EPIPE rather than kill the process
		// with SIGPIPE: it dies of the signal it caught.
		status, _, _ := runSignalChild(t, "plain", false, syscall.SIGINT, true, true)
		if !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Errorf("the process ended with %v; want death by SIGINT", status)
		}
	})
	t.Run("after Run returned", func(t *testing.T) {
		// Run gave SIGINT its default disposition back: the process dies of
		// it, where a handler left behind would swallow it and exit 0.
		status, stderr, _ := runSIGINTChild(t, "after", false)
		if !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Errorf("the process ended with %v; want death by SIGINT\n%s", status, stderr)
		}
	})
	t.Run("SIGINT ignored", func(t *testing.T) {
		status, stderr, survived := runSIGINTChild(t, "log", true)
		if status.Signaled() || status.ExitStatus() != 0 {
			t.Errorf("the process ended with %v; want exit 0\n%s", status, stderr)
		}
		if strings.Contains(stderr, "interrupted") || !strings.Contains(stderr, "✓ Starting container") {
			t.Errorf("the task did not run to the end:\n%q", stderr)
		}
		if !survived {
			t.Error("the child did not write its marker")
		}
	})
}

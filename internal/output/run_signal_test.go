package output

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// interruptibleRunner is a terminal runner whose SIGINT is the channel it
// returns and whose die reports on died and returns.
func interruptibleRunner(buf *syncBuffer) (r *runner, sigint chan os.Signal, died chan struct{}) {
	r = testRunner(NewStreamAt(buf, 60, true, ColourNone, false), &fakeClock{}, make(chan time.Time))
	sigint, died = make(chan os.Signal, 1), make(chan struct{}, 1)
	r.signals = func() (<-chan os.Signal, func()) { return sigint, func() {} }
	r.die = func() { died <- struct{}{} }
	return r, sigint, died
}

func waitOrFail(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
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
	waitOrFail(t, died, "die")
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
	waitOrFail(t, died, "die")
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

// TestNothingIsCaughtOffATerminal: off a terminal SIGINT is left alone.
func TestNothingIsCaughtOffATerminal(t *testing.T) {
	var buf syncBuffer
	r := testRunner(NewStreamAt(&buf, 60, false, ColourNone, false), &fakeClock{}, nil)
	r.signals = func() (<-chan os.Signal, func()) {
		t.Error("SIGINT is caught off a terminal")
		return nil, func() {}
	}
	_ = r.run([]Task{{Title: "Stopping workspace", Run: ok}})
}

const (
	sigintChildEnv  = "WS_TEST_RUN_SIGINT_CHILD"
	sigintMarkerEnv = "WS_TEST_RUN_SIGINT_MARKER"
)

// TestRunSIGINTChild is the far side of TestSIGINT: a runner on a terminal
// stream over the real stderr, with the real SIGINT, whose task starts a
// child that traps SIGINT, keeps writing for a second and then writes a
// marker file. WS_TEST_RUN_SIGINT_CHILD=pipe gives the child a pipe instead
// of the task's log.
func TestRunSIGINTChild(t *testing.T) {
	mode := os.Getenv(sigintChildEnv)
	if mode == "" {
		t.Skip("child half of TestSIGINT; runs only in the subprocess")
	}
	r := newRunner(NewStreamAt(stdWriter{err: true}, 80, true, ColourNone, false))
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
	cmd.Stderr = &errBuf
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
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
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

// TestSIGINT: SIGINT on a terminal drains the running task with the
// interrupted line and its queue, and the process dies of the signal; the
// child it interrupted keeps writing to the log, which is a file, and ends
// on its own. A child given a pipe instead dies of SIGPIPE with the runner —
// the control that shows the marker can be missing. A process started with
// SIGINT ignored catches nothing, and its task runs to the end.
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

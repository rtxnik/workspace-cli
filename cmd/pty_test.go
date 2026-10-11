//go:build linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// A real prompt on a pty (§4.8 of the phase-6 design): the child runs the
// real Execute — TestExecuteChildProcess — with a pty's slave as its
// controlling terminal, stdin and stderr, and a pipe as stdout. The test
// reads the master, answers the terminal's queries, waits for the prompt and
// sends it a key or the process a signal. It pins what the in-process tests
// cannot: huh's and Bubble Tea's real behaviour under the core.

// ptyFakeDevpod is the devpod on the pty child's PATH: it lists and states
// the workspaces as the fixture's does, and logs every other call to
// $FAKE_DEVPOD_LOG.
const ptyFakeDevpod = `#!/bin/sh
case "$1" in
list) echo '[{"id":"api"},{"id":"web-frontend"},{"id":"ml-training"},{"id":"ops"}]' ;;
status) echo "info Workspace '$2' is 'Running'" ;;
*) echo "$*" >> "$FAKE_DEVPOD_LOG" ;;
esac
`

// ioctl runs one ioctl on f's descriptor without putting it in blocking
// mode, so that a read of f can still be interrupted by its Close.
func ioctl(t *testing.T, f *os.File, req uintptr, arg unsafe.Pointer) {
	t.Helper()
	rc, err := f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		t.Fatal(err)
	}
	if errno != 0 {
		t.Fatalf("ioctl %#x on %s: %v", req, f.Name(), errno)
	}
}

// openPty opens a pty pair, its window 80 by 24, with the standard
// library's ioctls; /dev/ptmx is a requirement of the suite already.
func openPty(t *testing.T) (master *os.File, slavePath string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening /dev/ptmx: %v", err)
	}
	var n uint32
	ioctl(t, m, syscall.TIOCGPTN, unsafe.Pointer(&n))
	var unlock int32
	ioctl(t, m, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock))
	ws := struct{ row, col, x, y uint16 }{24, 80, 0, 0}
	ioctl(t, m, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
	return m, "/dev/pts/" + strconv.Itoa(int(n))
}

// slaveState reads the terminal state of the pty's slave.
func slaveState(t *testing.T, path string) syscall.Termios {
	t.Helper()
	s, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var st syscall.Termios
	ioctl(t, s, syscall.TCGETS, unsafe.Pointer(&st))
	return st
}

// ptyRun is one child on a pty: what it drew on the pty, how it ended, what
// it wrote on stdout.
type ptyRun struct {
	t      *testing.T
	master *os.File
	slave  string
	before syscall.Termios // the slave's state before the child started
	cmd    *exec.Cmd
	stdout bytes.Buffer
	mu     sync.Mutex
	screen bytes.Buffer
	// drained is closed when the reader has taken the end of the pty: every
	// byte the child drew is on the screen.
	drained chan struct{}
}

// startPtyChild starts args in a child over fx with TERM term, the pty's
// slave as its terminal; the master is read, and the terminal's colour and
// cursor queries answered, until the child has gone.
func startPtyChild(t *testing.T, fx streamsFixture, term string, args ...string) *ptyRun {
	t.Helper()
	argv, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	master, slavePath := openPty(t)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := &ptyRun{t: t, master: master, slave: slavePath, before: slaveState(t, slavePath), drained: make(chan struct{})}
	r.cmd = exec.Command(os.Args[0], "-test.run=^TestExecuteChildProcess$")
	r.cmd.Env = []string{
		executeChildEnv + "=1",
		executeArgsEnv + "=" + string(argv),
		"PATH=" + fx.bin,
		"HOME=" + fx.home,
		"WORKSPACES_DIR=" + filepath.Join(fx.home, "workspaces"),
		"FAKE_DEVPOD_LOG=" + filepath.Join(fx.home, "devpod.log"),
		"DOCKER_HOST=unix://" + childHome + "/docker.sock",
		"TERM=" + term,
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"TMPDIR=" + os.TempDir(),
		"GORACE=atexit_sleep_ms=0",
	}
	if v, ok := os.LookupEnv("GOCOVERDIR"); ok {
		r.cmd.Env = append(r.cmd.Env, "GOCOVERDIR="+v)
	}
	r.cmd.Stdin, r.cmd.Stderr, r.cmd.Stdout = slave, slave, &r.stdout
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	t.Cleanup(func() {
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		_ = master.Close()
	})
	go func() {
		defer close(r.drained)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			chunk := buf[:n]
			r.mu.Lock()
			r.screen.Write(chunk)
			r.mu.Unlock()
			if bytes.Contains(chunk, []byte("\x1b]11;?")) {
				_, _ = master.Write([]byte("\x1b]11;rgb:0000/0000/0000\x1b\\"))
			}
			if bytes.Contains(chunk, []byte("\x1b[6n")) {
				_, _ = master.Write([]byte("\x1b[1;1R"))
			}
			if err != nil {
				return
			}
		}
	}()
	return r
}

// drawn is everything the child has drawn on the pty so far.
func (r *ptyRun) drawn() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.screen.String()
}

// waitFor waits, at most 30 seconds, for text to be drawn count times.
func (r *ptyRun) waitFor(text string, count int) {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for strings.Count(r.drawn(), text) < count {
		if time.Now().After(deadline) {
			r.t.Fatalf("%q was not drawn %d time(s) within 30s; the pty shows:\n%q", text, count, r.drawn())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// send writes keys to the terminal.
func (r *ptyRun) send(keys string) {
	r.t.Helper()
	if _, err := r.master.Write([]byte(keys)); err != nil {
		r.t.Fatal(err)
	}
}

// wait waits, at most 30 seconds, for the child to end, and returns how;
// on the deadline its whole session is killed and the test fails.
func (r *ptyRun) wait() syscall.WaitStatus {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			r.t.Fatal(err)
		}
	case <-ctx.Done():
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		r.t.Fatalf("the child did not end within 30s; the pty shows:\n%q", r.drawn())
	}
	// With the child gone the slave has no opener left, so the reader gets
	// the end of the pty once it has taken what the child drew last.
	select {
	case <-r.drained:
	case <-time.After(5 * time.Second):
		r.t.Fatalf("the pty was not drained within 5s of the child's end; it shows:\n%q", r.drawn())
	}
	return r.cmd.ProcessState.Sys().(syscall.WaitStatus)
}

// ptyFixture is the matrix's fixture with ptyFakeDevpod on its PATH.
func ptyFixture(t *testing.T) streamsFixture {
	t.Helper()
	fx := newStreamsFixture(t)
	if err := os.WriteFile(filepath.Join(fx.bin, "devpod"), []byte(ptyFakeDevpod), 0o755); err != nil {
		t.Fatal(err)
	}
	return fx
}

// devpodCalls is what the child asked the fake devpod beyond listing.
func devpodCalls(fx streamsFixture) string {
	b, _ := os.ReadFile(filepath.Join(fx.home, "devpod.log"))
	return string(b)
}

// endedBy says how the child ended, for a message.
func endedBy(ws syscall.WaitStatus) string {
	if ws.Signaled() {
		return "death by " + ws.Signal().String()
	}
	return "exit " + strconv.Itoa(ws.ExitStatus())
}

// TestPromptOnAPty: ws delete ops asks on the pty — y deletes, n prints
// Aborted and exits 0, a Ctrl-C ends the process by SIGINT, a SIGTERM by
// SIGTERM, and a SIGHUP by SIGHUP with the terminal's state restored; only a
// yes removes the workspace. The prompt is drawn on the pty alone.
func TestPromptOnAPty(t *testing.T) {
	ops := func(fx streamsFixture) bool {
		_, err := os.Stat(filepath.Join(fx.home, "workspaces", "ops"))
		return err == nil
	}
	for _, c := range []struct {
		name   string
		act    func(r *ptyRun)
		signal syscall.Signal // 0: an exit 0
		kept   bool
		drawn  string // drawn after the prompt, when not empty
	}{
		{"y", func(r *ptyRun) { r.send("y") }, 0, false, ""},
		{"n", func(r *ptyRun) { r.send("n") }, 0, true, "Aborted"},
		{"Ctrl-C", func(r *ptyRun) { r.send("\x03") }, syscall.SIGINT, true, ""},
		{"SIGTERM", func(r *ptyRun) { _ = r.cmd.Process.Signal(syscall.SIGTERM) }, syscall.SIGTERM, true, ""},
		{"SIGHUP", func(r *ptyRun) { _ = r.cmd.Process.Signal(syscall.SIGHUP) }, syscall.SIGHUP, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := ptyFixture(t)
			r := startPtyChild(t, fx, "xterm-256color", "delete", "ops")
			r.waitFor("Delete workspace", 1)
			c.act(r)
			ws := r.wait()
			if c.signal == 0 && (ws.Signaled() || ws.ExitStatus() != 0) || c.signal != 0 && (!ws.Signaled() || ws.Signal() != c.signal) {
				t.Errorf("the child ended with %s; want %s", endedBy(ws), endedBy(syscall.WaitStatus(c.signal)))
			}
			if ops(fx) != c.kept {
				t.Errorf("workspace ops kept = %t; want %t", ops(fx), c.kept)
			}
			if c.drawn != "" && !strings.Contains(r.drawn(), c.drawn) {
				t.Errorf("%q was not drawn; the pty shows:\n%q", c.drawn, r.drawn())
			}
			if r.stdout.Len() != 0 {
				t.Errorf("stdout = %q; want nothing", r.stdout.String())
			}
			if c.signal == syscall.SIGHUP {
				if after := slaveState(t, r.slave); after != r.before {
					t.Errorf("the terminal's state after the hang-up differs from before:\nbefore %+v\nafter  %+v", r.before, after)
				}
			}
		})
	}
}

// TestSelectOnAPty: ws ssh's selector on the pty — Enter takes the
// highlighted workspace, the first; a Ctrl-C and a SIGTERM end the process
// by their signals with no workspace entered.
func TestSelectOnAPty(t *testing.T) {
	for _, c := range []struct {
		name   string
		act    func(r *ptyRun)
		signal syscall.Signal
		devpod string
	}{
		{"Enter", func(r *ptyRun) { r.send("\r") }, 0, "ssh api\n"},
		{"Ctrl-C", func(r *ptyRun) { r.send("\x03") }, syscall.SIGINT, ""},
		{"SIGTERM", func(r *ptyRun) { _ = r.cmd.Process.Signal(syscall.SIGTERM) }, syscall.SIGTERM, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := ptyFixture(t)
			r := startPtyChild(t, fx, "xterm-256color", "ssh")
			r.waitFor("Select workspace", 1)
			c.act(r)
			ws := r.wait()
			if c.signal == 0 && (ws.Signaled() || ws.ExitStatus() != 0) || c.signal != 0 && (!ws.Signaled() || ws.Signal() != c.signal) {
				t.Errorf("the child ended with %s; want %s", endedBy(ws), endedBy(syscall.WaitStatus(c.signal)))
			}
			if got := devpodCalls(fx); got != c.devpod {
				t.Errorf("devpod was called %q; want %q", got, c.devpod)
			}
			if r.stdout.Len() != 0 {
				t.Errorf("stdout = %q; want nothing", r.stdout.String())
			}
		})
	}
}

// TestScreenReaderOnAPty: under TERM=dumb, ws ssh answered with a number out
// of range and then the end of input ends by SIGINT, with no panic and no
// workspace entered; ws profile-create given the end of input at its first
// field ends by SIGINT with no second field drawn and no profile created.
func TestScreenReaderOnAPty(t *testing.T) {
	t.Run("ws ssh", func(t *testing.T) {
		fx := ptyFixture(t)
		r := startPtyChild(t, fx, "dumb", "ssh")
		r.waitFor("Enter a number", 1)
		r.send("9\n")
		r.waitFor("Enter a number", 2)
		r.send("\x04")
		ws := r.wait()
		if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
			t.Errorf("the child ended with %s; want death by interrupt", endedBy(ws))
		}
		if strings.Contains(r.drawn(), "panic") || devpodCalls(fx) != "" || r.stdout.Len() != 0 {
			t.Errorf("a panic, a devpod call or stdout: devpod %q, stdout %q, pty:\n%q", devpodCalls(fx), r.stdout.String(), r.drawn())
		}
	})
	t.Run("ws profile-create", func(t *testing.T) {
		fx := ptyFixture(t)
		r := startPtyChild(t, fx, "dumb", "profile-create", "foo")
		r.waitFor("Base image", 1)
		r.send("\x04")
		ws := r.wait()
		if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
			t.Errorf("the child ended with %s; want death by interrupt", endedBy(ws))
		}
		if strings.Contains(r.drawn(), "System packages") || r.stdout.Len() != 0 {
			t.Errorf("a second field or stdout: stdout %q, pty:\n%q", r.stdout.String(), r.drawn())
		}
		if _, err := os.Stat(filepath.Join(fx.home, ".config", "workspaces", "profiles", "foo")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the profile was created: %v", err)
		}
	})
}

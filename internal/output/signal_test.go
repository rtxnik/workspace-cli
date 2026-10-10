package output

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeDeath is a death whose every effect is recorded as a line in calls.
func fakeDeath(ignored func(os.Signal) bool, calls *[]string) death {
	return death{
		ignored: ignored,
		reset:   func(sigs ...os.Signal) { *calls = append(*calls, fmt.Sprintf("reset %v", sigs)) },
		kill: func(pid int, sig syscall.Signal) error {
			*calls = append(*calls, fmt.Sprintf("kill self=%t %v", pid == syscall.Getpid(), sig))
			return nil
		},
		wait: func(d time.Duration) { *calls = append(*calls, "wait "+d.String()) },
		exit: func(code int) { *calls = append(*calls, fmt.Sprintf("exit %d", code)) },
	}
}

// TestExitBySignalDiesOfTheSignal: for a signal the process was not started
// with ignored, the death restores the default disposition, sends the signal
// to the process, waits a second and only then exits 128 + signal.
func TestExitBySignalDiesOfTheSignal(t *testing.T) {
	var calls []string
	fakeDeath(func(os.Signal) bool { return false }, &calls).die(syscall.SIGTERM)
	want := "reset [terminated] | kill self=true terminated | wait 1s | exit 143"
	if got := strings.Join(calls, " | "); got != want {
		t.Errorf("the death did %q; want %q", got, want)
	}
}

// TestExitBySignalExitsAtOnceForAnIgnoredSignal: for a signal the start
// snapshot says was ignored, the death exits 128 + signal at once, sending
// nothing and waiting for nothing — the kill would be discarded.
func TestExitBySignalExitsAtOnceForAnIgnoredSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGHUP} {
		var calls []string
		fakeDeath(func(s os.Signal) bool { return s == sig }, &calls).die(sig)
		want := fmt.Sprintf("exit %d", 128+int(sig))
		if got := strings.Join(calls, " | "); got != want {
			t.Errorf("%v ignored at start: the death did %q; want %q", sig, got, want)
		}
	}
}

const signalChildEnv = "WS_TEST_SIGNAL_CHILD"

// TestSignalChild is the far side of TestStartSnapshot and
// TestExitBySignalInAProcess. Each mode first does what a prompt does — asks
// for SIGINT and SIGHUP and stops asking — and then: =snapshot prints what
// the snapshot and signal.Ignored answer; =exit-<sig> prints READY and calls
// ExitBySignal with that signal.
func TestSignalChild(t *testing.T) {
	mode := os.Getenv(signalChildEnv)
	if mode == "" {
		t.Skip("child half of the start snapshot's tests; runs only in the subprocess")
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGHUP)
	signal.Stop(c)
	switch mode {
	case "snapshot":
		fmt.Printf("snapshot %t %t, now %t %t\n",
			ignoredAtStart(os.Interrupt), ignoredAtStart(syscall.SIGHUP),
			signal.Ignored(os.Interrupt), signal.Ignored(syscall.SIGHUP))
		os.Exit(0)
	case "exit-sigint":
		fmt.Println("READY")
		ExitBySignal(os.Interrupt)
	case "exit-sighup":
		fmt.Println("READY")
		ExitBySignal(syscall.SIGHUP)
	case "exit-sigterm":
		fmt.Println("READY")
		ExitBySignal(syscall.SIGTERM)
	}
	os.Exit(3)
}

// signalChild is TestSignalChild in the given mode, started with SIGINT and
// SIGHUP ignored when ignored is set — a shell's trap with an empty action,
// as `nohup` and a script's `&` leave them.
func signalChild(mode string, ignored bool) *exec.Cmd {
	var cmd *exec.Cmd
	if ignored {
		cmd = exec.Command("sh", "-c", `trap '' INT HUP; exec "$0" -test.run='^TestSignalChild$'`, os.Args[0])
	} else {
		cmd = exec.Command(os.Args[0], "-test.run=^TestSignalChild$")
	}
	cmd.Env = append(os.Environ(), signalChildEnv+"="+mode)
	return cmd
}

// TestStartSnapshot: the snapshot is read when the package initialises, so a
// process started with SIGINT and SIGHUP ignored still reads them ignored
// after a prompt has asked for them — when signal.Ignored no longer does.
func TestStartSnapshot(t *testing.T) {
	for _, tc := range []struct {
		ignored bool
		want    string
	}{
		{true, "snapshot true true, now false false"},
		{false, "snapshot false false, now false false"},
	} {
		out, err := signalChild("snapshot", tc.ignored).Output()
		if err != nil {
			t.Fatalf("ignored=%t: the child failed: %v", tc.ignored, err)
		}
		if got := strings.TrimSpace(string(out)); got != tc.want {
			t.Errorf("ignored=%t: the child printed %q; want %q", tc.ignored, got, tc.want)
		}
	}
}

// TestExitBySignalInAProcess: in a process started with SIGINT and SIGHUP
// ignored, ExitBySignal exits 128 + signal at once — well under the second
// it waits for a signal it sent — even after a prompt asked for them; a
// SIGTERM the process was not started with ignored kills it.
func TestExitBySignalInAProcess(t *testing.T) {
	run := func(mode string, ignored bool) (syscall.WaitStatus, time.Duration) {
		t.Helper()
		cmd := signalChild(mode, ignored)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("stdout pipe: %v", err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "READY\n" {
			_ = cmd.Process.Kill()
			t.Fatalf("%s: the child wrote %q, %v; want READY", mode, line, err)
		}
		ready := time.Now()
		waited := make(chan struct{})
		go func() { _ = cmd.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%s: the child did not end", mode)
		}
		return cmd.ProcessState.Sys().(syscall.WaitStatus), time.Since(ready)
	}
	for _, tc := range []struct {
		mode string
		code int
	}{{"exit-sigint", 130}, {"exit-sighup", 129}} {
		status, took := run(tc.mode, true)
		if status.Signaled() || status.ExitStatus() != tc.code {
			t.Errorf("%s, ignored at start: the child ended with %v; want exit %d", tc.mode, status, tc.code)
		}
		if took >= 900*time.Millisecond {
			t.Errorf("%s, ignored at start: the child took %v to end; want at once, not after the second's wait", tc.mode, took)
		}
	}
	if status, _ := run("exit-sigterm", false); !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("exit-sigterm: the child ended with %v; want death by SIGTERM", status)
	}
}

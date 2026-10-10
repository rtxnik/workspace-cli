package output

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// startIgnored records, for each signal the step runner drains for — the
// signals a prompt records too — whether the process was started with it
// ignored: `nohup` ignores SIGHUP, `ws … &` from a script SIGINT. It is read
// once, when the package initialises, before anything calls signal.Notify.
// signal.Ignored cannot be asked later: a Notify clears the runtime's ignored
// bit and stopping it never sets the bit again (runtime/sigqueue.go), and
// after one prompt Bubble Tea's own handler has done exactly that. The step
// runner's signal source, ExitBySignal and the prompt's signal recorder read
// this snapshot, never signal.Ignored.
var startIgnored = ignoredNow(drainedSignals)

// ignoredNow asks signal.Ignored about each of sigs.
func ignoredNow(sigs []os.Signal) map[os.Signal]bool {
	ignored := make(map[os.Signal]bool, len(sigs))
	for _, sig := range sigs {
		ignored[sig] = signal.Ignored(sig)
	}
	return ignored
}

// ignoredAtStart reports whether the process was started with sig ignored.
func ignoredAtStart(sig os.Signal) bool { return startIgnored[sig] }

// ExitBySignal ends the process by sig: it restores sig's default
// disposition, sends sig to the process, and, should the process survive a
// second, exits with the status a shell gives a death by sig, 128 + sig. A
// signal the process was started with ignored cannot end it — signal.Reset
// puts the inherited SIG_IGN back and the kill is discarded — so for such a
// signal it exits 128 + sig at once, sending nothing and waiting for nothing.
//
// It is the step runner's death, and the root's for a prompt the operator
// cancelled.
func ExitBySignal(sig os.Signal) { processDeath.die(sig) }

// death is ExitBySignal with everything it touches as a seam, so that its
// two paths are asserted in process without the test process dying.
type death struct {
	ignored func(sig os.Signal) bool
	reset   func(sigs ...os.Signal)
	kill    func(pid int, sig syscall.Signal) error
	wait    func(d time.Duration)
	exit    func(code int)
}

var processDeath = death{
	ignored: ignoredAtStart,
	reset:   signal.Reset,
	kill:    syscall.Kill,
	wait:    time.Sleep,
	exit:    os.Exit,
}

func (d death) die(sig os.Signal) {
	num, _ := sig.(syscall.Signal)
	if d.ignored(sig) {
		d.exit(128 + int(num))
		return
	}
	d.reset(sig)
	_ = d.kill(syscall.Getpid(), num)
	// The signal ends the process. Should it not within a second, exit with
	// the status a shell gives a death by that signal.
	d.wait(time.Second)
	d.exit(128 + int(num))
}

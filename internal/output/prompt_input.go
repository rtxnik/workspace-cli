package output

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/huh"
)

// The screen-reader mode.
//
// huh calls it accessible: no Bubble Tea, a prompt line, an answer read as a
// line of text. The core enters it under TERM=dumb, and only when the
// predicate holds — a person at a terminal that declares itself dumb: a
// screen reader, an editor's shell buffer.

// askAccessible runs f in huh's screen-reader mode, drawn on the prompt's
// stream instead of huh's default stdout, and reading stdin through a
// lineRecorder. huh never says how a scan ended: it answers a failed scan
// with the field's default, or with the last line its validator rejected,
// and returns nil — and a Select or a MultiSelect then indexes its options
// with a rejected number unchecked, and panics. So the recorder decides, the
// run is recovered, and the first rule that matches decides:
//
//  1. a read error, or a line as long as huh's scanner can hold: the prompt
//     failed;
//  2. the end of input, anywhere in the form: cancelled, by SIGINT — text
//     typed and then ended with Ctrl-D included, since the operator never
//     pressed Enter;
//  3. neither, and huh panicked or failed: the prompt failed;
//  4. neither: the answer, every field answered by a line ended by Enter.
//
// No signal is recorded in this mode: the line read blocks and nothing
// interrupts it, so a Ctrl-C is a real SIGINT whose default action ends the
// process, and the terminal, cooked, needs nothing restored.
func (p *prompter) askAccessible(f *huh.Form) error {
	in := &lineRecorder{r: p.input}
	f.WithAccessible(true).WithOutput(p.stream).WithInput(in)
	panicked, err := recovered(f.Run)
	switch {
	case in.failed != nil:
		return fmt.Errorf("prompt failed: %w", in.failed)
	case in.ended:
		return &Cancelled{Signal: os.Interrupt}
	case panicked != nil:
		return fmt.Errorf("prompt failed: %v", panicked)
	case err != nil:
		return fmt.Errorf("prompt failed: %w", err)
	}
	return nil
}

// errLineTooLong is a line as long as huh's scanner can hold, without its
// newline: the scanner gives up on it, and huh answers with a default.
var errLineTooLong = fmt.Errorf("a line of %d bytes or more", bufio.MaxScanTokenSize)

// lineRecorder is stdin as the screen-reader mode reads it. It records what
// huh cannot report — the end of input, a read error, and a run of bytes
// without a newline as long as huh's scanner can hold, counted across reads
// — and it changes two things about what huh is given:
//
//   - At most one line per Read; the rest waits for the next one. huh builds
//     a new bufio.Scanner for every prompt, and a scanner reads ahead, so the
//     bytes past the first line would be lost with it. A terminal in
//     canonical mode already gives one line per read; this makes every
//     reader behave so.
//   - It latches. Once a Read has returned the end of input or an error, or
//     an overlong line was seen, every later Read returns the end of input
//     at once, without touching stdin. A terminal's end of input is not
//     sticky — one read returns it, the next blocks again — and huh asks
//     the next field after a failed scan: unlatched, a Ctrl-D at the
//     wizard's first field would leave it waiting at its second.
type lineRecorder struct {
	r       io.Reader
	pending []byte // read from r and not handed to huh yet
	atEOF   bool   // r returned the end of input along with pending's bytes
	run     int    // bytes handed out since the last newline
	ended   bool   // the end of input reached huh
	failed  error  // a read error, or errLineTooLong
}

func (l *lineRecorder) Read(p []byte) (int, error) {
	if l.ended || l.failed != nil {
		return 0, io.EOF
	}
	if len(l.pending) == 0 {
		if l.atEOF {
			l.ended = true
			return 0, io.EOF
		}
		buf := make([]byte, max(len(p), 1))
		n, err := l.r.Read(buf)
		l.pending = buf[:n]
		switch {
		case errors.Is(err, io.EOF) && n == 0:
			l.ended = true
			return 0, io.EOF
		case errors.Is(err, io.EOF):
			l.atEOF = true
		case err != nil:
			l.failed = err
			return 0, err
		case n == 0:
			return 0, nil
		}
	}
	line := l.pending
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i+1]
	}
	n := copy(p, line)
	l.pending = l.pending[n:]
	// What was handed out ends with its line's newline, or holds none. A
	// scanner's buffer stops at its limit, so a line too long for it shows
	// here as a run without a newline that reaches the limit.
	if n > 0 && p[n-1] == '\n' {
		l.run = 0
	} else {
		l.run += n
		if l.run >= bufio.MaxScanTokenSize {
			l.failed = errLineTooLong
		}
	}
	return n, nil
}

package output

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// Log is a task's output. File is what a child is given as its stdout and
// stderr.
//
// It is a temporary file that the runner creates, opens a second time for
// reading and unlinks at once. The child writes to it directly — no pipe,
// no copying goroutine inside ws — and the runner reads what is new at each
// tick for the live line, keeping the last tailLines lines for the tail.
// Because it is a file and not a pipe, a child that outlives the runner —
// the runner has died of SIGINT while the child runs its own cleanup —
// keeps writing without a SIGPIPE; because it is unlinked, nothing is left
// behind in $TMPDIR.
type Log struct {
	w *os.File // the file a child writes to
	r *os.File // the same file, opened a second time for reading

	mu      sync.Mutex
	off     int64    // how much of the file has been read
	partial []byte   // the line being written, after its last carriage return
	cr      bool     // the last byte read was a carriage return
	full    bool     // the line being written reached lineCap; the rest is dropped
	cut     bool     // a read's window began inside the line being written: it is shown after "…"
	lines   []string // the last tailLines complete lines that are not blank

	readHook func() // a test's: called after each read of the file
}

const (
	// tailLines is how many of a task's last lines Tail keeps.
	tailLines = 20
	// lineCap is where each kept line is cut, in bytes.
	lineCap = 1024
	// readWindow is the most of what is new that one read parses, counted
	// back from the file's end: further back, what a child wrote is
	// skipped, as the tail and the live line are the log's last lines.
	readWindow = 256 << 10
)

// newLog creates a task's log.
func newLog() (*Log, error) {
	w, err := os.CreateTemp("", "ws-task-*.log")
	if err != nil {
		return nil, fmt.Errorf("create the task log: %w", err)
	}
	r, oerr := os.Open(w.Name())
	rerr := os.Remove(w.Name())
	if oerr != nil || rerr != nil {
		_ = w.Close()
		if r != nil {
			_ = r.Close()
		}
		return nil, fmt.Errorf("open the task log: %w", errors.Join(oerr, rerr))
	}
	return &Log{w: w, r: r}, nil
}

// nullLog is the log of a task whose log file cannot be made: what is
// written to it is discarded, and its tail and live line stay empty.
func nullLog() (*Log, error) {
	w, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open the null log: %w", err)
	}
	r, err := os.Open(os.DevNull)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open the null log: %w", err)
	}
	return &Log{w: w, r: r}, nil
}

// Write appends p to the log, as a child writing to File does.
func (l *Log) Write(p []byte) (int, error) { return l.w.Write(p) }

// File is the log's file, for exec.Cmd's Stdout and Stderr.
func (l *Log) File() *os.File { return l.w }

// Tail is the last lines of the log that are not blank, raw — not
// sanitised — at most tailLines of them, each cut at lineCap bytes. A line
// that a carriage return redrew is what followed its last one, which is
// what a terminal would have shown.
func (l *Log) Tail() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.readNew()
	out := append([]string(nil), l.lines...)
	if !blank(string(l.partial)) {
		out = append(out, l.shown(string(l.partial)))
	}
	if len(out) > tailLines {
		out = out[len(out)-tailLines:]
	}
	return out
}

// liveLine is what the frame's second line shows: the last line of the log
// that is not blank, the one still being written included, raw — frameLines
// cleans it. It is "" while the log holds no such line.
func (l *Log) liveLine() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.readNew()
	line := l.shown(string(l.partial))
	if blank(line) {
		if len(l.lines) == 0 {
			return ""
		}
		line = l.lines[len(l.lines)-1]
	}
	return line
}

// readNew reads what was written since the last read, up to what the file
// held when it began, and of that no more than the last readWindow bytes
// and the one before them: a child that writes faster than the log is parsed
// must not keep one read going, nor make each read longer than the one
// before. Called with mu held.
func (l *Log) readNew() {
	fi, err := l.r.Stat()
	if err != nil {
		return
	}
	end := fi.Size()
	if end-l.off > readWindow+1 {
		// Bytes are skipped. The read starts one byte before the window, so
		// a line that starts there is kept whole; the line it starts inside
		// is cut — shown after "…" — unless the byte before it ends a line.
		// What was kept before goes, so the tail stays contiguous.
		start := end - readWindow - 1
		var before [1]byte
		if _, err := l.r.ReadAt(before[:], start-1); err != nil {
			return
		}
		if _, err := l.r.Seek(start, io.SeekStart); err != nil {
			return
		}
		l.off = start
		l.lines, l.partial, l.cr, l.full = l.lines[:0], l.partial[:0], false, false
		l.cut = before[0] != '\n' && before[0] != '\r'
	}
	buf := make([]byte, 32*1024)
	for l.off < end {
		n, err := l.r.Read(buf[:min(int64(len(buf)), end-l.off)])
		l.off += int64(n)
		l.consume(buf[:n])
		if l.readHook != nil {
			l.readHook()
		}
		if err != nil || n == 0 {
			return
		}
	}
}

// consume splits p into lines. A carriage return followed by anything but a
// newline starts the line again, as a progress bar redrawing itself does; a
// line is kept up to lineCap bytes and the rest of it is dropped.
func (l *Log) consume(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexAny(p, "\r\n")
		if i < 0 {
			l.add(p)
			return
		}
		l.add(p[:i])
		switch p[i] {
		case '\n':
			l.cr = false
			l.endLine()
		case '\r':
			l.cr = true
		}
		p = p[i+1:]
	}
}

// add appends text to the line being written, starting the line again if a
// carriage return came before it.
func (l *Log) add(p []byte) {
	if len(p) == 0 {
		return
	}
	if l.cr {
		l.partial, l.full, l.cr, l.cut = l.partial[:0], false, false, false
	}
	if l.full {
		return
	}
	if room := lineCap - len(l.partial); len(p) > room {
		p, l.full = p[:room], true
	}
	l.partial = append(l.partial, p...)
	if l.full {
		l.partial = trimIncompleteRune(l.partial)
	}
}

// trimIncompleteRune drops a rune the cut at lineCap left incomplete, so a
// kept line is valid UTF-8 whenever the child's output was.
func trimIncompleteRune(b []byte) []byte {
	i := len(b) - 1
	for i >= 0 && !utf8.RuneStart(b[i]) {
		i--
	}
	if i >= 0 && !utf8.FullRune(b[i:]) {
		return b[:i]
	}
	return b
}

// shown is a line as the log keeps it: one a read's window cut at its start
// begins with "…", still within lineCap. The window may start inside a rune;
// what is left of that rune goes with the cut.
func (l *Log) shown(line string) string {
	if !l.cut {
		return line
	}
	for i := 0; i < utf8.UTFMax-1 && line != "" && !utf8.RuneStart(line[0]); i++ {
		line = line[1:]
	}
	if blank(line) {
		return line
	}
	line = "…" + line
	if len(line) > lineCap {
		line = string(trimIncompleteRune([]byte(line[:lineCap])))
	}
	return line
}

// endLine keeps the finished line when it is not blank.
func (l *Log) endLine() {
	line := l.shown(string(l.partial))
	l.partial, l.full, l.cut = l.partial[:0], false, false
	if blank(line) {
		return
	}
	if len(l.lines) == tailLines {
		copy(l.lines, l.lines[1:])
		l.lines = l.lines[:tailLines-1]
	}
	l.lines = append(l.lines, line)
}

// blank reports whether a line shows nothing once it is cleaned.
func blank(line string) bool { return strings.TrimSpace(SanitiseInline(line)) == "" }

// close closes both of the log's descriptors. A child that outlives the task
// keeps its own.
func (l *Log) close() {
	_ = l.w.Close()
	_ = l.r.Close()
}

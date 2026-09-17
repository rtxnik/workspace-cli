package output

import (
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
)

// §4.1 Stream — one renderer per file descriptor.
//
// A Stream is an output destination that owns its own colour profile and
// width budget. Colour capability is probed on the fd being written to, never
// on a different one: the measured defect this closes is raw SGR written into
// a redirected err.log because the palette had been chosen from stdout.
type Stream struct {
	w        io.Writer
	width    int
	tty      bool
	level    ColourLevel
	mode     GlyphMode
	renderer *lipgloss.Renderer
}

// WidthUnbounded is the budget for a stream that is not a terminal and has no
// COLUMNS. Content is then rendered complete: no truncation, no dropped
// columns. It is larger than any natural content width, so the allocator's
// "does it already fit?" branch takes over and a piped table renders at its
// natural size (§4.2).
const WidthUnbounded = math.MaxInt32

// MinWidth is the product floor: the narrowest terminal the CLI undertakes to
// serve. It is NOT the natural floor of the widest block — `Table` needs 46
// columns for `ws proxy profile list` — and must not be described as one.
// Between 29 and a table's own natural floor the allocator enters §4.3 step 5
// and the table degrades rather than overflowing. Below 29 the allocator
// clamps to 29 and lets the terminal wrap; that is a deliberate, stated limit.
const MinWidth = 29

// NewStreamAt builds a Stream over an arbitrary writer with every property
// injected. Required, not a convenience: a lipgloss renderer over a
// non-*os.File writer resolves to Ascii and cannot be raised through the
// library's own probe, so without this the §6 acceptance sweep could not run
// at any colour level above none (§4.1).
func NewStreamAt(w io.Writer, width int, tty bool, level ColourLevel, ascii bool) *Stream {
	mode := GlyphUTF8
	if ascii {
		mode = GlyphASCII
	}
	renderer := lipgloss.NewRenderer(w)
	// The layer declares each role's value per colour level itself (§4.6), so
	// lipgloss is told to emit exactly what it is handed rather than applying
	// termenv's automatic downsample — which was measured to invert the
	// palette at 16 colours.
	if level == ColourNone {
		renderer.SetColorProfile(termenv.Ascii)
	} else {
		renderer.SetColorProfile(termenv.TrueColor)
	}
	return &Stream{w: w, width: width, tty: tty, level: level, mode: mode, renderer: renderer}
}

// NewStream builds a Stream over a real fd, probing that fd — and only that
// fd — for width, TTY status and colour.
func NewStream(f *os.File) *Stream { return newStream(f, os.Getenv, terminalWidth) }

// newStream is NewStream with the environment and the terminal-size probe
// injected. The seam is what lets §6.6 assert that the fd handed to the probe
// is the stream's OWN fd without a pty: probing the wrong one is the measured
// defect at cmd/workspace.go:108 and cmd/profile.go:55, and it is invisible to
// a test that can only look at the resolved number.
func newStream(f *os.File, getenv func(string) string, probe widthProbe) *Stream {
	fd := f.Fd()
	tty := term.IsTerminal(fd)
	// Width, TTY status and colour are all resolved from f — the descriptor
	// this Stream writes to — and never from another one (§4.1).
	return NewStreamAt(
		f,
		ResolveWidth(fd, getenv, probe),
		tty,
		probeColour(f, tty, getenv),
		glyphModeFromEnv(getenv) == GlyphASCII,
	)
}

var (
	outOnce, errOnce     sync.Once
	outStream, errStream *Stream
)

// stdWriter is the writer of the two PROCESS streams, and it resolves
// os.Stdout / os.Stderr at WRITE time rather than at resolution time.
//
// The probe is memoised; the destination is not. Out() and Err() resolve
// width, TTY status, colour level and glyph mode once per process — which is
// all §4.1 asks for — but a *Stream that also captured the *os.File it was
// built from would make a later reassignment of os.Stderr invisible, and
// reassigning os.Stderr around a call is how several packages in this
// repository capture operator-facing warnings in their own tests
// (internal/docker/verify_fixroutes_test.go's captureStderr is one).
// fmt.Fprintln(os.Stderr, ...) re-reads the variable on every call, so the
// helpers behave that way today and must keep behaving that way.
//
// Nothing outside this package reaches Out() or Err() yet, so those capturing
// suites do NOT detect a lost late binding today: measured, dropping this
// writer leaves every other package in the repository green and reddens only
// probeStreamIdentity's landing assertions. That is why the probe asserts
// where a write LANDS rather than what the writer is, and why it cannot be
// deferred until the message helpers route through Err().
type stdWriter struct{ err bool }

func (s stdWriter) Write(p []byte) (int, error) {
	if s.err {
		return os.Stderr.Write(p)
	}
	return os.Stdout.Write(p)
}

// newStdStream probes f once and then builds the Stream over the late-bound
// writer for that descriptor — BOTH the Write path and the lipgloss renderer,
// so no field of the returned struct retains f.
//
// Swapping only s.w would leave the renderer's termenv output wrapping the
// file the stream was probed from, which makes "the destination is not
// memoised" true of Write and false of the struct. Measured when this was
// repointed: the SGR is byte-identical at every role and every colour level
// (56 level x glyph-mode x role renders, 0 differences, and 0 of 7 on the real
// NewStream path), because NewStreamAt sets the colour profile explicitly
// instead of letting the renderer probe its own writer.
func newStdStream(f *os.File, err bool) *Stream {
	probed := NewStream(f)
	return NewStreamAt(stdWriter{err: err}, probed.width, probed.tty, probed.level, probed.mode == GlyphASCII)
}

// Out is stdout: the answer. Resolved once per process and memoised (§4.1);
// the destination itself is late-bound, see stdWriter.
//
// The memoisation is a sync.Once rather than a nil check because
// cmd/workspace.go:242 calls output.Warn from inside the closure handed to
// output.RunWithSpinner, and huh/spinner runs that closure on its own
// goroutine while the spinner redraws. No file that launches a goroutine
// imports this package today, so -race is currently quiet — the race is
// latent, which is exactly why a nil check would survive review.
func Out() *Stream {
	outOnce.Do(func() { outStream = newStdStream(os.Stdout, false) })
	return outStream
}

// Err is stderr: everything about producing the answer (§4.7).
func Err() *Stream {
	errOnce.Do(func() { errStream = newStdStream(os.Stderr, true) })
	return errStream
}

// Width reports the resolved width of this stream, unclamped. Renders use
// budget(), which applies the MinWidth floor.
func (s *Stream) Width() int { return s.width }

// IsTTY reports whether this stream's fd is a terminal. Spinners and step
// progress are gated on the TTY status of the fd they actually write to
// (§4.7), which is why this is per-stream and not a process-wide flag.
func (s *Stream) IsTTY() bool { return s.tty }

// Mode reports this stream's glyph mode (§4.5).
func (s *Stream) Mode() GlyphMode { return s.mode }

// Write makes a Stream an io.Writer, so a component that owns the stream for
// its lifetime — the spinner of §4.7 — can be pointed at it directly.
func (s *Stream) Write(p []byte) (int, error) { return s.w.Write(p) }

// budget is the width every Render lays out against: the resolved width with
// the MinWidth floor applied.
func (s *Stream) budget() int { return clampBudget(s.width) }

// clampBudget applies §4.2's floor. It is an invariant of the layer rather
// than a side effect of one constructor: any path that reaches the allocator
// with a sub-MinWidth budget gets the same clamp.
func clampBudget(width int) int {
	if width < MinWidth {
		return MinWidth
	}
	return width
}

// Style returns the lipgloss style for a role on this stream. RoleDefault
// emits no SGR at all, and NO_COLOR or a non-TTY stream have already resolved
// every role to RoleDefault by way of ColourNone (§4.6).
func (s *Stream) Style(role Role) lipgloss.Style {
	base := s.renderer.NewStyle()
	if colour, ok := colourFor(role, s.level); ok {
		return base.Foreground(colour)
	}
	return base
}

// ---------------------------------------------------------------- §4.2 width

// widthProbe is the terminal-size probe, injectable so width resolution is
// testable without a pty.
type widthProbe func(fd uintptr) (int, error)

func terminalWidth(fd uintptr) (int, error) {
	w, _, err := term.GetSize(fd)
	return w, err
}

// ResolveWidth implements §4.2's resolution order for one fd:
//
//  1. COLUMNS, if it parses as a positive integer. A value below MinWidth is
//     CLAMPED to MinWidth, never rejected: rejecting it would fall through to
//     step 3, so COLUMNS=28 in a pipe would render an unbounded table while
//     COLUMNS=29 rendered at 29 — a discontinuity in the wrong direction.
//     Measured on the rejecting variant: COLUMNS=1, 10 and 28 all resolved to
//     WidthUnbounded while COLUMNS=29 resolved to 29.
//  2. term.GetSize(fd) on this stream's own fd, likewise clamped.
//  3. WidthUnbounded.
func ResolveWidth(fd uintptr, getenv func(string) string, probe widthProbe) int {
	if v := strings.TrimSpace(getenv("COLUMNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return clampBudget(n)
		}
	}
	if probe != nil {
		if w, err := probe(fd); err == nil && w > 0 {
			return clampBudget(w)
		}
	}
	return WidthUnbounded
}

// probeColour resolves the colour level of one fd. NO_COLOR and a non-TTY
// stream both resolve every role to RoleDefault (§4.6).
func probeColour(f *os.File, tty bool, getenv func(string) string) ColourLevel {
	if getenv("NO_COLOR") != "" || !tty {
		return ColourNone
	}
	switch termenv.NewOutput(f).Profile {
	case termenv.TrueColor:
		return ColourTrue
	case termenv.ANSI256:
		return Colour256
	case termenv.ANSI:
		return Colour16
	}
	return ColourNone
}

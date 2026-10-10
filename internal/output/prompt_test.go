package output

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestPromptReadyRule: a prompt may ask only when stdin and stderr are both
// terminals; otherwise the error is ErrNoTTY, and its text names the stream,
// or the two streams, that are not.
func TestPromptReadyRule(t *testing.T) {
	for _, tc := range []struct {
		stdin, stderr bool
		want          string
	}{
		{true, true, ""},
		{false, true, "stdin is not a terminal"},
		{true, false, "stderr is not a terminal"},
		{false, false, "stdin and stderr are not terminals"},
	} {
		err := promptReady(tc.stdin, tc.stderr)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("stdin=%t stderr=%t: %v; want nil", tc.stdin, tc.stderr, err)
		case tc.want != "" && (!errors.Is(err, ErrNoTTY) || err.Error() != tc.want):
			t.Errorf("stdin=%t stderr=%t: %v; want ErrNoTTY reading %q", tc.stdin, tc.stderr, err, tc.want)
		}
	}
}

// TestPromptReadyProbesStdinAndStderr: PromptReady reads its answer from the
// process's own stdin and stderr — each of the four combinations of a
// terminal (a pty's master) and the null device, with stdout left alone.
func TestPromptReadyProbesStdinAndStderr(t *testing.T) {
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("no pty to probe against: %v", err)
	}
	defer func() { _ = pty.Close() }()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = null.Close() }()

	files := map[bool]*os.File{true: pty, false: null}
	for _, tc := range []struct {
		stdin, stderr bool
		want          string
	}{
		{true, true, ""},
		{false, true, "stdin is not a terminal"},
		{true, false, "stderr is not a terminal"},
		{false, false, "stdin and stderr are not terminals"},
	} {
		var got error
		savedIn := os.Stdin
		os.Stdin = files[tc.stdin]
		withStdStreams(os.Stdout, files[tc.stderr], func() { got = PromptReady() })
		os.Stdin = savedIn
		gotText := ""
		if got != nil {
			gotText = got.Error()
		}
		if gotText != tc.want || (got != nil) != errors.Is(got, ErrNoTTY) {
			t.Errorf("stdin terminal=%t, stderr terminal=%t: PromptReady() = %v; want %q", tc.stdin, tc.stderr, got, tc.want)
		}
	}
}

// TestCancelledCarriesItsSignal: a Cancelled is found through a wrap, and
// carries the signal the process is to die of.
func TestCancelledCarriesItsSignal(t *testing.T) {
	err := error(&Cancelled{Signal: syscall.SIGTERM})
	var c *Cancelled
	if !errors.As(errors.Join(errors.New("context"), err), &c) || c.Signal != syscall.SIGTERM {
		t.Fatalf("errors.As found %v; want the Cancelled with SIGTERM", c)
	}
	if got, want := err.Error(), "prompt cancelled: terminated"; got != want {
		t.Errorf("Error() = %q; want %q", got, want)
	}
}

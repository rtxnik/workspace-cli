package output

import (
	"bytes"
	"errors"
	"testing"
)

// failingWriter always fails, to prove WriteJSON propagates sink errors
// instead of swallowing them.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("sink failed") }

// TestWriteJSONByteShape pins the exact display contract the five migrated
// call sites rely on: 2-space indent, sorted map keys, trailing newline.
func TestWriteJSONByteShape(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, map[string]any{"name": "demo", "count": 2}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	want := "{\n  \"count\": 2,\n  \"name\": \"demo\"\n}\n"
	if got := buf.String(); got != want {
		t.Errorf("byte shape mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestWriteJSONPropagatesWriteError(t *testing.T) {
	if err := WriteJSON(failingWriter{}, map[string]string{"k": "v"}); err == nil {
		t.Fatal("expected write error, got nil")
	}
}

func TestWriteJSONPropagatesEncodeError(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, func() {}); err == nil {
		t.Fatal("expected encode error for unmarshalable value, got nil")
	}
}

// TestWriteJSONEscapesHTML pins the default HTML escaping the migrated
// call sites inherited from json.MarshalIndent — a SetEscapeHTML(false)
// in WriteJSON would silently change display output.
func TestWriteJSONEscapesHTML(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, map[string]string{"u": "a<b&c"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	want := "{\n  \"u\": \"a\\u003cb\\u0026c\"\n}\n"
	if got := buf.String(); got != want {
		t.Errorf("HTML escaping mismatch:\n got %q\nwant %q", got, want)
	}
}

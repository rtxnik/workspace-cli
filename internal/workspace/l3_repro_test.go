package workspace

import (
	"encoding/json"
	"testing"
)

// mustParse strips JSONC and unmarshals into a generic value; a failure means
// the scanner did not produce valid JSON.
func mustParseJSONC(t *testing.T, in string) map[string]any {
	t.Helper()
	cleaned, err := stripJSONCComments(in)
	if err != nil {
		t.Fatalf("stripJSONCComments(%q) error: %v", in, err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(cleaned), &v); err != nil {
		t.Fatalf("cleaned %q is not valid JSON: %v", cleaned, err)
	}
	return v
}

// TestL3_04a: block comments are legal JSONC and must be stripped.
func TestL3_04a(t *testing.T) {
	v := mustParseJSONC(t, `{/* leading */ "a": 1 /* trailing */ }`)
	if v["a"] != float64(1) {
		t.Errorf("a = %v, want 1", v["a"])
	}
}

// TestL3_04b: trailing commas are legal JSONC and must be elided.
func TestL3_04b(t *testing.T) {
	v := mustParseJSONC(t, "{\n\t\"runArgs\": [\"--x\", \"--y\",],\n\t\"a\": 1,\n}")
	arr, _ := v["runArgs"].([]any)
	if len(arr) != 2 {
		t.Errorf("runArgs = %v, want 2 elements", arr)
	}
}

// TestL3_05_EscapedBackslashClosesString: a value ending in an escaped
// backslash pair (\\) must let the very next quote close the string, so a
// following // comment is stripped rather than swallowed as string content.
func TestL3_05_EscapedBackslashClosesString(t *testing.T) {
	in := `{"path": "C:\\", "b": 2} // tail comment`
	v := mustParseJSONC(t, in)
	if v["path"] != `C:\` {
		t.Errorf("path = %q, want %q", v["path"], `C:\`)
	}
	if v["b"] != float64(2) {
		t.Errorf("b = %v, want 2", v["b"])
	}
}

// TestL3_05_EscapedQuoteInString: an escaped quote must NOT end the string.
func TestL3_05_EscapedQuoteInString(t *testing.T) {
	v := mustParseJSONC(t, `{"s": "a \" // not-a-comment b", "n": 3}`)
	if v["s"] != `a " // not-a-comment b` {
		t.Errorf("s = %q", v["s"])
	}
	if v["n"] != float64(3) {
		t.Errorf("n = %v, want 3", v["n"])
	}
}

// TestJSONCUnterminatedBlockCommentFailsClosed: an unterminated /* must return
// an error, never be silently stripped into valid JSON.
func TestJSONCUnterminatedBlockCommentFailsClosed(t *testing.T) {
	if _, err := stripJSONCComments(`{"a":1} /* never closed`); err == nil {
		t.Fatal("expected an error for an unterminated block comment")
	}
}

// TestJSONCStrayTrailingCommaStaysInvalid: a comma that is NOT immediately
// before a } or ] (e.g. after the root object, possibly followed only by a
// comment) must be preserved so json.Unmarshal still rejects the malformed
// document — the scanner must never rewrite it into valid JSON.
func TestJSONCStrayTrailingCommaStaysInvalid(t *testing.T) {
	for _, in := range []string{`{"a":1},`, "{\"a\":1}, // tail", `{"a":1}, /* tail */`} {
		cleaned, err := stripJSONCComments(in)
		if err != nil {
			continue // erroring is an acceptable fail-closed outcome
		}
		var v any
		if json.Unmarshal([]byte(cleaned), &v) == nil {
			t.Errorf("malformed %q was silently accepted as valid JSON (cleaned=%q)", in, cleaned)
		}
	}
}

// TestJSONCPreservesCommentLikeStringContent: // and /* inside strings survive.
func TestJSONCPreservesCommentLikeStringContent(t *testing.T) {
	in := `{"url": "https://x.example/a", "glob": "/* not a comment */"}`
	v := mustParseJSONC(t, in)
	if v["url"] != "https://x.example/a" {
		t.Errorf("url = %q", v["url"])
	}
	if v["glob"] != "/* not a comment */" {
		t.Errorf("glob = %q", v["glob"])
	}
}

// FuzzJSONC: whenever the scanner returns cleaned bytes that parse, they must
// parse to the same value as the input parsed as strict JSON (when the input
// itself is already strict JSON) — i.e. the transform never corrupts content.
func FuzzJSONC(f *testing.F) {
	f.Add(`{"a":1}`)
	f.Add(`{"a": "b//c", "d": [1,2]}`)
	f.Add(`{"p":"C:\\"}`)
	f.Fuzz(func(t *testing.T, in string) {
		var direct any
		if json.Unmarshal([]byte(in), &direct) != nil {
			return // only reason about inputs that are already valid JSON
		}
		cleaned, err := stripJSONCComments(in)
		if err != nil {
			t.Fatalf("valid JSON %q made the scanner error: %v", in, err)
		}
		var viaScanner any
		if err := json.Unmarshal([]byte(cleaned), &viaScanner); err != nil {
			t.Fatalf("valid JSON %q became invalid after scan: %q (%v)", in, cleaned, err)
		}
		a, _ := json.Marshal(direct)
		b, _ := json.Marshal(viaScanner)
		// Exact byte compare: json.Marshal on equal map[string]any trees is
		// deterministic, so any difference (including a case mutation, which a
		// case-insensitive compare would hide) is real corruption.
		if string(a) != string(b) {
			t.Fatalf("scan changed the value: %s -> %s", a, b)
		}
	})
}

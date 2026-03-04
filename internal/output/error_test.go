package output

import (
	"strings"
	"testing"
)

func TestRenderError_WithSuggestions(t *testing.T) {
	e := ErrorDetail{
		Title:       "Something failed",
		Context:     map[string]string{"Error": "connection refused"},
		Suggestions: []string{"Check your config", "Try again"},
	}
	got := RenderError(e)
	if !strings.Contains(got, "Something failed") {
		t.Error("expected title in output")
	}
	if !strings.Contains(got, "Check your config") {
		t.Error("expected first suggestion in output")
	}
	if !strings.Contains(got, "Try again") {
		t.Error("expected second suggestion in output")
	}
}

func TestRenderError_EmptyContext(t *testing.T) {
	e := ErrorDetail{
		Title: "Error occurred",
	}
	got := RenderError(e)
	if !strings.Contains(got, "Error occurred") {
		t.Error("expected title in output")
	}
}

package output

import (
	"strings"
	"testing"
)

func TestNewTable_RendersHeaders(t *testing.T) {
	tbl := NewTable([]string{"NAME", "STATUS"}).
		Rows([]string{"test", "running"})

	got := tbl.String()
	if !strings.Contains(got, "NAME") {
		t.Error("expected NAME header in table output")
	}
	if !strings.Contains(got, "STATUS") {
		t.Error("expected STATUS header in table output")
	}
}

func TestRenderSection_ContainsTitle(t *testing.T) {
	got := RenderSection("My Section", "some content")
	if !strings.Contains(got, "My Section") {
		t.Error("expected title in section output")
	}
	if !strings.Contains(got, "some content") {
		t.Error("expected content in section output")
	}
}

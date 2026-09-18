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

package output

import "testing"

func TestStatusText_AllStatuses(t *testing.T) {
	statuses := []string{"running", "stopped", "notcreated", "", "busy", "starting", "healthy", "unhealthy", "unknown"}
	for _, s := range statuses {
		got := StatusText(s)
		if got == "" {
			t.Errorf("StatusText(%q) returned empty string", s)
		}
	}
}

func TestStatusIcon_AllStatuses(t *testing.T) {
	statuses := []string{"running", "stopped", "notcreated", "", "busy", "starting", "healthy", "unhealthy", "unknown"}
	for _, s := range statuses {
		got := StatusIcon(s)
		if got == "" {
			t.Errorf("StatusIcon(%q) returned empty string", s)
		}
	}
}

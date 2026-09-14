package output

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

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

// §4.6 requires each role to declare its value explicitly PER LEVEL rather than
// relying on termenv's automatic downsample, "which was measured to invert the
// palette at 16 colours (Green renders yellow, Blue renders green)". The palette
// is data the spec fixes, so the declared values are asserted verbatim — and
// then the round-trip below asserts the level actually SELECTS the declared
// value, which is what stops this from being decoration.
//
// Measured before this test existed: inverting RoleOK and RoleInfo at 256 and 16
// left the whole suite green (`ok wsrender 97.977s`). That is the defect this
// test exists to catch.
func TestPaletteDeclaresEveryLevel(t *testing.T) {
	want := map[Role][3]string{
		//            truecolour   256    16
		RoleOK:     {"#b8bb26", "142", "2"},
		RoleWarn:   {"#fabd2f", "214", "3"},
		RoleFail:   {"#fb4934", "167", "1"},
		RoleInfo:   {"#83a598", "109", "4"},
		RoleMuted:  {"#928374", "245", "8"},
		RoleAccent: {"#d3869b", "175", "5"},
	}
	levels := [3]ColourLevel{ColourTrue, Colour256, Colour16}

	for role, values := range want {
		for i, level := range levels {
			got, ok := colourFor(role, level)
			if !ok {
				t.Errorf("role %d at level %d: no colour declared", role, level)
				continue
			}
			if string(got.(lipgloss.Color)) != values[i] {
				t.Errorf("role %d at level %d = %q, want %q", role, level, got, values[i])
			}
		}
	}

	// RoleDefault never emits SGR, at any level.
	for _, level := range []ColourLevel{ColourNone, Colour16, Colour256, ColourTrue} {
		if _, ok := colourFor(RoleDefault, level); ok {
			t.Errorf("RoleDefault declared a colour at level %d; it must emit none", level)
		}
	}
	// ColourNone silences every role.
	for role := RoleOK; role <= RoleAccent; role++ {
		if _, ok := colourFor(role, ColourNone); ok {
			t.Errorf("role %d declared a colour at ColourNone", role)
		}
	}
}

// The three states an operator must never confuse have to stay distinguishable
// wherever colour is available at all. This is the half of the assertion that is
// a property rather than a transcription: it fails on any future palette edit
// that collapses two of them, which a verbatim table alone would not catch.
func TestStateRolesStayDistinct(t *testing.T) {
	for _, level := range []ColourLevel{Colour16, Colour256, ColourTrue} {
		seen := map[string]Role{}
		for _, role := range []Role{RoleOK, RoleWarn, RoleFail} {
			c, ok := colourFor(role, level)
			if !ok {
				t.Fatalf("role %d has no colour at level %d", role, level)
			}
			v := string(c.(lipgloss.Color))
			if prev, dup := seen[v]; dup {
				t.Errorf("level %d: roles %d and %d both render as %q", level, prev, role, v)
			}
			seen[v] = role
		}
	}
}

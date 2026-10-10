package output

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// fieldView runs prompt on a core drawn on s whose runner focuses the form's
// field and captures it as drawn, then completes the form.
func fieldView(s *Stream, prompt func(p *prompter)) string {
	var view string
	p := coreFor(make(chan os.Signal, 3), func(f *huh.Form) error {
		f.Init()
		view = f.GetFocusedField().View()
		f.State = huh.StateCompleted
		return nil
	})
	p.stream = s
	prompt(p)
	return view
}

func deleteOps(p *prompter) { _, _ = p.confirm(`Delete workspace "ops"?`, "This cannot be undone.") }

func selectWorkspace(p *prompter) { _, _ = p.choose("Select workspace:", threeWorkspaces()) }

// sgrOf is the escape sequence role opens with on s.
func sgrOf(s *Stream, role Role) string {
	painted := s.Style(role).Render("x")
	return painted[:strings.Index(painted, "x")]
}

// TestPromptThemeWithoutColour: with no colour the focused button is told
// apart by brackets — [Yes] or [No], the other bare — and no ESC is written,
// in both glyph modes.
func TestPromptThemeWithoutColour(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		s := NewStreamAt(io.Discard, 80, true, ColourNone, ascii)
		for _, yes := range []bool{true, false} {
			value := yes
			field := huh.NewConfirm().Title("Delete?").Affirmative("Yes").Negative("No").Value(&value)
			form := huh.NewForm(huh.NewGroup(field)).WithTheme(promptTheme(s)).WithWidth(40)
			form.Init()
			view := form.GetFocusedField().View()
			focused, blurred := "[No]", "[Yes]"
			if yes {
				focused, blurred = "[Yes]", "[No]"
			}
			if !strings.Contains(view, focused) || strings.Contains(view, blurred) {
				t.Errorf("ascii %t, value %t: want %s focused and %s bare:\n%s", ascii, yes, focused, blurred, view)
			}
			if strings.Contains(view, "\x1b") {
				t.Errorf("ascii %t, value %t: an ESC with no colour: %q", ascii, yes, view)
			}
		}
	}
}

// TestPromptThemeGlyphs: the glyphs follow the stream's mode — the title's
// prefix is the advisory mark, ⚠ or !, and the left bar ┃ or | — and the
// selector is > in both.
func TestPromptThemeGlyphs(t *testing.T) {
	for _, tc := range []struct {
		ascii     bool
		bar, mark string
	}{{false, "┃", "⚠"}, {true, "|", "!"}} {
		s := NewStreamAt(io.Discard, 80, true, ColourNone, tc.ascii)
		if view, head := fieldView(s, deleteOps), tc.bar+" "+tc.mark+" Delete workspace"; !strings.HasPrefix(view, head) {
			t.Errorf("ascii %t: the Confirm does not begin %q:\n%s", tc.ascii, head, view)
		}
		view := fieldView(s, selectWorkspace)
		if !strings.Contains(view, tc.bar+" > api") {
			t.Errorf("ascii %t: the Select's selector is not > after the bar %s:\n%s", tc.ascii, tc.bar, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if !strings.HasPrefix(line, tc.bar) {
				t.Errorf("ascii %t: a line of the Select without the bar %s: %q", tc.ascii, tc.bar, line)
			}
		}
	}
}

// TestPromptThemeColour: with colour, the title and the focused button carry
// the accent role's SGR for the stream's level, and the blurred button none.
func TestPromptThemeColour(t *testing.T) {
	for _, level := range []ColourLevel{Colour16, Colour256, ColourTrue} {
		s := NewStreamAt(io.Discard, 80, true, level, false)
		accent := sgrOf(s, RoleAccent)
		view := fieldView(s, deleteOps)
		if !strings.Contains(view, accent+`⚠ Delete workspace "ops"?`) {
			t.Errorf("level %d: the title does not carry the accent %q: %q", level, accent, view)
		}
		if !strings.Contains(view, accent+"No") || strings.Contains(view, accent+"Yes") || strings.Contains(view, "[No]") {
			t.Errorf("level %d: want the focused No alone in the accent %q, without brackets: %q", level, accent, view)
		}
	}
}

// eachStyle calls fn on every lipgloss.Style reachable through v's exported
// fields, by its path, and returns how many it visited.
func eachStyle(v reflect.Value, path string, fn func(path string, st lipgloss.Style)) int {
	if st, ok := v.Interface().(lipgloss.Style); ok {
		fn(path, st)
		return 1
	}
	if v.Kind() != reflect.Struct {
		return 0
	}
	n := 0
	for i := 0; i < v.NumField(); i++ {
		if f := v.Type().Field(i); f.IsExported() {
			n += eachStyle(v.Field(i), path+"."+f.Name, fn)
		}
	}
	return n
}

// TestPromptThemeColoursAreRoles: every colour of the theme, at every level,
// is a role of the layer's palette at that level: a foreground or none, and
// no background or border colour — none of huh's own colours, and no
// AdaptiveColor, which asks the terminal for its background.
func TestPromptThemeColoursAreRoles(t *testing.T) {
	for _, level := range []ColourLevel{ColourNone, Colour16, Colour256, ColourTrue} {
		s := NewStreamAt(io.Discard, 80, true, level, false)
		roles := map[lipgloss.TerminalColor]bool{lipgloss.NoColor{}: true}
		for role := RoleDefault; role <= RoleAccent; role++ {
			roles[s.Style(role).GetForeground()] = true
		}
		visited := eachStyle(reflect.ValueOf(*promptTheme(s)), "Theme", func(path string, st lipgloss.Style) {
			if fg := st.GetForeground(); !roles[fg] {
				t.Errorf("level %d: %s's foreground %#v is no role's colour", level, path, fg)
			}
			for name, c := range map[string]lipgloss.TerminalColor{
				"background":               st.GetBackground(),
				"top border foreground":    st.GetBorderTopForeground(),
				"right border foreground":  st.GetBorderRightForeground(),
				"bottom border foreground": st.GetBorderBottomForeground(),
				"left border foreground":   st.GetBorderLeftForeground(),
				"top border background":    st.GetBorderTopBackground(),
				"right border background":  st.GetBorderRightBackground(),
				"bottom border background": st.GetBorderBottomBackground(),
				"left border background":   st.GetBorderLeftBackground(),
			} {
				if c != (lipgloss.NoColor{}) {
					t.Errorf("level %d: %s has a %s, %#v; a prompt's colours are foreground roles only", level, path, name, c)
				}
			}
		})
		if visited < 50 {
			t.Fatalf("level %d: the walk visited %d styles; the theme holds more, so it cannot see them", level, visited)
		}
	}
}

// TestPromptFitsTheBudget: a prompt is laid out to fit its stream's budget:
// no line of a Confirm with a title that wraps, or of a Select, is wider.
func TestPromptFitsTheBudget(t *testing.T) {
	long := func(p *prompter) {
		_, _ = p.confirm(`Delete workspace "web-frontend"? This removes the container, its volume and its devpod state for good.`,
			"Every file inside the workspace that was not pushed or copied out is lost with it.")
	}
	for _, budget := range []int{MinWidth, 40, 80} {
		for _, ascii := range []bool{false, true} {
			s := NewStreamAt(io.Discard, budget, true, ColourTrue, ascii)
			for name, prompt := range map[string]func(*prompter){"Confirm": long, "Select": selectWorkspace} {
				for _, line := range strings.Split(fieldView(s, prompt), "\n") {
					if w := ansi.StringWidth(line); w > budget {
						t.Errorf("%s at %d, ascii %t: a line %d cells wide: %q", name, budget, ascii, w, line)
					}
				}
			}
		}
	}
}

package output

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// promptTheme is the theme of a prompt drawn on s. Every style is built from
// s.Style — so it is painted by s's own renderer, at s's colour level, in a
// role of the layer's palette — and none is inherited from huh, whose themes
// name colour values of their own and ask the terminal for its background:
//
//	title, a group's title                             RoleAccent
//	description, help, placeholder                     RoleMuted
//	focused button, selector, selected option, cursor  RoleAccent
//	blurred button, other options, text                RoleDefault
//	error message and indicator                        RoleFail
//
// Without colour — NO_COLOR, or a stream whose styles carry no SGR — the
// focused button is still told apart: it is drawn [Yes] or [No], through a
// Transform that applies whatever the colour profile. Glyphs follow s's
// glyph mode: the left bar is ┃ or |; the selector is > in both.
func promptTheme(s *Stream) *huh.Theme {
	plain := s.Style(RoleDefault)
	accent, muted, fail := s.Style(RoleAccent), s.Style(RoleMuted), s.Style(RoleFail)

	bar, next, prev := lipgloss.ThickBorder(), "→", "←"
	if s.mode == GlyphASCII {
		bar, next, prev = lipgloss.Border{Left: "|"}, ">", "<"
	}

	button := plain.Padding(0, 2).MarginRight(1)
	focusedButton := accent.Padding(0, 2).MarginRight(1)
	if s.level == ColourNone {
		focusedButton = focusedButton.Transform(func(label string) string { return "[" + label + "]" })
	}

	var f huh.FieldStyles
	f.Base = plain.PaddingLeft(1).BorderStyle(bar).BorderLeft(true)
	f.Card = f.Base
	f.Title = accent
	f.NoteTitle = accent
	f.Description = muted
	f.ErrorIndicator = fail.SetString(" *")
	f.ErrorMessage = fail.SetString(" *")
	f.SelectSelector = accent.SetString("> ")
	f.Option = plain
	f.NextIndicator = accent.MarginLeft(1).SetString(next)
	f.PrevIndicator = accent.MarginRight(1).SetString(prev)
	f.Directory = accent
	f.File = plain
	f.MultiSelectSelector = accent.SetString("> ")
	f.SelectedOption = accent
	f.SelectedPrefix = accent.SetString("[x] ")
	f.UnselectedOption = plain
	f.UnselectedPrefix = plain.SetString("[ ] ")
	f.TextInput = huh.TextInputStyles{Cursor: accent, CursorText: plain, Placeholder: muted, Prompt: accent, Text: plain}
	f.FocusedButton = focusedButton
	f.BlurredButton = button
	f.Next = focusedButton

	var t huh.Theme
	t.Form.Base = plain
	t.Group.Base = plain
	t.Group.Title = accent
	t.Group.Description = muted
	t.FieldSeparator = plain.SetString("\n\n")
	t.Focused = f
	t.Blurred = f
	t.Blurred.Base = f.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.MultiSelectSelector = plain.SetString("  ")
	t.Blurred.NextIndicator = plain
	t.Blurred.PrevIndicator = plain
	t.Help.Ellipsis = muted
	t.Help.ShortKey, t.Help.ShortDesc, t.Help.ShortSeparator = muted, muted, muted
	t.Help.FullKey, t.Help.FullDesc, t.Help.FullSeparator = muted, muted, muted
	return &t
}

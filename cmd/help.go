package cmd

import (
	"fmt"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The help document.
//
// cobra renders help from a text/template. ws renders it here, from the
// render layer's blocks, for the stream it is written to: its width and its
// colour are that stream's. The template could do neither. Its one wrapping
// primitive, pflag's FlagUsagesWrapped, counts bytes and stops wrapping below
// about 40 columns, where the product floor is 29, and its headings were
// styled once, at package init, for stdout.

// commandGroups are the group annotations of the root's children, in the
// order the root's help lists them. Below the root every child is listed
// under one "Commands:" heading, so the annotation is read on the root's
// children only.
var commandGroups = []struct{ annotation, title string }{
	{"workspace", "Workspace Commands:"},
	{"profile", "Profile Commands:"},
	{"proxy", "Proxy Commands:"},
	{"vault", "Vault Commands:"},
}

// helpFunc is the root's help function. It renders cmd's help for
// output.Out() and writes it to cmd.OutOrStdout(). A help function has no
// error return, so a failed write is reported on stderr and leaves the exit
// code alone, as cobra's own help function does.
func helpFunc(cmd *cobra.Command, _ []string) {
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), renderHelp(output.Out(), cmd, true)); err != nil {
		cmd.PrintErrln(err)
	}
}

// renderHelp lays out cmd's help document for s. The sections, in order: the
// description (Long, else Short; only when withDescription is set), the
// synopsis, the aliases, the examples, the command lists, the flags, the
// inherited flags and the footer. Each is printed only when it has content,
// one blank line from the next. The section titles are painted RoleAccent
// and nothing else carries colour.
func renderHelp(s *output.Stream, cmd *cobra.Command, withDescription bool) string {
	// cobra adds -h/--help, and -v/--version on the root, when it executes a
	// command, and its help command adds them before calling Help. Adding them
	// here as well makes the document the same however it was reached.
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()

	budget := s.Budget()
	var sections []string
	if withDescription {
		text := cmd.Long
		if text == "" {
			text = cmd.Short
		}
		if text = strings.TrimRight(text, " \t\n"); text != "" {
			sections = append(sections, strings.Join(layOut(text, 0, budget), "\n"))
		}
	}

	// Wrapped at budget-4, then indented 2 on the first line and 4 on the
	// rest, so that no line is wider than the budget.
	usage := output.Wrap(synopsis(cmd), budget-4)
	for i := range usage {
		if i == 0 {
			usage[i] = "  " + usage[i]
		} else {
			usage[i] = "    " + usage[i]
		}
	}
	sections = append(sections, titled(s, "Usage:", usage))

	if len(cmd.Aliases) > 0 {
		aliases := output.Wrap(cmd.NameAndAliases(), budget-2)
		for i := range aliases {
			aliases[i] = "  " + aliases[i]
		}
		sections = append(sections, titled(s, "Aliases:", aliases))
	}
	if example := strings.TrimRight(cmd.Example, " \t\n"); example != "" {
		sections = append(sections, titled(s, "Examples:", layOut(example, 2, budget)))
	}
	for _, list := range commandLists(cmd) {
		sections = append(sections, list.Render(s))
	}
	if pairs := flagPairs(cmd.LocalFlags()); len(pairs) > 0 {
		sections = append(sections, output.KV{Title: "Flags:", Pairs: pairs, PlainKeys: true}.Render(s))
	}
	if pairs := flagPairs(cmd.InheritedFlags()); len(pairs) > 0 {
		sections = append(sections, output.KV{Title: "Global Flags:", Pairs: pairs, PlainKeys: true}.Render(s))
	}
	if cmd.HasAvailableSubCommands() {
		footer := `Use "` + cmd.CommandPath() + ` [command] --help" for more information about a command.`
		sections = append(sections, strings.Join(output.Wrap(footer, budget), "\n"))
	}
	return strings.Join(sections, "\n\n")
}

// synopsis is cmd's one-line usage: its use line for a leaf, and its path
// followed by " [command]" for a command with subcommands.
func synopsis(cmd *cobra.Command) string {
	if cmd.HasAvailableSubCommands() {
		return cmd.CommandPath() + " [command]"
	}
	return cmd.UseLine()
}

// titled is a section whose title is not a block's own: the title painted
// RoleAccent, as a block paints its title, then the lines under it.
func titled(s *output.Stream, title string, lines []string) string {
	return s.Style(output.RoleAccent).Render(title) + "\n" + strings.Join(lines, "\n")
}

// layOut lays text out one source line at a time, for Long and Example. A
// line keeps its leading indentation, plus extra; the rest of it is wrapped
// at the budget minus that indentation, and its continuation lines take the
// same indentation. An empty line stays empty.
//
// output.Wrap alone keeps the lines apart — a newline is a paragraph break to
// it — but it drops each line's leading whitespace, which is what sets the
// command in upgrade-config's description apart from the prose around it.
func layOut(text string, extra, budget int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		body := strings.TrimLeft(line, " \t")
		if strings.TrimSpace(body) == "" {
			out = append(out, "")
			continue
		}
		// At least two cells are left for the text: output.Wrap emits a
		// grapheme cluster wider than its budget whole, and the widest is two.
		indent := min(output.W(line[:len(line)-len(body)])+extra, budget-2)
		prefix := strings.Repeat(" ", indent)
		for _, l := range output.Wrap(body, budget-indent) {
			out = append(out, prefix+l)
		}
	}
	return out
}

// commandLists are cmd's available children, and cobra's help command, as
// KV blocks: one per group at the root, then "Additional Commands:" for the
// children with no group, and one "Commands:" list below the root.
func commandLists(cmd *cobra.Command) []output.KV {
	var listed []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() || c.Name() == "help" {
			listed = append(listed, c)
		}
	}
	if len(listed) == 0 {
		return nil
	}
	if cmd.HasParent() {
		return []output.KV{commandList("Commands:", listed)}
	}
	var lists []output.KV
	grouped := map[*cobra.Command]bool{}
	for _, g := range commandGroups {
		var members []*cobra.Command
		for _, c := range listed {
			if c.Annotations["group"] == g.annotation {
				members = append(members, c)
				grouped[c] = true
			}
		}
		if len(members) > 0 {
			lists = append(lists, commandList(g.title, members))
		}
	}
	var rest []*cobra.Command
	for _, c := range listed {
		if !grouped[c] {
			rest = append(rest, c)
		}
	}
	if len(rest) > 0 {
		lists = append(lists, commandList("Additional Commands:", rest))
	}
	return lists
}

func commandList(title string, cmds []*cobra.Command) output.KV {
	pairs := make([]output.Fact, 0, len(cmds))
	for _, c := range cmds {
		pairs = append(pairs, output.Fact{K: c.Name(), V: c.Short})
	}
	return output.KV{Title: title, Pairs: pairs, PlainKeys: true}
}

// flagPairs are the flags of fs as the rows of a KV block, in pflag's order,
// without the hidden and the deprecated ones. The key is pflag's left column
// and the description its right one, both built from pflag's exported data;
// TestFlagRowsMatchPflag holds them to pflag's own rendering.
func flagPairs(fs *pflag.FlagSet) []output.Fact {
	var pairs []output.Fact
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Deprecated != "" {
			return
		}
		pairs = append(pairs, output.Fact{K: flagKey(f), V: flagDescription(f)})
	})
	return pairs
}

// flagKey is pflag's left column for f without its two-space indent:
// "-s, --name type", or "    --name type" without a usable shorthand, with
// "[=value]" when the flag has a NoOptDefVal.
func flagKey(f *pflag.Flag) string {
	key := "    --" + f.Name
	if f.Shorthand != "" && f.ShorthandDeprecated == "" {
		key = "-" + f.Shorthand + ", --" + f.Name
	}
	if varname, _ := pflag.UnquoteUsage(f); varname != "" {
		key += " " + varname
	}
	if f.NoOptDefVal != "" {
		switch f.Value.Type() {
		case "string":
			key += `[="` + f.NoOptDefVal + `"]`
		case "bool", "boolfunc":
			if f.NoOptDefVal != "true" {
				key += "[=" + f.NoOptDefVal + "]"
			}
		case "count":
			if f.NoOptDefVal != "+1" {
				key += "[=" + f.NoOptDefVal + "]"
			}
		default:
			key += "[=" + f.NoOptDefVal + "]"
		}
	}
	return key
}

// flagDescription is pflag's right column for f: the usage, with the
// back-quoted name unquoted, then " (default …)" when the default is not
// the zero value of the flag's type.
func flagDescription(f *pflag.Flag) string {
	_, usage := pflag.UnquoteUsage(f)
	if !defaultIsZero(f) {
		if f.Value.Type() == "string" {
			usage += fmt.Sprintf(" (default %q)", f.DefValue)
		} else {
			usage += " (default " + f.DefValue + ")"
		}
	}
	return usage
}

// defaultIsZero is pflag's defaultIsZeroValue, which pflag does not export.
// pflag switches on its own concrete value types; this reads Type() instead,
// which names the same types.
func defaultIsZero(f *pflag.Flag) bool {
	if _, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return f.DefValue == "false" || f.DefValue == ""
	}
	switch f.Value.Type() {
	case "duration":
		return f.DefValue == "0" || f.DefValue == "0s"
	case "int", "int8", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "count", "float32", "float64":
		return f.DefValue == "0"
	case "string":
		return f.DefValue == ""
	case "ip", "ipMask", "ipNet":
		return f.DefValue == "<nil>"
	case "intSlice", "stringSlice", "stringArray":
		return f.DefValue == "[]"
	}
	switch f.DefValue {
	case "false", "<nil>", "", "0":
		return true
	}
	return false
}

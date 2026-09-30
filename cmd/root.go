package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

var version = "dev"

// logoLines is the logo ws --version prints above the version on a UTF-8
// terminal.
var logoLines = []string{"╦ ╦╔═╗", "║║║╚═╗", "╚╩╝╚═╝"}

// versionTemplate is the --version template for s, the stream the version
// is written to. It is the version alone unless s is a terminal in the UTF-8
// glyph mode; there the logo comes first, painted RoleAccent — without
// colour under NO_COLOR. Piped, the output is exactly "ws <version>\n", so
// `ws --version | head -1` is the version.
func versionTemplate(s *output.Stream) string {
	const versionLine = "ws {{.Version}}\n"
	if !s.IsTTY() || s.Mode() != output.GlyphUTF8 {
		return versionLine
	}
	var b strings.Builder
	for _, line := range logoLines {
		b.WriteString(s.Style(output.RoleAccent).Render(line) + "\n")
	}
	return b.String() + versionLine
}

var rootCmd = &cobra.Command{
	Use:   "ws",
	Short: "Workspace manager for DevPod",
	Long:  "ws — workspace manager CLI for DevPod environments with proxy support.",
	// The root prints every error itself — Execute, below — so cobra must
	// not: with both printing, each error appeared twice, as "Error: <msg>"
	// and again as the ✗ line. SilenceUsage is deliberately NOT set here. A
	// command sets it in its own body once it starts work, which keeps the
	// Usage: block on an argument error and drops it on a runtime error; set
	// on the root, it would drop the block on both.
	SilenceErrors: true,
}

// cliErrorWithExit is the leaf-level error type that surfaces a non-default
// exit code. Per CONTEXT D-18 + envelope.go MapErrorCodeToExitCode, exit
// codes 0-7 map to the documented MCP error codes; a leaf wraps the failing
// envelope into a cliErrorWithExit{code, msg} and the process exits with code.
//
// A non-empty msg is printed once, by the root, like any other error. An
// empty msg, unwrapped, prints nothing: the command has already printed
// everything the operator needs — vault-health-score's score on stdout, a
// status report — and the exit code is the rest of its interface.
type cliErrorWithExit struct {
	code int
	msg  string
}

func (e *cliErrorWithExit) Error() string { return e.msg }

// run is the root's error protocol. Given what the command tree returned, it
// decides what the root prints and what the process exits with; msg is
// printed through output.Fail, and an empty msg prints nothing. Four branches
// and nothing else:
//
//	nil                                     -> "", 0
//	*cliErrorWithExit, err.Error() == ""    -> "", cerr.code
//	*cliErrorWithExit, err.Error() != ""    -> err.Error(), cerr.code
//	any other error                         -> err.Error(), 1
//
// The two *cliErrorWithExit branches test and print err.Error(), not
// cerr.msg: errors.As also finds one that another error wraps, and cerr.msg
// would drop the wrapper's text. Unwrapped, the two are the same string.
func run(err error) (msg string, code int) {
	if err == nil {
		return "", 0
	}
	var cerr *cliErrorWithExit
	if errors.As(err, &cerr) {
		return err.Error(), cerr.code
	}
	return err.Error(), 1
}

// Execute runs the command tree. An error that reaches it is printed at most
// once, through output.Fail, and the process exits with the code run chose.
func Execute() {
	// Built here rather than at package init, so that it is built for the
	// stream --version writes to, once that stream has been resolved.
	rootCmd.SetVersionTemplate(versionTemplate(output.Out()))
	cmd, err := rootCmd.ExecuteC()
	msg, code := run(err)
	if msg != "" {
		output.Fail(msg)
		if isUnknownCommand(err) && !cmd.HasParent() {
			_, _ = fmt.Fprintf(output.Err(), "Run '%s --help' for usage.\n", cmd.CommandPath())
		}
	}
	if code != 0 {
		os.Exit(code)
	}
}

// isUnknownCommand reports whether err is cobra's own "unknown command"
// error. Cobra follows that error with a "Run '<path> --help' for usage."
// hint, but only when errors are not silenced, and the root silences them;
// Execute prints the hint instead. Cobra builds the error with fmt.Errorf and
// exports no type for it, so its text is the only handle.
//
// Two different errors carry that text: the one the root raises for a command
// it cannot find (args.go legacyArgs), which cobra follows with the hint, and
// the one cobra.NoArgs raises for an extra argument (args.go NoArgs), which it
// does not. Only the first can come back from a command with no parent, which
// is how Execute tells them apart.
func isUnknownCommand(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "unknown command ")
}

func init() {
	rootCmd.Version = version
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	// Every command inherits --json, and only some of them read it; the
	// usage says so, rather than promise an effect a command does not have.
	rootCmd.PersistentFlags().Bool("json", false, "Output in JSON format, where the command supports it")
	rootCmd.SetHelpFunc(helpFunc)

	cobra.AddTemplateFunc("groupTag", func(cmd *cobra.Command) []string {
		if tag, ok := cmd.Annotations["group"]; ok {
			return []string{tag}
		}
		return []string{""}
	})

	rootCmd.SetUsageTemplate(groupedUsageTemplate)

	// Validate template at init time.
	template.Must(template.New("usage").Funcs(template.FuncMap{
		"groupTag": func(cmd *cobra.Command) []string { return []string{""} },
		"rpad": func(s string, p int) string {
			return fmt.Sprintf(fmt.Sprintf("%%-%ds", p), s)
		},
		"trimTrailingWhitespaces": func(s string) string { return s },
	}).Parse(groupedUsageTemplate))
}

var groupedUsageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}

{{- if .HasAvailableSubCommands}}

` + output.SectionStyle.Render("Workspace Commands:") + `{{range .Commands}}{{if (eq (index (groupTag .) 0) "workspace")}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}

` + output.SectionStyle.Render("Profile Commands:") + `{{range .Commands}}{{if (eq (index (groupTag .) 0) "profile")}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}

` + output.SectionStyle.Render("Proxy Commands:") + `{{range .Commands}}{{if (eq (index (groupTag .) 0) "proxy")}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}

` + output.SectionStyle.Render("Vault Commands:") + `{{range .Commands}}{{if (eq (index (groupTag .) 0) "vault")}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}
{{- end}}

{{if .HasAvailableLocalFlags}}Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}

Use "{{.CommandPath}} [command] --help" for more information about a command.
`

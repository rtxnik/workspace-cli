package cmd

import (
	"errors"
	"fmt"
	"os"
	"text/template"

	"github.com/charmbracelet/lipgloss"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

var version = "dev"

// logo renders a compact ASCII logo with gruvbox gradient.
func logo() string {
	lines := []struct {
		text  string
		color lipgloss.Color
	}{
		{"╦ ╦╔═╗", output.Orange},
		{"║║║╚═╗", output.Yellow},
		{"╚╩╝╚═╝", output.Green},
	}
	var s string
	for _, l := range lines {
		s += lipgloss.NewStyle().Foreground(l.color).Bold(true).Render(l.text) + "\n"
	}
	return s
}

var rootCmd = &cobra.Command{
	Use:   "ws",
	Short: "Workspace manager for DevPod",
	Long:  "ws — workspace manager CLI for DevPod environments with proxy support.",
}

// cliErrorWithExit is the leaf-level error type used by `ws vault` Cobra
// leaves to surface a non-default exit code without losing Cobra's "Error:"
// rendering. Per CONTEXT D-18 + envelope.go MapErrorCodeToExitCode, exit
// codes 0-7 map to the documented MCP error codes; leaves wrap the failing
// envelope into a cliErrorWithExit{code, msg} so Execute() can route the
// code to os.Exit while Cobra still prints the Error: msg line.
//
// An empty Msg suppresses the "Error:" line — used by vault-health-score
// (CLI-09) which exits 1/2 on yellow/red bands but emits NO error text
// (machine-parseable score on stdout is the whole interface). When Msg is
// empty, Execute() exits with code without rendering anything.
type cliErrorWithExit struct {
	code int
	msg  string
}

func (e *cliErrorWithExit) Error() string { return e.msg }

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// Honour leaf-level explicit exit codes (cliErrorWithExit) so the
		// `ws vault` family can map MCP envelope error codes to Unix exit
		// codes per CONTEXT D-18 + ADR-int-03. Other commands keep the
		// historical Die() behaviour (red ✗ + exit 1).
		var cerr *cliErrorWithExit
		if errors.As(err, &cerr) {
			os.Exit(cerr.code)
		}
		output.Die(err.Error())
	}
}

func init() {
	rootCmd.Version = version
	rootCmd.SetVersionTemplate(logo() + "ws {{.Version}}\n")
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.PersistentFlags().Bool("json", false, "Output in JSON format")

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

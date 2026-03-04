package cmd

import (
	"fmt"
	"text/template"

	"github.com/charmbracelet/lipgloss"
	"github.com/rtxnik/ws/internal/output"
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

func Execute() {
	if err := rootCmd.Execute(); err != nil {
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
{{- end}}

{{if .HasAvailableLocalFlags}}Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}

Use "{{.CommandPath}} [command] --help" for more information about a command.
`

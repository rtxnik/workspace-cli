package cmd

import (
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

// profileCmd is the depth-3 parent for `ws proxy profile *` per CONTEXT.md D-01.
// Plans 02-05 fill in the leaf Run bodies; this file ships only the registration
// scaffolding so all downstream plans land their commands in parallel without
// merge conflicts on this file.
var profileCmd = &cobra.Command{
	Use:         "profile",
	Short:       "Manage xray VLESS profiles",
	Long:        "Lifecycle management for named xray VLESS configurations.\nReplaces the legacy ad-hoc xray-switch shell wrapper.",
	Annotations: proxyAnnotation,
}

var profileAddCmd = &cobra.Command{
	Use:         "add <name> <vless-uri>",
	Short:       "Add a new profile from a VLESS URI",
	Args:        cobra.ExactArgs(2),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		output.Die("not implemented; pending Plan 22-02 (profile add)")
	},
}

var profileListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List profiles in a table or JSON",
	Args:        cobra.NoArgs,
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		output.Die("not implemented; pending Plan 22-02 (profile list)")
	},
}

var profileUseCmd = &cobra.Command{
	Use:         "use <name>",
	Short:       "Validate, swap, and restart to <name>",
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		output.Die("not implemented; pending Plan 22-03 (profile use)")
	},
}

var profileRmCmd = &cobra.Command{
	Use:         "rm <name>",
	Short:       "Remove a profile (refuses active)",
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		output.Die("not implemented; pending Plan 22-05 (profile rm)")
	},
}

var profileShowCmd = &cobra.Command{
	Use:         "show <name>",
	Short:       "Show profile (masked unless --reveal)",
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		output.Die("not implemented; pending Plan 22-02 (profile show)")
	},
}

var profileCurrentCmd = &cobra.Command{
	Use:         "current",
	Short:       "Print the currently active profile",
	Args:        cobra.NoArgs,
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		output.Die("not implemented; pending Plan 22-02 (profile current)")
	},
}

var profileRegenCmd = &cobra.Command{
	Use:         "regenerate <name>",
	Short:       "Refresh routing rules in <name> from the currently-active profile",
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		output.Die("not implemented; pending Plan 22-03 (profile regenerate)")
	},
}

func init() {
	profileCmd.PersistentFlags().Bool("no-migrate", false, "Refuse auto-migration of legacy regular-file config.json; error out instead")

	profileCmd.AddCommand(profileAddCmd)
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileUseCmd)
	profileCmd.AddCommand(profileRmCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileCurrentCmd)
	profileCmd.AddCommand(profileRegenCmd)
}

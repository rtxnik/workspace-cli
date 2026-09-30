package cmd

import (
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/profile"
	"github.com/rtxnik/workspace-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish]",
	Short: "Generate shell completion script",
	Long: `Generate shell completion script for ws.

# Zsh: add to ~/.zshrc
eval "$(ws completion zsh)"

# Bash: add to ~/.bashrc
eval "$(ws completion bash)"

# Fish: add to ~/.config/fish/config.fish
ws completion fish | source`,
	// ValidArgs alone only feeds shell completion; OnlyValidArgs is what
	// rejects a shell outside it.
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	ValidArgs: []string{"bash", "zsh", "fish"},
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		// A failed write — a full disk under `> file` — is returned. A closed
		// pipe is not seen here: ws does not handle SIGPIPE, so a write to a
		// broken pipe on stdout ends the process quietly.
		out := cmd.OutOrStdout()
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletion(out)
		case "zsh":
			return rootCmd.GenZshCompletion(out)
		case "fish":
			return rootCmd.GenFishCompletion(out, true)
		}
		return nil
	},
}

// workspaceNames returns all workspace names for completion.
func workspaceNames(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	cfg := config.Load()
	workspaces, err := workspace.List(cfg)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		names = append(names, ws.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// profileNames returns all profile names for completion.
func profileNames(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	cfg := config.Load()
	profiles, err := profile.List(cfg)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, p.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	rootCmd.AddCommand(completionCmd)

	// Dynamic completions for workspace commands.
	startCmd.ValidArgsFunction = workspaceNames
	stopCmd.ValidArgsFunction = workspaceNames
	restartCmd.ValidArgsFunction = workspaceNames
	deleteCmd.ValidArgsFunction = workspaceNames
	sshCmd.ValidArgsFunction = workspaceNames
	codeCmd.ValidArgsFunction = workspaceNames
	logsCmd.ValidArgsFunction = workspaceNames

	// ws new <name> <profile> — second arg is profile name.
	newCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 1 {
			return profileNames(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

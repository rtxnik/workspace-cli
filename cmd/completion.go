package cmd

import (
	"os"

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
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"bash", "zsh", "fish"},
	Run: func(cmd *cobra.Command, args []string) {
		switch args[0] {
		case "bash":
			_ = rootCmd.GenBashCompletion(os.Stdout)
		case "zsh":
			_ = rootCmd.GenZshCompletion(os.Stdout)
		case "fish":
			_ = rootCmd.GenFishCompletion(os.Stdout, true)
		}
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

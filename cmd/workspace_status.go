package cmd

import (
	"os"
	"path/filepath"

	"github.com/rtxnik/workspace-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var probeRepoFn = workspace.ProbeRepo

func repoList() []string {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, "projects")
	return []string{
		filepath.Join(base, "workspace-cli"),
		filepath.Join(base, "vault-ai"),
		filepath.Join(base, "dotfiles"),
		filepath.Join(base, "loop-workflow-runbook"),
	}
}

func newWorkspaceStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Show workspace health across all repositories",
		Annotations:   wsAnnotation,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Output as JSON")
	return cmd
}

func init() {
	rootCmd.AddCommand(newWorkspaceStatusCmd())
}

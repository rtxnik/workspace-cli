package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rtxnik/workspace-cli/internal/output"
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
	}
}

func runWorkspaceStatus() []workspace.RepoStatus {
	paths := repoList()
	statuses := make([]workspace.RepoStatus, 0, len(paths))
	for _, p := range paths {
		statuses = append(statuses, probeRepoFn(p))
	}
	return statuses
}

func isRepoHealthy(s workspace.RepoStatus) bool {
	return s.Exists && s.Clean && s.Branch == "main" && s.Error == ""
}

func renderWorkspaceStatus(out io.Writer, statuses []workspace.RepoStatus, jsonMode bool) error {
	if jsonMode {
		return output.WriteJSON(out, statuses)
	}

	healthy := 0
	rows := make([][]string, 0, len(statuses))

	for _, s := range statuses {
		if !s.Exists {
			rows = append(rows, []string{
				s.Name,
				output.StyleError.Render("–"),
				output.StyleError.Render("missing"),
				"",
			})
			continue
		}

		if s.Error != "" {
			rows = append(rows, []string{
				s.Name,
				output.StyleWarning.Render("–"),
				output.StyleWarning.Render("error"),
				output.StyleDim.Render(s.Error),
			})
			continue
		}

		branchStr := output.StyleSuccess.Render(s.Branch)
		if s.Branch != "main" {
			branchStr = output.StyleWarning.Render(s.Branch)
		}

		cleanStr := output.StyleSuccess.Render("clean")
		if !s.Clean {
			cleanStr = output.StyleWarning.Render("dirty")
		}

		var syncStr string
		switch {
		case s.NoRemote:
			syncStr = output.StyleDim.Render("no remote")
		case s.Ahead == 0 && s.Behind == 0:
			syncStr = output.StyleDim.Render("±0")
		default:
			syncStr = output.StyleWarning.Render(fmt.Sprintf("+%d/-%d", s.Ahead, s.Behind))
		}

		if isRepoHealthy(s) {
			healthy++
		}

		rows = append(rows, []string{s.Name, branchStr, cleanStr, syncStr})
	}

	t := output.NewTable([]string{"REPO", "BRANCH", "STATUS", "SYNC"}).Rows(rows...)
	if _, err := fmt.Fprintln(out, t); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%s\n",
		output.StyleDim.Render(fmt.Sprintf("  %d/%d repos healthy", healthy, len(statuses))))
	return nil
}

func newWorkspaceStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Show workspace health across all repositories",
		Annotations:   wsAnnotation,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			statuses := runWorkspaceStatus()

			jsonFlag, _ := cmd.Flags().GetBool("json")
			if err := renderWorkspaceStatus(cmd.OutOrStdout(), statuses, jsonFlag); err != nil {
				return fmt.Errorf("workspace status: render: %w", err)
			}

			for _, s := range statuses {
				if !isRepoHealthy(s) {
					return &cliErrorWithExit{code: 1, msg: ""}
				}
			}
			return nil
		},
	}
	return cmd
}

func init() {
	rootCmd.AddCommand(newWorkspaceStatusCmd())
}

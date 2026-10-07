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
	t, err := statusTable(statuses)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, t.Render(output.Out()))
	return err
}

// statusTable is ws status's table, REPO, BRANCH, STATUS and SYNC, with its
// caption. A branch other than main and a sync that is not zero are painted
// as warnings, and a probe's error as muted text; the words carry the same.
func statusTable(statuses []workspace.RepoStatus) (output.Table, error) {
	t, err := output.NewTableBlock([]output.Col{
		{Title: "REPO", Prio: 1, Min: 8, Trunc: output.TruncMid},
		{Title: "BRANCH", Prio: 2, Min: 6, Trunc: output.TruncMid},
		{Title: "STATUS", Prio: 1, Min: 7, Atomic: true, Kind: output.ColState},
		{Title: "SYNC", Prio: 3, Min: 6, Trunc: output.TruncTail},
	}, nil)
	if err != nil {
		return output.Table{}, err
	}
	healthy := 0
	for _, s := range statuses {
		switch {
		case !s.Exists:
			t.Rows = append(t.Rows, []output.Cell{output.Text(s.Name), output.Text(""),
				output.Mark(output.StateFail, "missing"), output.Text("")})
			continue
		case s.Error != "":
			t.Rows = append(t.Rows, []output.Cell{output.Text(s.Name), output.Text(""),
				output.Mark(output.StateFail, "error"), {Text: s.Error, Role: output.RoleMuted}})
			continue
		}
		branch := output.Text(s.Branch)
		if s.Branch != "main" {
			branch.Role = output.RoleWarn
		}
		state := output.Mark(output.StateOK, "clean")
		if !s.Clean {
			state = output.Mark(output.StateBusy, "dirty")
		}
		sync := output.Text("in sync")
		switch {
		case s.NoRemote:
			sync = output.Text("no remote")
		case s.Ahead != 0 || s.Behind != 0:
			sync = output.Cell{Text: fmt.Sprintf("+%d -%d", s.Ahead, s.Behind), Role: output.RoleWarn}
		}
		if isRepoHealthy(s) {
			healthy++
		}
		t.Rows = append(t.Rows, []output.Cell{output.Text(s.Name), branch, state, sync})
	}
	t.Caption = fmt.Sprintf("%d/%d repos healthy", healthy, len(statuses))
	return t, nil
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

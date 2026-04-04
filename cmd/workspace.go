package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/detect"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var wsAnnotation = map[string]string{"group": "workspace"}

var newCmd = &cobra.Command{
	Use:         "new <name> [profile]",
	Short:       "Create a new workspace",
	Args:        cobra.RangeArgs(1, 2),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name := args[0]

		if err := workspace.ValidateName(name); err != nil {
			output.Die(err.Error())
		}

		if workspace.Exists(cfg, name) {
			fmt.Fprintln(os.Stderr, output.RenderError(output.ErrorDetail{
				Title:       fmt.Sprintf("Workspace %q already exists", name),
				Suggestions: []string{"Choose a different name", fmt.Sprintf("Delete existing: ws delete %s", name)},
			}))
			os.Exit(1)
		}

		var profile string
		if len(args) >= 2 {
			profile = args[1]
		} else {
			// Try auto-detection from current directory.
			cwd, _ := os.Getwd()
			profile = detect.Profile(cwd)
			if profile == "" {
				profile = "default"
			}
			output.Info(fmt.Sprintf("Detected profile: %s", profile))
		}

		withProxy, _ := cmd.Flags().GetBool("proxy")

		if err := workspace.Create(cfg, name, profile, withProxy); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Workspace %q created with profile %q", name, profile))
	},
}

var listCmd = &cobra.Command{
	Use:         "list",
	Aliases:     []string{"ls"},
	Short:       "List workspaces",
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		workspaces, err := workspace.List(cfg)
		if err != nil {
			output.Die(err.Error())
		}
		if len(workspaces) == 0 {
			output.Info("No workspaces found")
			return
		}

		jsonFlag, _ := cmd.Flags().GetBool("json")
		if jsonFlag {
			type wsJSON struct {
				Name    string `json:"name"`
				Status  string `json:"status"`
				Profile string `json:"profile"`
				Proxy   bool   `json:"proxy"`
			}
			items := make([]wsJSON, 0, len(workspaces))
			for _, ws := range workspaces {
				items = append(items, wsJSON{
					Name:    ws.Name,
					Status:  strings.ToLower(ws.Status),
					Profile: ws.Profile,
					Proxy:   ws.Proxy,
				})
			}
			output.JSON(items)
			return
		}

		termWidth := 100
		if w, _, err := term.GetSize(0); err == nil {
			termWidth = w
		}
		narrow := termWidth < 80

		running := 0
		rows := make([][]string, 0, len(workspaces))
		for _, ws := range workspaces {
			st := strings.ToLower(ws.Status)
			if st == "running" {
				running++
			}
			proxy := output.StyleDim.Render("–")
			if ws.Proxy {
				proxy = output.StyleAccent.Render("⚡")
			}
			if narrow {
				rows = append(rows, []string{ws.Name, output.StatusIcon(st), proxy})
			} else {
				rows = append(rows, []string{ws.Name, output.StatusText(st), ws.Profile, proxy})
			}
		}

		var t fmt.Stringer
		if narrow {
			t = output.NewTable([]string{"NAME", "STATUS", "PROXY"}).Rows(rows...)
		} else {
			t = output.NewTable([]string{"NAME", "STATUS", "PROFILE", "PROXY"}).Rows(rows...)
		}

		fmt.Println(t)
		fmt.Fprintf(os.Stderr, "\n%s\n",
			output.StyleDim.Render(fmt.Sprintf("  %d workspace(s), %d running", len(workspaces), running)))
	},
}

var detectCmd = &cobra.Command{
	Use:         "detect [path]",
	Short:       "Detect project profile",
	Args:        cobra.MaximumNArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		dir := "."
		if len(args) > 0 {
			dir = args[0]
		}
		profile := detect.Profile(dir)
		if profile == "" {
			output.Info("No profile detected")
			return
		}
		output.Success(fmt.Sprintf("Detected profile: %s", profile))
	},
}

var startCmd = &cobra.Command{
	Use:         "start <name>",
	Short:       "Start a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name := args[0]
		if !workspace.Exists(cfg, name) {
			fmt.Fprintln(os.Stderr, output.RenderError(output.ErrorDetail{
				Title:       fmt.Sprintf("Workspace %q not found", name),
				Suggestions: []string{"List workspaces: ws list", fmt.Sprintf("Create it: ws new %s", name)},
			}))
			os.Exit(1)
		}
		source := filepath.Join(cfg.WorkspacesDir, name)
		runner := output.NewStepRunner(
			output.Step{Name: "Checking workspace", Fn: func() error {
				if !workspace.Exists(cfg, name) {
					return fmt.Errorf("workspace dir missing")
				}
				return nil
			}},
			output.Step{Name: "Starting container", Fn: func() error {
				return workspace.DevpodUp(source)
			}},
		)
		if err := runner.Run(); err != nil {
			output.Die(err.Error())
		}
	},
}

var stopCmd = &cobra.Command{
	Use:         "stop <name>",
	Short:       "Stop a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		if err := output.RunWithSpinner(fmt.Sprintf("Stopping workspace %q", name), func() error {
			return workspace.DevpodStop(name)
		}); err != nil {
			output.Die(err.Error())
		}
	},
}

var deleteCmd = &cobra.Command{
	Use:         "delete <name>",
	Aliases:     []string{"rm"},
	Short:       "Delete a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name := args[0]

		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if !output.Confirm(fmt.Sprintf("Delete workspace %q?", name), "This will remove the workspace and its local files.") {
				output.Info("Aborted")
				return
			}
		}

		if err := output.RunWithSpinner(fmt.Sprintf("Deleting workspace %q", name), func() error {
			if err := workspace.DevpodDelete(name); err != nil {
				output.Warn(fmt.Sprintf("devpod delete: %s", err))
			}
			wsDir := filepath.Join(cfg.WorkspacesDir, name)
			return os.RemoveAll(wsDir)
		}); err != nil {
			output.Die(err.Error())
		}
	},
}

var sshCmd = &cobra.Command{
	Use:         "ssh [name]",
	Short:       "SSH into a workspace",
	Args:        cobra.MaximumNArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		var name string
		if len(args) > 0 {
			name = args[0]
		} else {
			name = selectWorkspace()
		}
		// Rename tmux window if inside tmux.
		if tmux := os.Getenv("TMUX"); tmux != "" {
			_ = exec.Command("tmux", "rename-window", name).Run()
		}
		if err := workspace.DevpodSSH(name); err != nil {
			output.Die(err.Error())
		}
	},
}

var codeCmd = &cobra.Command{
	Use:         "code [name]",
	Short:       "Open workspace in VS Code",
	Args:        cobra.MaximumNArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		var name string
		if len(args) > 0 {
			name = args[0]
		} else {
			name = selectWorkspace()
		}
		output.Info(fmt.Sprintf("Opening workspace %q in VS Code...", name))
		if err := workspace.DevpodCode(name); err != nil {
			output.Die(err.Error())
		}
	},
}

var restartCmd = &cobra.Command{
	Use:         "restart <name>",
	Short:       "Restart a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name := args[0]
		source := filepath.Join(cfg.WorkspacesDir, name)

		steps := []output.Step{
			{Name: "Starting container", Fn: func() error {
				return workspace.DevpodUp(source)
			}},
		}

		// Only stop if workspace is currently running.
		workspaces, _ := workspace.List(cfg)
		for _, ws := range workspaces {
			if ws.Name == name && strings.EqualFold(ws.Status, "running") {
				steps = append([]output.Step{
					{Name: "Stopping workspace", Fn: func() error {
						return workspace.DevpodStop(name)
					}},
				}, steps...)
				break
			}
		}

		if err := output.NewStepRunner(steps...).Run(); err != nil {
			output.Die(err.Error())
		}
	},
}

var logsCmd = &cobra.Command{
	Use:         "logs <name>",
	Short:       "Show workspace logs",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		follow, _ := cmd.Flags().GetBool("follow")

		// Try journalctl inside the workspace first.
		journalArgs := []string{"ssh", name, "--", "journalctl", "--user", "-n", "50", "--no-pager"}
		if follow {
			journalArgs = append(journalArgs, "-f")
		}
		c := exec.Command("devpod", journalArgs...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			// Fall back to devpod logs.
			if err := workspace.DevpodLogs(name); err != nil {
				output.Die(err.Error())
			}
		}
	},
}

// selectWorkspace shows an interactive selector of workspaces and returns
// the selected name. Exits if no workspaces exist or user cancels.
func selectWorkspace() string {
	cfg := config.Load()
	workspaces, err := workspace.List(cfg)
	if err != nil {
		output.Die(err.Error())
	}
	if len(workspaces) == 0 {
		output.Die("no workspaces found")
	}

	opts := make([]output.SelectOption, 0, len(workspaces))
	for _, ws := range workspaces {
		label := output.StatusLabel(ws.Name, strings.ToLower(ws.Status))
		opts = append(opts, output.SelectOption{Label: label, Value: ws.Name})
	}

	return output.Select("Select workspace:", opts)
}

func init() {
	newCmd.Flags().Bool("proxy", false, "Enable proxy networking")
	deleteCmd.Flags().BoolP("force", "f", false, "Skip delete confirmation")
	logsCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	rootCmd.AddCommand(newCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(detectCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(codeCmd)
	rootCmd.AddCommand(logsCmd)
}

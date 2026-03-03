package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/rtxnik/ws/internal/config"
	"github.com/rtxnik/ws/internal/detect"
	"github.com/rtxnik/ws/internal/output"
	"github.com/rtxnik/ws/internal/workspace"
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

		if workspace.Exists(cfg, name) {
			output.Die(fmt.Sprintf("workspace %q already exists", name))
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

		headerStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
		cellStyle := lipgloss.NewStyle().Padding(0, 1)

		rows := make([][]string, 0, len(workspaces))
		for _, ws := range workspaces {
			status := formatStatus(ws.Status)
			rows = append(rows, []string{ws.Name, status, ws.Profile})
		}

		t := table.New().
			Headers("NAME", "STATUS", "PROFILE").
			Rows(rows...).
			StyleFunc(func(row, col int) lipgloss.Style {
				if row == table.HeaderRow {
					return headerStyle
				}
				return cellStyle
			})

		fmt.Println(t)
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
			output.Die(fmt.Sprintf("workspace %q not found", name))
		}
		source := filepath.Join(cfg.WorkspacesDir, name)
		output.Info(fmt.Sprintf("Starting workspace %q...", name))
		if err := workspace.DevpodUp(source); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Workspace %q started", name))
	},
}

var stopCmd = &cobra.Command{
	Use:         "stop <name>",
	Short:       "Stop a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		output.Info(fmt.Sprintf("Stopping workspace %q...", name))
		if err := workspace.DevpodStop(name); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Workspace %q stopped", name))
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
		output.Info(fmt.Sprintf("Deleting workspace %q...", name))
		if err := workspace.DevpodDelete(name); err != nil {
			output.Warn(fmt.Sprintf("devpod delete: %s", err))
		}
		wsDir := filepath.Join(cfg.WorkspacesDir, name)
		if err := os.RemoveAll(wsDir); err != nil {
			output.Die(fmt.Sprintf("remove directory: %s", err))
		}
		output.Success(fmt.Sprintf("Workspace %q deleted", name))
	},
}

var sshCmd = &cobra.Command{
	Use:         "ssh <name>",
	Short:       "SSH into a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
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
	Use:         "code <name>",
	Short:       "Open workspace in VS Code",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		output.Info(fmt.Sprintf("Opening workspace %q in VS Code...", name))
		if err := workspace.DevpodCode(name); err != nil {
			output.Die(err.Error())
		}
	},
}

func formatStatus(s string) string {
	s = strings.ToLower(s)
	switch {
	case s == "running":
		return "● Running"
	case s == "stopped":
		return "○ Stopped"
	case s == "busy":
		return "◉ Busy"
	default:
		return "○ " + s
	}
}

func init() {
	newCmd.Flags().Bool("proxy", false, "Enable proxy networking")
	rootCmd.AddCommand(newCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(detectCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(codeCmd)
}

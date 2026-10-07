package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/detect"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/procx"
	"github.com/rtxnik/workspace-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var wsAnnotation = map[string]string{"group": "workspace"}

// confirmDestructiveFn gates every destructive delete; a test seam so
// confirmation outcomes are driven deterministically without a terminal.
var confirmDestructiveFn = output.ConfirmDestructive

var newCmd = &cobra.Command{
	Use:         "new <name> [profile]",
	Short:       "Create a new workspace",
	Args:        cobra.RangeArgs(1, 2),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		name := args[0]

		if err := workspace.ValidateName(name); err != nil {
			return err
		}

		if workspace.Exists(cfg, name) {
			return workspaceExists(name)
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
			return err
		}
		output.Success(fmt.Sprintf("Workspace %q created with profile %q", name, profile))
		return nil
	},
}

var listCmd = &cobra.Command{
	Use:         "list",
	Aliases:     []string{"ls"},
	Short:       "List workspaces",
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		workspaces, err := workspace.List(cfg)
		if err != nil {
			return err
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
			return output.WriteJSON(cmd.OutOrStdout(), items)
		}

		s := output.Out()
		if len(workspaces) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), output.Empty{Subject: "workspaces", Steps: []output.Remedy{
				{Label: "Create one", Cmd: "ws new <name>"},
				{Label: "See profiles", Cmd: "ws profiles"},
			}}.Render(s))
			return err
		}
		t, err := listTable(workspaces)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), t.Render(s))
		return err
	},
}

// listTable is ws list's table, NAME, STATUS, PROFILE and PROXY, with its
// caption. The allocator lays it out for the stream it is rendered on.
func listTable(workspaces []workspace.Info) (output.Table, error) {
	t, err := output.NewTableBlock([]output.Col{
		{Title: "NAME", Prio: 1, Min: 8, Trunc: output.TruncMid},
		{Title: "STATUS", Prio: 1, Min: 6, Atomic: true, Kind: output.ColState},
		{Title: "PROFILE", Prio: 3, Min: 6, Trunc: output.TruncTail},
		{Title: "PROXY", Prio: 2, Min: 3, Atomic: true},
	}, nil)
	if err != nil {
		return output.Table{}, err
	}
	running := 0
	for _, ws := range workspaces {
		st, word := workspaceState(ws.Status)
		if st == output.StateOK {
			running++
		}
		proxy := "off"
		if ws.Proxy {
			proxy = "on"
		}
		t.Rows = append(t.Rows, []output.Cell{output.Text(ws.Name), output.Mark(st, word), output.Text(ws.Profile), output.Text(proxy)})
	}
	t.Caption = fmt.Sprintf("%s, %d running", countOf(len(workspaces), "workspace", "workspaces"), running)
	return t, nil
}

// workspaceState is a workspace's state and its word, from devpod's status
// lower-cased (§4.5). Anything devpod reports that is not below is shown as
// an unknown state with devpod's own word.
func workspaceState(status string) (output.State, string) {
	switch status = strings.ToLower(status); status {
	case "running":
		return output.StateOK, "running"
	case "stopped":
		return output.StateIdle, "stopped"
	case "notcreated", "":
		return output.StateIdle, "not created"
	case "busy", "starting":
		return output.StateBusy, status
	case "notfound":
		return output.StateIdle, "not found"
	default:
		return output.StateUnknown, status
	}
}

// workspaceNotFound is the error of a command on a workspace that is not
// there, carrying its Problem: list the workspaces, or take next, the step
// that fits the command.
func workspaceNotFound(name string, next output.Remedy) error {
	return &output.ProblemError{P: output.Problem{
		Title: fmt.Sprintf("Workspace %q not found", name),
		Steps: []output.Remedy{{Label: "List workspaces", Cmd: "ws list"}, next},
	}}
}

// workspaceExists is the error of ws new for a name already taken, carrying
// its Problem.
func workspaceExists(name string) error {
	return &output.ProblemError{P: output.Problem{
		Title: fmt.Sprintf("Workspace %q already exists", name),
		Steps: []output.Remedy{{Label: "Delete it", Cmd: "ws delete " + name}, {Label: "List workspaces", Cmd: "ws list"}},
	}}
}

// countOf is "1 workspace" or "5 workspaces".
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

var detectCmd = &cobra.Command{
	Use:         "detect [path]",
	Short:       "Detect project profile",
	Args:        cobra.MaximumNArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		dir := "."
		if len(args) > 0 {
			dir = args[0]
		}
		profile := detect.Profile(dir)
		if profile == "" {
			output.Info("No profile detected")
			return nil
		}
		output.Success(fmt.Sprintf("Detected profile: %s", profile))
		return nil
	},
}

var startCmd = &cobra.Command{
	Use:         "start <name>",
	Short:       "Start a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		name := args[0]
		if err := workspace.ValidateName(name); err != nil {
			return err
		}
		if !workspace.Exists(cfg, name) {
			return workspaceNotFound(name, output.Remedy{Label: "Create it", Cmd: "ws new " + name})
		}
		source := filepath.Join(cfg.WorkspacesDir, name)
		return output.Run(
			output.Task{Title: "Checking workspace", Run: func(*output.Log) error {
				if !workspace.Exists(cfg, name) {
					return fmt.Errorf("workspace dir missing")
				}
				return nil
			}},
			output.Task{Title: "Starting container", Run: func(log *output.Log) error {
				return workspace.DevpodUp(source, log)
			}},
		)
	},
}

var stopCmd = &cobra.Command{
	Use:         "stop <name>",
	Short:       "Stop a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		name := args[0]
		if err := workspace.ValidateName(name); err != nil {
			return err
		}
		return output.Run(output.Task{
			Title: fmt.Sprintf("Stopping workspace %q", name),
			Run:   func(log *output.Log) error { return workspace.DevpodStop(name, log) },
		})
	},
}

var deleteCmd = &cobra.Command{
	Use:         "delete <name>",
	Aliases:     []string{"rm"},
	Short:       "Delete a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		name := args[0]

		// Errors are returned as they are: wrapped in a *cliErrorWithExit,
		// which has no Unwrap, a failed step would hide from the root.
		if err := workspace.ValidateName(name); err != nil {
			return err
		}

		// A workspace that is not there is refused before the confirmation,
		// with or without --force. The devpod step is offered for every
		// missing name: whether devpod still knows the workspace would take
		// a devpod call of up to 10s on the refusal path, and devpod refuses
		// a name it does not know.
		if !workspace.Exists(cfg, name) {
			return workspaceNotFound(name, output.Remedy{Label: "Remove it from devpod", Cmd: "devpod delete " + name})
		}

		force, _ := cmd.Flags().GetBool("force")
		if !confirmDestructiveFn(force,
			fmt.Sprintf("Delete workspace %q?", name),
			"This will remove the workspace and its local files.") {
			output.Info("Aborted")
			return nil
		}

		// devpod failing to delete its workspace is a warning, and the
		// directory goes all the same; the warning carries devpod's last
		// lines, which no longer reach the terminal. A directory that cannot
		// be removed is a Problem of its own: the task's log holds devpod's
		// lines, which are not its cause.
		dir := filepath.Join(cfg.WorkspacesDir, name)
		return output.Run(output.Task{
			Title: fmt.Sprintf("Deleting workspace %q", name),
			Run: func(log *output.Log) error {
				if err := workspace.DevpodDelete(name, log); err != nil {
					output.Warn(err.Error())
					for _, line := range log.Tail() {
						output.Detail(line)
					}
				}
				if err := os.RemoveAll(dir); err != nil {
					return &output.ProblemError{P: output.Problem{
						Title: fmt.Sprintf("The directory of workspace %q could not be removed", name),
						Cause: err.Error(),
						Facts: []output.Fact{{K: "Directory", V: dir}},
					}, Err: err}
				}
				return nil
			},
		})
	},
}

var sshCmd = &cobra.Command{
	Use:         "ssh [name]",
	Short:       "SSH into a workspace",
	Args:        cobra.MaximumNArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		var name string
		if len(args) > 0 {
			name = args[0]
		} else {
			picked, ok, err := selectWorkspace()
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			name = picked
		}
		if err := workspace.ValidateName(name); err != nil {
			return err
		}
		// Rename tmux window if inside tmux. Bounded: a wedged tmux server
		// must not stall the ssh command (best-effort, error ignored).
		if tmux := os.Getenv("TMUX"); tmux != "" {
			_, _ = procx.Run(context.Background(), 5*time.Second, "tmux", "rename-window", name)
		}
		if err := workspace.DevpodSSH(name); err != nil {
			return err
		}
		return nil
	},
}

var codeCmd = &cobra.Command{
	Use:         "code [name]",
	Short:       "Open workspace in VS Code",
	Args:        cobra.MaximumNArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		var name string
		if len(args) > 0 {
			name = args[0]
		} else {
			picked, ok, err := selectWorkspace()
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			name = picked
		}
		if err := workspace.ValidateName(name); err != nil {
			return err
		}
		output.Info(fmt.Sprintf("Opening workspace %q in VS Code...", name))
		if err := workspace.DevpodCode(name); err != nil {
			return err
		}
		return nil
	},
}

var restartCmd = &cobra.Command{
	Use:         "restart <name>",
	Short:       "Restart a workspace",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		name := args[0]
		if err := workspace.ValidateName(name); err != nil {
			return err
		}
		source := filepath.Join(cfg.WorkspacesDir, name)

		tasks := []output.Task{
			{Title: "Starting container", Run: func(log *output.Log) error {
				return workspace.DevpodUp(source, log)
			}},
		}

		// Only stop if workspace is currently running.
		workspaces, _ := workspace.List(cfg)
		for _, ws := range workspaces {
			if ws.Name == name && strings.EqualFold(ws.Status, "running") {
				tasks = append([]output.Task{
					{Title: "Stopping workspace", Run: func(log *output.Log) error {
						return workspace.DevpodStop(name, log)
					}},
				}, tasks...)
				break
			}
		}

		return output.Run(tasks...)
	},
}

var logsCmd = &cobra.Command{
	Use:         "logs <name>",
	Short:       "Show workspace logs",
	Args:        cobra.ExactArgs(1),
	Annotations: wsAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		name := args[0]
		if err := workspace.ValidateName(name); err != nil {
			return err
		}
		follow, _ := cmd.Flags().GetBool("follow")

		// Try journalctl inside the workspace first.
		journalArgs := []string{"ssh", name, "--", "journalctl", "--user", "-n", "50", "--no-pager"}
		var c *exec.Cmd
		if follow {
			journalArgs = append(journalArgs, "-f")
			// Live `journalctl -f` stream: intentionally unbounded (a
			// deadline would truncate the follow session mid-stream).
			c = exec.Command("devpod", journalArgs...)
		} else {
			// One-shot journal read: bounded so a wedged ssh transport to an
			// unresponsive workspace cannot hang the command forever.
			jctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c = exec.CommandContext(jctx, "devpod", journalArgs...)
		}
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			// Fall back to devpod logs.
			if err := workspace.DevpodLogs(name); err != nil {
				return err
			}
		}
		return nil
	},
}

// selectWorkspace shows an interactive selector of workspaces and returns the
// selected name. ok is false when the selector produced no choice — the
// operator cancelled it, or it could not run (see output.Select); the
// caller returns nil and the process exits 0.
func selectWorkspace() (string, bool, error) {
	cfg := config.Load()
	workspaces, err := workspace.List(cfg)
	if err != nil {
		return "", false, err
	}
	if len(workspaces) == 0 {
		return "", false, errors.New("no workspaces found")
	}

	// huh draws its forms on stderr, so the labels are rendered for it.
	return output.Select("Select workspace:", workspaceOptions(output.Err(), workspaces))
}

// workspaceOptions labels each workspace "<name>  <mark> <word>" for s, with
// ws list's state vocabulary.
func workspaceOptions(s *output.Stream, workspaces []workspace.Info) []output.SelectOption {
	opts := make([]output.SelectOption, 0, len(workspaces))
	for _, ws := range workspaces {
		opts = append(opts, output.SelectOption{Label: ws.Name + "  " + s.StateText(workspaceState(ws.Status)), Value: ws.Name})
	}
	return opts
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

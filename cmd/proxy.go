package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rtxnik/ws/internal/config"
	"github.com/rtxnik/ws/internal/docker"
	"github.com/rtxnik/ws/internal/output"
	"github.com/rtxnik/ws/internal/vless"
	"github.com/spf13/cobra"
)

var proxyAnnotation = map[string]string{"group": "proxy"}

var proxyCmd = &cobra.Command{
	Use:         "proxy",
	Short:       "Proxy management commands",
	Annotations: proxyAnnotation,
}

var proxyUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the proxy container",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if err := output.RunWithSpinner("Starting proxy", func() error {
			return docker.ProxyUp(cfg)
		}); err != nil {
			fmt.Fprintln(os.Stderr, output.RenderError(output.ErrorDetail{
				Title:       "Failed to start proxy",
				Context:     map[string]string{"Error": err.Error()},
				Suggestions: []string{"Check config: ws proxy check", "Initialize config: ws proxy init <vless-uri>", "Rebuild image: ws proxy rebuild"},
			}))
			os.Exit(1)
		}
	},
}

var proxyDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop the proxy container",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			warnProxyConnected(cfg)
		}

		if err := output.RunWithSpinner("Stopping proxy", func() error {
			return docker.ProxyDown(cfg)
		}); err != nil {
			output.Die(err.Error())
		}
	},
}

var proxyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show proxy container status",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		st, err := docker.ProxyStatus(cfg)
		if err != nil {
			output.Die(err.Error())
		}

		stateStatus := "stopped"
		if st.Running {
			stateStatus = "running"
		}

		var lines []string
		lines = append(lines, output.StyleSection.Render(output.StatusText(stateStatus)))
		if st.Health != "" {
			lines = append(lines, fmt.Sprintf("Health:  %s", output.StatusText(st.Health)))
		}
		if st.Uptime != "" {
			lines = append(lines, fmt.Sprintf("Uptime:  %s", st.Uptime))
		}
		if st.Image != "" {
			lines = append(lines, fmt.Sprintf("Image:   %s", st.Image))
		}

		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(output.Blue).
			Padding(0, 1).
			Render(strings.Join(lines, "\n"))

		fmt.Println(box)
	},
}

var proxyCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify proxy prerequisites",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		results := docker.ProxyCheck(cfg)

		passed := 0
		for _, r := range results {
			if r.Passed {
				fmt.Printf("  %s %s\n", output.StyleSuccess.Render("✓"), r.Name)
				passed++
			} else {
				fmt.Printf("  %s %s\n", output.StyleError.Render("✗"), r.Name)
			}
		}

		fmt.Println()
		total := len(results)
		if passed == total {
			output.Success(fmt.Sprintf("%d/%d checks passed", passed, total))
		} else {
			output.Warn(fmt.Sprintf("%d/%d checks passed", passed, total))
		}
	},
}

var proxyLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show proxy container logs",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		logs, err := docker.ProxyLogs(cfg, 50)
		if err != nil {
			output.Die(err.Error())
		}
		fmt.Print(logs)
	},
}

var proxyRebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Rebuild proxy image from scratch",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			warnProxyConnected(cfg)
		}

		runner := output.NewStepRunner(
			output.Step{Name: "Building proxy image", Fn: func() error {
				return docker.BuildProxyImage(cfg, "")
			}},
			output.Step{Name: "Recreating container", Fn: func() error {
				st, _ := docker.ProxyStatus(cfg)
				if st.Running {
					return docker.ProxyRecreate(cfg)
				}
				return nil
			}},
			output.Step{Name: "Cleaning old images", Fn: func() error {
				return exec.Command("docker", "image", "prune", "-f").Run()
			}},
		)
		if err := runner.Run(); err != nil {
			output.Die(err.Error())
		}
	},
}

var proxyTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Test proxy connectivity",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		st, err := docker.ProxyStatus(cfg)
		if err != nil || !st.Running {
			output.Die("Proxy is not running")
		}

		output.Info("Testing proxy connectivity...")
		output.Detail(fmt.Sprintf("Uptime: %s", st.Uptime))
		if st.Health != "" {
			output.Detail(fmt.Sprintf("Health: %s", st.Health))
		}

		if st.Health == "healthy" {
			output.Success("Proxy is healthy")
		} else {
			output.Warn("Proxy health check not passing")
		}
	},
}

var proxyDebugCmd = &cobra.Command{
	Use:   "debug <on|off>",
	Short: "Toggle debug logging",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		mode := args[0]

		var level string
		switch mode {
		case "on":
			level = "debug"
		case "off":
			level = "warning"
		default:
			output.Die("usage: ws proxy debug <on|off>")
		}

		if err := setXrayLogLevel(cfg.XrayConfig, level); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Log level set to %q", level))

		// Restart proxy if running.
		st, _ := docker.ProxyStatus(cfg)
		if st.Running {
			output.Info("Restarting proxy...")
			if err := docker.ProxyRestart(cfg); err != nil {
				output.Die(err.Error())
			}
			output.Success("Proxy restarted")
		}
	},
}

var proxyUpdateCmd = &cobra.Command{
	Use:   "update [version]",
	Short: "Update xray-core version",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()

		version := ""
		if len(args) > 0 {
			version = args[0]
		} else {
			output.Info("Fetching latest xray-core version...")
			v, err := fetchLatestXrayVersion()
			if err != nil {
				output.Die(err.Error())
			}
			version = v
			output.Detail(fmt.Sprintf("Latest: %s", version))
		}

		if err := output.RunWithSpinner(fmt.Sprintf("Building proxy image with xray-core %s", version), func() error {
			return docker.BuildProxyImage(cfg, version)
		}); err != nil {
			output.Die(err.Error())
		}

		// Recreate proxy container to use the new image.
		output.Info("Restarting proxy...")
		if err := docker.ProxyRecreate(cfg); err != nil {
			output.Warn(err.Error())
		} else {
			output.Success("Proxy restarted with new version")
		}
	},
}

var proxyInitCmd = &cobra.Command{
	Use:   "init <vless-uri>",
	Short: "Generate xray config from VLESS URI",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		uri := args[0]

		parsed, err := vless.Parse(uri)
		if err != nil {
			output.Die(err.Error())
		}

		add, _ := cmd.Flags().GetBool("add")

		if add {
			if err := vless.AddNode(cfg.XrayConfig, parsed); err != nil {
				output.Die(err.Error())
			}
			output.Success(fmt.Sprintf("Added node %q to config", parsed.Remark))
		} else {
			if err := vless.WriteNewConfig(cfg.XrayConfig, parsed); err != nil {
				output.Die(err.Error())
			}
			output.Success(fmt.Sprintf("Config written to %s", cfg.XrayConfig))
		}

		output.Detail(fmt.Sprintf("Transport: %s, Security: %s", parsed.Network, parsed.Security))
	},
}

// warnProxyConnected checks for workspaces sharing the proxy network
// and asks for confirmation before proceeding. Exits if user declines.
func warnProxyConnected(cfg config.Config) {
	names, err := docker.ProxyConnectedContainers(cfg)
	if err != nil || len(names) == 0 {
		return
	}

	desc := fmt.Sprintf("Active workspaces: %s\nThis will interrupt network for these workspaces.", strings.Join(names, ", "))
	if !output.Confirm("Continue?", desc) {
		output.Info("Aborted")
		os.Exit(0)
	}
}

func init() {
	proxyInitCmd.Flags().Bool("add", false, "Add node to existing config instead of creating new")
	proxyDownCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")
	proxyRebuildCmd.Flags().BoolP("force", "f", false, "Skip confirmation for connected workspaces")

	proxyCmd.AddCommand(proxyUpCmd)
	proxyCmd.AddCommand(proxyDownCmd)
	proxyCmd.AddCommand(proxyStatusCmd)
	proxyCmd.AddCommand(proxyCheckCmd)
	proxyCmd.AddCommand(proxyLogsCmd)
	proxyCmd.AddCommand(proxyRebuildCmd)
	proxyCmd.AddCommand(proxyTestCmd)
	proxyCmd.AddCommand(proxyDebugCmd)
	proxyCmd.AddCommand(proxyUpdateCmd)
	proxyCmd.AddCommand(proxyInitCmd)
	rootCmd.AddCommand(proxyCmd)
}

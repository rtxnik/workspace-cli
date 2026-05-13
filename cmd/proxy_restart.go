package cmd

import (
	"fmt"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/spf13/cobra"
)

// proxyRestartCmdFn is the production->test seam for `ws proxy restart`.
// Production wires to docker.ProxyRestart; tests override.
var proxyRestartCmdFn = docker.ProxyRestart

var proxyRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the proxy container (preserves container, re-reads config)",
	Long: `Stop and start the proxy container. The container itself is preserved
(same network, same volumes, same image) — only the xray process restarts
and re-reads its config from disk.

Use this after changing xray config, or as recovery if 'ws proxy profile use'
swapped the symlink but the auto-reload failed. For container-level changes
(image, env, network) use 'ws proxy recreate' instead.`,
	Annotations: proxyAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		if err := proxyRestartCmdFn(cfg); err != nil {
			cmd.SilenceUsage = true
			return fmt.Errorf("proxy restart failed: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Proxy restarted")
		return nil
	},
}

package cmd

import (
	"fmt"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/spf13/cobra"
)

// proxyRecreateCmdFn is the production->test seam for `ws proxy recreate`.
// Production wires to docker.ProxyRecreate; tests override.
var proxyRecreateCmdFn = docker.ProxyRecreate

var proxyRecreateCmd = &cobra.Command{
	Use:   "recreate",
	Short: "Recreate the proxy container (destroys + creates new container)",
	Long: `Remove the proxy container and create a new one on the same network
with the same IP. Use this after changing container-level settings (image,
env vars, network config, volumes). For config-file-only changes use
'ws proxy restart' which is faster and preserves the container.

Workspace containers on the ws-proxy bridge are unaffected — they keep
their own network namespace and resume connectivity when the new proxy
comes up.`,
	Annotations: proxyAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		if err := proxyRecreateCmdFn(cfg); err != nil {
			cmd.SilenceUsage = true
			return fmt.Errorf("proxy recreate failed: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Proxy recreated")
		return nil
	},
}

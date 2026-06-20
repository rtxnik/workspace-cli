package cmd

import (
	"fmt"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/xray"
	"github.com/spf13/cobra"
)

var proxyUpgradeConfigCmd = &cobra.Command{
	Use:   "upgrade-config",
	Short: "Upgrade on-disk xray profiles to the current canonical inbound (adds sockopt.tproxy)",
	Long: `Rewrites the Inbounds section of every *.json profile in the profiles directory
to match the canonical inbound produced by the current build (dokodemo-door with
streamSettings.sockopt.tproxy="tproxy"). Outbounds and Routing are preserved.

Profiles that already carry sockopt.tproxy are skipped (the command is idempotent).
Profiles with no recognisable proxy outbound are skipped with a warning.

After upgrading, recreate the proxy container to pick up the new config:

  ws proxy recreate`,
	Annotations: proxyAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		changed, err := xray.UpgradeProfileInbounds(cfg)
		if err != nil {
			cmd.SilenceUsage = true
			return fmt.Errorf("upgrade-config: %w", err)
		}
		switch changed {
		case 0:
			output.Success("All profiles are already up to date.")
		case 1:
			output.Success("Upgraded 1 profile.")
			output.Detail("Run 'ws proxy recreate' to apply the new config.")
		default:
			output.Success(fmt.Sprintf("Upgraded %d profiles.", changed))
			output.Detail("Run 'ws proxy recreate' to apply the new config.")
		}
		return nil
	},
}

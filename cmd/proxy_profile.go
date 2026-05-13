package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/xray"
	"github.com/spf13/cobra"
)

// Test seams for cmd-layer orchestration (D-10 partial-failure contract
// owner). Production wires to real implementations; tests override.
// These are INDEPENDENT of internal/xray's seams — both layers need their
// own seams because the TestManualRecoveryOnFailedSwitch tripwire (xray)
// and TestProfileUseRendersPartialFailureWithoutRollback tripwire (cmd)
// exercise different boundary conditions of the same D-10 contract.
var (
	verifyProxyReadyFn    = docker.VerifyProxyReadyForReload
	switchToFn            = xray.SwitchTo
	switchToSymlinkOnlyFn = xray.SwitchToSymlinkOnly
	proxyRestartFn        = docker.ProxyRestart
	loadConfigFn          = config.Load
)

// profileCmd is the depth-3 parent for `ws proxy profile *` per CONTEXT.md D-01.
// Plans 02-05 fill in the leaf Run bodies; this file ships only the registration
// scaffolding so all downstream plans land their commands in parallel without
// merge conflicts on this file.
//
// PersistentPreRunE (Plan 22-04 + D-07): every `ws proxy profile *` leaf
// invocation funnels through EnsureMigrated, which transparently migrates a
// legacy regular-file ~/.config/xray/config.json to the profiles/primary.json
// + symlink layout. When --no-migrate is set AND migration WOULD have
// triggered, EnsureMigrated errors out without mutating state (memory
// feedback_no_auto_state_mutation). Cobra short-circuits --help / completion
// before PersistentPreRunE, so help output remains available even on a host
// with no xray state.
var profileCmd = &cobra.Command{
	Use:         "profile",
	Short:       "Manage xray VLESS profiles",
	Long:        "Lifecycle management for named xray VLESS configurations.\nReplaces the legacy ad-hoc xray-switch shell wrapper.",
	Annotations: proxyAnnotation,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Guard against `ws proxy profile help <leaf>` — Cobra short-circuits
		// the `--help` flag path before PersistentPreRunE, but the explicit
		// `help` subcommand still walks the chain.
		if cmd.Name() == "help" {
			return nil
		}
		cfg := config.Load()
		noMigrate, _ := cmd.Flags().GetBool("no-migrate")
		return xray.EnsureMigrated(cfg, !noMigrate)
	},
}

var profileAddCmd = &cobra.Command{
	Use:         "add <name> <vless-uri>",
	Short:       "Add a new profile from a VLESS URI",
	Args:        cobra.ExactArgs(2),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		force, _ := cmd.Flags().GetBool("force")
		if err := xray.AddProfile(cfg, args[0], args[1], force); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Profile %q added", args[0]))
	},
}

var profileListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List profiles in a table or JSON",
	Args:        cobra.NoArgs,
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		profiles, err := xray.ListProfiles(cfg)
		if err != nil {
			output.Die(err.Error())
		}
		jsonFlag, _ := cmd.Flags().GetBool("json")
		reveal, _ := cmd.Flags().GetBool("reveal")

		if jsonFlag {
			if reveal {
				type fullRow struct {
					xray.ProfileSummary
					UUIDFull string `json:"uuid_full"`
				}
				rows := make([]fullRow, 0, len(profiles))
				for _, p := range profiles {
					dp, err := xray.LoadProfile(cfg, p.Name)
					if err != nil {
						output.Warn(fmt.Sprintf("load %s: %v", p.Name, err))
						continue
					}
					rows = append(rows, fullRow{ProfileSummary: p, UUIDFull: dp.UUID})
				}
				output.JSON(rows)
				return
			}
			output.JSON(profiles)
			return
		}

		t := output.NewTable([]string{"ACTIVE", "NAME", "TRANSPORT", "ADDRESS:PORT", "SNI", "UUID"})
		for _, p := range profiles {
			active := ""
			if p.Active {
				active = "*"
			}
			uuid := p.UUIDMasked
			if reveal {
				if dp, err := xray.LoadProfile(cfg, p.Name); err == nil {
					uuid = dp.UUID
				}
			}
			t.Row(active, p.Name, p.Transport, fmt.Sprintf("%s:%d", p.Address, p.Port), p.SNI, uuid)
		}
		fmt.Println(t)
	},
}

var profileUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Switch active xray profile and reload proxy",
	Long: `Validate, atomically swap, and reload the proxy so the new profile takes effect immediately.

Use --no-reload to perform only the symlink swap (advanced — operator must run 'ws proxy restart' manually to apply).`,
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigFn()
		noReload, _ := cmd.Flags().GetBool("no-reload")

		// --no-reload path: swap only, no docker side-effects, no pre-flight.
		// Operator takes explicit ownership of the restart side-effect;
		// xray's own internal bind-mount check inside SwitchToSymlinkOnly
		// still defends against legacy single-file bind.
		if noReload {
			if err := switchToSymlinkOnlyFn(cfg, args[0]); err != nil {
				cmd.SilenceUsage = true
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Switched to profile %q (proxy NOT reloaded — run 'ws proxy restart' to apply)\n",
				args[0])
			return nil
		}

		// Pre-flight (D-10 validation-before-mutation). If proxy is not
		// in a state where the post-swap restart will succeed, refuse to
		// swap the symlink so the operator never sees disk/runtime
		// divergence from a preventable cause.
		if err := verifyProxyReadyFn(cfg); err != nil {
			cmd.SilenceUsage = true
			return fmt.Errorf("proxy not ready for reload: %w", err)
		}

		// Atomic swap + restart via xray.SwitchTo (which itself drives
		// Validate -> AtomicSwap -> Restart -> WaitForHealth and owns the
		// TestManualRecoveryOnFailedSwitch tripwire contract).
		//
		// D-10 partial-failure boundary: on error, xray.SwitchTo already
		// rendered output.RenderError on stderr (switch.go ~127) and
		// wrapped the previous-profile name into the returned error. The
		// cmd layer does NOT render a duplicate error box and does NOT
		// auto-rollback the symlink. TestProfileUseRendersPartialFailureWithoutRollback
		// pins this contract.
		start := time.Now()
		if err := switchToFn(cfg, args[0]); err != nil {
			cmd.SilenceUsage = true
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(),
			"Switched to profile %q (proxy reloaded in %v)\n",
			args[0], time.Since(start).Truncate(time.Millisecond))
		return nil
	},
}

var profileRmCmd = &cobra.Command{
	Use:         "rm <name>",
	Short:       "Remove a profile (refuses active)",
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name := args[0]
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			desc := fmt.Sprintf("Profile file %s will be deleted.", filepath.Join(cfg.XrayProfilesDir, name+".json"))
			if !output.Confirm(fmt.Sprintf("Remove profile %q?", name), desc) {
				output.Info("Aborted")
				return
			}
		}
		if err := xray.RemoveProfile(cfg, name); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Profile %q removed", name))
	},
}

var profileShowCmd = &cobra.Command{
	Use:         "show <name>",
	Short:       "Show profile (masked unless --reveal)",
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		dp, err := xray.LoadProfile(cfg, args[0])
		if err != nil {
			output.Die(err.Error())
		}
		reveal, _ := cmd.Flags().GetBool("reveal")
		jsonFlag, _ := cmd.Flags().GetBool("json")

		if !reveal {
			dp.UUID = xray.MaskUUID(dp.UUID)
			dp.PublicKey = xray.MaskShort(dp.PublicKey)
			dp.ShortID = xray.MaskShort(dp.ShortID)
			dp.SpiderX = xray.MaskShort(dp.SpiderX)
		}

		if jsonFlag {
			output.JSON(dp)
			return
		}
		fmt.Printf("Name:       %s\n", dp.Name)
		if dp.Active {
			fmt.Println("Active:     yes")
		} else {
			fmt.Println("Active:     no")
		}
		fmt.Printf("Transport:  %s\n", dp.Transport)
		fmt.Printf("Address:    %s\n", dp.Address)
		fmt.Printf("Port:       %d\n", dp.Port)
		fmt.Printf("Security:   %s\n", dp.Security)
		if dp.SNI != "" {
			fmt.Printf("SNI:        %s\n", dp.SNI)
		}
		fmt.Printf("UUID:       %s\n", dp.UUID)
		if dp.Security == "reality" {
			fmt.Printf("PublicKey:  %s\n", dp.PublicKey)
			fmt.Printf("ShortID:    %s\n", dp.ShortID)
			fmt.Printf("SpiderX:    %s\n", dp.SpiderX)
		}
	},
}

var profileCurrentCmd = &cobra.Command{
	Use:         "current",
	Short:       "Print the currently active profile",
	Args:        cobra.NoArgs,
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name, err := xray.ReadActiveProfileName(cfg)
		if err != nil {
			output.Die(err.Error())
		}
		fmt.Println(name)
	},
}

var profileRegenCmd = &cobra.Command{
	Use:         "regenerate <name>",
	Short:       "Refresh routing rules in <name> from the currently-active profile",
	Args:        cobra.ExactArgs(1),
	Annotations: proxyAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if err := xray.RegenerateProfile(cfg, args[0]); err != nil {
			output.Die(err.Error())
		}
	},
}

func init() {
	profileCmd.PersistentFlags().Bool("no-migrate", false, "Refuse auto-migration of legacy regular-file config.json; error out instead")

	// Plan 22-02: per-command flags. --reveal intentionally NOT persistent on
	// profileCmd (D-13 leak risk); declared per leaf that may emit credentials.
	profileAddCmd.Flags().Bool("force", false, "Overwrite existing profile of the same name")
	profileRmCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")
	profileListCmd.Flags().Bool("json", false, "Emit JSON instead of an aligned table")
	profileListCmd.Flags().Bool("reveal", false, "Include cleartext UUID (default: masked)")
	profileShowCmd.Flags().Bool("json", false, "Emit JSON instead of key:value lines")
	profileShowCmd.Flags().Bool("reveal", false, "Include cleartext UUID/REALITY fields (default: masked)")
	profileUseCmd.Flags().Bool("no-reload", false,
		"Skip proxy restart after switching profile (advanced — operator must run 'ws proxy restart' manually to apply)")

	profileCmd.AddCommand(profileAddCmd)
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileUseCmd)
	profileCmd.AddCommand(profileRmCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileCurrentCmd)
	profileCmd.AddCommand(profileRegenCmd)
}

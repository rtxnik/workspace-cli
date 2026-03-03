package cmd

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/rtxnik/ws/internal/config"
	"github.com/rtxnik/ws/internal/output"
	"github.com/rtxnik/ws/internal/profile"
	"github.com/spf13/cobra"
)

var profileAnnotation = map[string]string{"group": "profile"}

var profilesCmd = &cobra.Command{
	Use:         "profiles",
	Short:       "List available profiles",
	Annotations: profileAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		profiles, err := profile.List(cfg)
		if err != nil {
			output.Die(err.Error())
		}
		if len(profiles) == 0 {
			output.Info("No profiles found")
			return
		}

		headerStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
		cellStyle := lipgloss.NewStyle().Padding(0, 1)

		rows := make([][]string, 0, len(profiles))
		for _, p := range profiles {
			rows = append(rows, []string{p.Name, p.BaseImage, p.Tools})
		}

		t := table.New().
			Headers("NAME", "BASE IMAGE", "TOOLS").
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

var profileCreateCmd = &cobra.Command{
	Use:         "profile-create <name>",
	Short:       "Create a custom profile",
	Args:        cobra.ExactArgs(1),
	Annotations: profileAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name := args[0]

		if err := profile.ValidateName(name); err != nil {
			output.Die(err.Error())
		}

		if profile.Exists(cfg, name) {
			output.Die(fmt.Sprintf("profile %q already exists", name))
		}

		baseImage, _ := cmd.Flags().GetString("image")
		dind, _ := cmd.Flags().GetBool("docker-in-docker")

		opts := profile.CreateOpts{
			Name:       name,
			BaseImage:  baseImage,
			DockerDind: dind,
		}

		if err := profile.Create(cfg, opts); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Profile %q created", name))
	},
}

var profileDeleteCmd = &cobra.Command{
	Use:         "profile-delete <name>",
	Short:       "Delete a custom profile",
	Args:        cobra.ExactArgs(1),
	Annotations: profileAnnotation,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		name := args[0]

		if profile.IsBuiltin(name) {
			output.Die(fmt.Sprintf("cannot delete built-in profile %q", name))
		}

		if err := profile.Delete(cfg, name); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Profile %q deleted", name))
	},
}

func init() {
	profileCreateCmd.Flags().String("image", "mcr.microsoft.com/devcontainers/base:ubuntu-24.04", "Base Docker image")
	profileCreateCmd.Flags().Bool("docker-in-docker", false, "Enable Docker-in-Docker feature")

	rootCmd.AddCommand(profilesCmd)
	rootCmd.AddCommand(profileCreateCmd)
	rootCmd.AddCommand(profileDeleteCmd)
}

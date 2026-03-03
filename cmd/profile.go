package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
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

		// Truncate long tool lists based on terminal width.
		maxTools := 40
		if w, _, err := term.GetSize(0); err == nil && w > 80 {
			maxTools = w - 50
		}

		rows := make([][]string, 0, len(profiles))
		for _, p := range profiles {
			tools := p.Tools
			if len(tools) > maxTools {
				tools = tools[:maxTools-1] + "…"
			}
			rows = append(rows, []string{p.Name, p.BaseImage, tools})
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

		// Non-interactive path: flags were explicitly provided.
		if cmd.Flags().Changed("image") || !isatty.IsTerminal(os.Stdin.Fd()) {
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
			return
		}

		// Interactive wizard.
		opts := runProfileWizard(name)
		if err := profile.Create(cfg, opts); err != nil {
			output.Die(err.Error())
		}
		output.Success(fmt.Sprintf("Profile %q created", name))
	},
}

var commonImages = []huh.Option[string]{
	huh.NewOption("Ubuntu 24.04 (default)", "mcr.microsoft.com/devcontainers/base:ubuntu-24.04"),
	huh.NewOption("Ubuntu 22.04", "mcr.microsoft.com/devcontainers/base:ubuntu-22.04"),
	huh.NewOption("Debian Bookworm", "mcr.microsoft.com/devcontainers/base:debian-bookworm"),
	huh.NewOption("Alpine 3.20", "mcr.microsoft.com/devcontainers/base:alpine-3.20"),
}

var commonTools = []huh.Option[string]{
	huh.NewOption("Go", "go"),
	huh.NewOption("Node.js", "node"),
	huh.NewOption("Python", "python"),
	huh.NewOption("Rust", "rust"),
	huh.NewOption("Java", "java"),
	huh.NewOption("Terraform", "terraform"),
	huh.NewOption("kubectl", "kubectl"),
	huh.NewOption("Helm", "helm"),
}

func runProfileWizard(name string) profile.CreateOpts {
	var baseImage string
	var packages string
	var selectedTools []string
	var dind bool
	var confirmed bool

	// Step 1: Base image.
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Base image").
				Options(commonImages...).
				Value(&baseImage),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("System packages (space-separated, optional)").
				Placeholder("e.g. jq wget htop").
				Value(&packages),
		),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Mise tools to install").
				Options(commonTools...).
				Value(&selectedTools),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Enable Docker-in-Docker?").
				Value(&dind),
		),
	)

	if err := form.Run(); err != nil {
		fmt.Fprintln(os.Stderr)
		os.Exit(0)
	}

	// Build summary.
	fmt.Fprintf(os.Stderr, "\n%s\n", output.SectionStyle.Render("Profile Summary"))
	output.Detail(fmt.Sprintf("Name:    %s", name))
	output.Detail(fmt.Sprintf("Image:   %s", baseImage))
	if packages != "" {
		output.Detail(fmt.Sprintf("Packages: %s", packages))
	}
	if len(selectedTools) > 0 {
		output.Detail(fmt.Sprintf("Tools:   %s", strings.Join(selectedTools, ", ")))
	}
	output.Detail(fmt.Sprintf("DinD:    %v", dind))
	fmt.Fprintln(os.Stderr)

	if err := huh.NewConfirm().
		Title("Create this profile?").
		Affirmative("Yes").
		Negative("No").
		Value(&confirmed).
		Run(); err != nil || !confirmed {
		output.Info("Aborted")
		os.Exit(0)
	}

	// Build opts.
	opts := profile.CreateOpts{
		Name:       name,
		BaseImage:  baseImage,
		DockerDind: dind,
	}

	if packages != "" {
		opts.Packages = strings.Fields(packages)
	}

	if len(selectedTools) > 0 {
		opts.MiseTools = make(map[string]string, len(selectedTools))
		for _, t := range selectedTools {
			opts.MiseTools[t] = "latest"
		}
	}

	return opts
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

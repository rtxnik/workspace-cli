package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/profile"
	"github.com/spf13/cobra"
)

var profileAnnotation = map[string]string{"group": "profile"}

var profilesCmd = &cobra.Command{
	Use:         "profiles",
	Short:       "List available profiles",
	Annotations: profileAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		profiles, err := profile.List(cfg)
		if err != nil {
			return err
		}
		jsonFlag, _ := cmd.Flags().GetBool("json")
		if jsonFlag {
			type profileJSON struct {
				Name      string `json:"name"`
				BaseImage string `json:"baseImage"`
				Tools     string `json:"tools"`
			}
			items := make([]profileJSON, 0, len(profiles))
			for _, p := range profiles {
				items = append(items, profileJSON{
					Name:      p.Name,
					BaseImage: p.BaseImage,
					Tools:     p.Tools,
				})
			}
			return output.WriteJSON(cmd.OutOrStdout(), items)
		}

		s := output.Out()
		if len(profiles) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), output.Empty{Subject: "profiles", Steps: []output.Remedy{
				{Label: "Create one", Cmd: "ws profile-create <name>"},
			}}.Render(s))
			return err
		}
		t, err := profilesTable(profiles)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), t.Render(s))
		return err
	},
}

// profilesTable is ws profiles' table, NAME, BASE IMAGE and TOOLS, with its
// caption. The tool list is cut by the allocator at the width of the stream
// it is rendered on, and not at all in a pipe.
func profilesTable(profiles []profile.Info) (output.Table, error) {
	t, err := output.NewTableBlock([]output.Col{
		{Title: "NAME", Prio: 1, Min: 6, Trunc: output.TruncMid},
		{Title: "BASE IMAGE", Prio: 3, Min: 12, Trunc: output.TruncHead},
		{Title: "TOOLS", Prio: 2, Min: 12, Trunc: output.TruncTail},
	}, nil)
	if err != nil {
		return output.Table{}, err
	}
	for _, p := range profiles {
		t.Rows = append(t.Rows, []output.Cell{output.Text(p.Name), output.Text(p.BaseImage), output.Text(p.Tools)})
	}
	t.Caption = countOf(len(profiles), "profile", "profiles")
	return t, nil
}

var profileCreateCmd = &cobra.Command{
	Use:         "profile-create <name>",
	Short:       "Create a custom profile",
	Args:        cobra.ExactArgs(1),
	Annotations: profileAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		name := args[0]

		if err := profile.ValidateName(name); err != nil {
			return err
		}

		if profile.Exists(cfg, name) {
			return fmt.Errorf("profile %q already exists", name)
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
				return err
			}
			output.Success(fmt.Sprintf("Profile %q created", name))
			return nil
		}

		// Interactive wizard.
		opts, ok := runProfileWizard(name)
		if !ok {
			return nil
		}
		if err := profile.Create(cfg, opts); err != nil {
			return err
		}
		output.Success(fmt.Sprintf("Profile %q created", name))
		return nil
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

// runProfileWizard walks the operator through a new profile's settings. ok is
// false when the operator backed out or the form could not run — a blank
// line after the first form, "Aborted" after the final confirmation — the
// caller returns nil, and the process exits 0.
func runProfileWizard(name string) (profile.CreateOpts, bool) {
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
		return profile.CreateOpts{}, false
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
		return profile.CreateOpts{}, false
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

	return opts, true
}

var profileDeleteCmd = &cobra.Command{
	Use:         "profile-delete <name>",
	Short:       "Delete a custom profile",
	Args:        cobra.ExactArgs(1),
	Annotations: profileAnnotation,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg := config.Load()
		name := args[0]

		if profile.IsBuiltin(name) {
			return &cliErrorWithExit{code: 1, msg: fmt.Sprintf("cannot delete built-in profile %q", name)}
		}
		if err := profile.ValidateName(name); err != nil {
			return &cliErrorWithExit{code: 1, msg: err.Error()}
		}

		force, _ := cmd.Flags().GetBool("force")
		if !confirmDestructiveFn(force,
			fmt.Sprintf("Delete profile %q?", name),
			"This will remove the profile directory and its files.") {
			output.Info("Aborted")
			return nil
		}

		if err := profile.Delete(cfg, name); err != nil {
			return &cliErrorWithExit{code: 1, msg: err.Error()}
		}
		output.Success(fmt.Sprintf("Profile %q deleted", name))
		return nil
	},
}

func init() {
	profileCreateCmd.Flags().String("image", "mcr.microsoft.com/devcontainers/base:ubuntu-24.04", "Base Docker image")
	profileCreateCmd.Flags().Bool("docker-in-docker", false, "Enable Docker-in-Docker feature")
	profileDeleteCmd.Flags().BoolP("force", "f", false, "Skip delete confirmation")

	rootCmd.AddCommand(profilesCmd)
	rootCmd.AddCommand(profileCreateCmd)
	rootCmd.AddCommand(profileDeleteCmd)
}

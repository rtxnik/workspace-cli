package profile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/rtxnik/ws/internal/config"
)

var builtinProfiles = map[string]bool{
	"default": true, "devops": true, "go": true, "k8s": true,
	"matrix": true, "web": true, "proxy": true, "rust": true, "python": true, "synopra": true,
}

// Info holds parsed profile metadata.
type Info struct {
	Name      string
	BaseImage string
	Tools     string
}

// List returns metadata for all profiles in the profiles directory.
func List(cfg config.Config) ([]Info, error) {
	entries, err := os.ReadDir(cfg.ProfilesDir)
	if err != nil {
		return nil, fmt.Errorf("read profiles dir: %w", err)
	}

	var profiles []Info
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "proxy" {
			continue
		}
		name := e.Name()
		p := Info{
			Name:      name,
			BaseImage: parseDockerfileBase(filepath.Join(cfg.ProfilesDir, name, "Dockerfile")),
			Tools:     parseMiseTools(filepath.Join(cfg.ProfilesDir, name, "mise.toml")),
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// IsBuiltin returns true if the profile is a built-in that cannot be deleted.
func IsBuiltin(name string) bool {
	return builtinProfiles[name]
}

// Delete removes a custom profile directory.
func Delete(cfg config.Config, name string) error {
	if IsBuiltin(name) {
		return fmt.Errorf("cannot delete built-in profile %q", name)
	}
	dir := filepath.Join(cfg.ProfilesDir, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("profile %q not found", name)
	}
	return os.RemoveAll(dir)
}

// Exists returns true if a profile directory exists.
func Exists(cfg config.Config, name string) bool {
	_, err := os.Stat(filepath.Join(cfg.ProfilesDir, name))
	return err == nil
}

// CreateOpts holds parameters for creating a new profile.
type CreateOpts struct {
	Name       string
	BaseImage  string
	Packages   []string
	MiseTools  map[string]string
	DockerDind bool
}

// Create generates a new profile directory with Dockerfile, devcontainer.json,
// and optionally mise.toml.
func Create(cfg config.Config, opts CreateOpts) error {
	dir := filepath.Join(cfg.ProfilesDir, opts.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}

	if err := writeFromTemplate(filepath.Join(dir, "Dockerfile"), dockerfileTmpl, opts); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}
	if err := writeFromTemplate(filepath.Join(dir, "devcontainer.json"), devcontainerTmpl, opts); err != nil {
		return fmt.Errorf("write devcontainer.json: %w", err)
	}
	if len(opts.MiseTools) > 0 {
		if err := writeFromTemplate(filepath.Join(dir, "mise.toml"), miseTmpl, opts); err != nil {
			return fmt.Errorf("write mise.toml: %w", err)
		}
	}
	return nil
}

// parseDockerfileBase extracts the base image from the FROM instruction.
func parseDockerfileBase(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}

// parseMiseTools extracts tool names from a mise.toml [tools] section.
func parseMiseTools(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var tools []string
	inTools := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[tools]" {
			inTools = true
			continue
		}
		if inTools {
			if strings.HasPrefix(line, "[") {
				break
			}
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				tools = append(tools, strings.TrimSpace(parts[0]))
			}
		}
	}
	return strings.Join(tools, ", ")
}

func writeFromTemplate(path, tmplStr string, data any) error {
	tmpl, err := template.New("").Parse(tmplStr)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, data)
}

var dockerfileTmpl = `FROM {{ .BaseImage }}

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl{{ range .Packages }} {{ . }}{{ end }} \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL https://mise.jdx.dev/install.sh | sh

ENV PATH="/home/vscode/.local/bin:${PATH}"
`

var devcontainerTmpl = `{
	"name": "{{ .Name }}",
	"build": {
		"dockerfile": "Dockerfile"
	},
	"features": {
		"ghcr.io/devcontainers/features/common-utils:2": {
			"installZsh": true,
			"configureZshAsDefaultShell": true,
			"installOhMyZsh": false
		},
		"ghcr.io/devcontainers/features/git:1": {},
		"ghcr.io/devcontainers/features/github-cli:1": {}{{ if .DockerDind }},
		"ghcr.io/devcontainers/features/docker-in-docker:2": {}{{ end }}
	},
	"containerEnv": {
		"WORKSPACE_PROFILE": "{{ .Name }}"
	},
	"postCreateCommand": "bash .devcontainer/post-create.sh",
	"remoteUser": "vscode"
}
`

var miseTmpl = `[tools]
{{ range $tool, $version := .MiseTools }}{{ $tool }} = "{{ $version }}"
{{ end }}`

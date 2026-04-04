package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
)

type Info struct {
	Name    string
	Status  string
	Profile string
	Proxy   bool
}

// Exists returns true if a workspace directory exists.
func Exists(cfg config.Config, name string) bool {
	_, err := os.Stat(filepath.Join(cfg.WorkspacesDir, name))
	return err == nil
}

// Create sets up a new workspace directory with devcontainer config.
func Create(cfg config.Config, name, profile string, withProxy bool) error {
	wsDir := filepath.Join(cfg.WorkspacesDir, name)
	dcDir := filepath.Join(wsDir, ".devcontainer")
	profileDir := filepath.Join(cfg.ProfilesDir, profile)

	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	filesToCopy := []string{"devcontainer.json", "Dockerfile"}
	for _, f := range filesToCopy {
		src := filepath.Join(profileDir, f)
		dst := filepath.Join(dcDir, f)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", f, err)
		}
	}

	// Copy mise.toml if present in profile.
	miseSrc := filepath.Join(profileDir, "mise.toml")
	if _, err := os.Stat(miseSrc); err == nil {
		if err := copyFile(miseSrc, filepath.Join(dcDir, "mise.toml")); err != nil {
			return fmt.Errorf("copy mise.toml: %w", err)
		}
	}

	// Copy post-create.sh from shared dir.
	postCreate := filepath.Join(cfg.SharedDir, "post-create.sh")
	if _, err := os.Stat(postCreate); err == nil {
		if err := copyFile(postCreate, filepath.Join(dcDir, "post-create.sh")); err != nil {
			return fmt.Errorf("copy post-create.sh: %w", err)
		}
	}

	// If proxy requested, patch devcontainer.json to add proxy network.
	if withProxy {
		if err := patchProxyNetwork(filepath.Join(dcDir, "devcontainer.json"), cfg.ProxyNetwork, cfg.ProxyIP); err != nil {
			return fmt.Errorf("patch proxy network: %w", err)
		}
	}

	return nil
}

// List returns info about all workspaces found in the workspaces directory.
func List(cfg config.Config) ([]Info, error) {
	entries, err := os.ReadDir(cfg.WorkspacesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspaces dir: %w", err)
	}

	statuses := devpodStatuses()

	var workspaces []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		ws := Info{
			Name:    name,
			Status:  statuses[name],
			Profile: readProfile(cfg, name),
			Proxy:   hasProxyNetwork(cfg, name),
		}
		if ws.Status == "" {
			ws.Status = "NotCreated"
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

// devpodStatuses queries devpod for workspace statuses.
func devpodStatuses() map[string]string {
	result := make(map[string]string)
	out, err := exec.Command("devpod", "list", "--output", "json").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			output.Warn("devpod not found in PATH, workspace statuses unavailable")
		} else {
			output.Warn(fmt.Sprintf("devpod list failed: %s", err))
		}
		return result
	}

	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return result
	}
	for _, item := range items {
		out, err := exec.Command("devpod", "status", item.ID).CombinedOutput()
		if err != nil {
			continue
		}
		result[item.ID] = parseDevpodStatus(string(out))
	}
	return result
}

// readProfile reads the WORKSPACE_PROFILE env var from devcontainer.json.
func readProfile(cfg config.Config, name string) string {
	dcPath := filepath.Join(cfg.WorkspacesDir, name, ".devcontainer", "devcontainer.json")
	data, err := os.ReadFile(dcPath)
	if err != nil {
		return ""
	}
	// Strip JSONC comments for parsing.
	cleaned := stripJSONCComments(string(data))
	var dc struct {
		ContainerEnv map[string]string `json:"containerEnv"`
	}
	if err := json.Unmarshal([]byte(cleaned), &dc); err != nil {
		return ""
	}
	return dc.ContainerEnv["WORKSPACE_PROFILE"]
}

// HasProxyNetwork reports whether the workspace has proxy networking enabled.
func HasProxyNetwork(cfg config.Config, name string) bool {
	return hasProxyNetwork(cfg, name)
}

// hasProxyNetwork checks if the workspace devcontainer.json has proxy network.
func hasProxyNetwork(cfg config.Config, name string) bool {
	dcPath := filepath.Join(cfg.WorkspacesDir, name, ".devcontainer", "devcontainer.json")
	data, err := os.ReadFile(dcPath)
	if err != nil {
		return false
	}
	cleaned := stripJSONCComments(string(data))
	var dc struct {
		RunArgs []string `json:"runArgs"`
	}
	if err := json.Unmarshal([]byte(cleaned), &dc); err != nil {
		return false
	}
	for _, arg := range dc.RunArgs {
		if strings.HasPrefix(arg, "--network=container:") || arg == "--network="+cfg.ProxyNetwork {
			return true
		}
	}
	return false
}

// stripJSONCComments removes single-line // comments from JSONC.
func stripJSONCComments(s string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		if s[i] == '"' && (i == 0 || s[i-1] != '\\') {
			inString = !inString
		}
		if !inString && i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
			// Skip to end of line.
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseDevpodStatus extracts the status from devpod status output.
// Example input: "18:33:52 info Workspace 'dotfiles' is 'Running'"
func parseDevpodStatus(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if i := strings.LastIndex(line, " is '"); i != -1 {
			rest := line[i+5:]
			if j := strings.Index(rest, "'"); j != -1 {
				return rest[:j]
			}
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func patchProxyNetwork(dcPath, proxyNetwork, proxyIP string) error {
	data, err := os.ReadFile(dcPath)
	if err != nil {
		return err
	}

	cleaned := stripJSONCComments(string(data))
	var dc map[string]any
	if err := json.Unmarshal([]byte(cleaned), &dc); err != nil {
		return err
	}

	// Connect workspace to the proxy bridge network with NET_ADMIN
	// for route override in postStartCommand.
	runArgs, _ := dc["runArgs"].([]any)
	runArgs = append(runArgs, "--network="+proxyNetwork, "--cap-add=NET_ADMIN")
	dc["runArgs"] = runArgs

	// Add postStartCommand to route traffic through the proxy.
	routeCmd := fmt.Sprintf("sudo ip route replace default via %s 2>/dev/null || true", proxyIP)
	switch existing := dc["postStartCommand"].(type) {
	case map[string]any:
		existing["proxy-route"] = routeCmd
	case string:
		dc["postStartCommand"] = map[string]any{
			"setup":       existing,
			"proxy-route": routeCmd,
		}
	default:
		dc["postStartCommand"] = map[string]any{
			"proxy-route": routeCmd,
		}
	}

	out, err := json.MarshalIndent(dc, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(dcPath, out, 0o644)
}

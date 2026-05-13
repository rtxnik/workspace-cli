package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/vless"
)

// RegenerateProfile refreshes the routing rules in <name>.json from the
// currently-active profile (D-05 drift fix). Symlink is NOT touched.
// Refuses to operate (no-op + Info) if <name> is the active profile —
// copying routing from itself is a tautology and the operator likely meant
// a different target.
func RegenerateProfile(cfg config.Config, name string) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}

	active, err := ReadActiveProfileName(cfg)
	if err != nil {
		return fmt.Errorf("read active profile: %w", err)
	}
	if active == name {
		output.Info(fmt.Sprintf("Profile %q is the currently-active profile; regenerate is a no-op (it would copy routing from itself).", name))
		return nil
	}

	// Load active profile's routing.
	activePath := filepath.Join(cfg.XrayProfilesDir, active+".json")
	activeData, err := os.ReadFile(activePath)
	if err != nil {
		return fmt.Errorf("read active profile %q: %w", active, err)
	}
	var activeCfg vless.XrayConfig
	if err := json.Unmarshal(activeData, &activeCfg); err != nil {
		return fmt.Errorf("parse active profile %q: %w", active, err)
	}

	// Load target profile.
	targetPath := filepath.Join(cfg.XrayProfilesDir, name+".json")
	targetData, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("read target profile %q: %w", name, err)
	}
	var targetCfg vless.XrayConfig
	if err := json.Unmarshal(targetData, &targetCfg); err != nil {
		return fmt.Errorf("parse target profile %q: %w", name, err)
	}

	// Copy routing field only — other fields (outbound, transport) preserved.
	targetCfg.Routing = activeCfg.Routing

	out, err := json.MarshalIndent(&targetCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal regenerated profile: %w", err)
	}
	if err := os.WriteFile(targetPath, out, 0o644); err != nil {
		return fmt.Errorf("write regenerated profile: %w", err)
	}
	output.Success(fmt.Sprintf("Profile %q routing refreshed from active profile %q", name, active))
	return nil
}

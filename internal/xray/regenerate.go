package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/fsutil"
	"github.com/rtxnik/workspace-cli/internal/output"
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

	// Load the active profile's routing as raw JSON so unknown sub-keys
	// (e.g. domainMatcher, balancer strategy.settings) survive the copy.
	activePath := filepath.Join(cfg.XrayProfilesDir, active+".json")
	activeData, err := os.ReadFile(activePath)
	if err != nil {
		return fmt.Errorf("read active profile %q: %w", active, err)
	}
	var activeRaw map[string]json.RawMessage
	if err := json.Unmarshal(activeData, &activeRaw); err != nil {
		return fmt.Errorf("parse active profile %q: %w", active, err)
	}

	// Load the target's ORIGINAL bytes; regenerate owns only the routing key,
	// so every other top-level section the operator hand-added (dns, api,
	// policy, stats, transport, …) is preserved byte-for-byte. Squeezing the
	// target through the fixed XrayConfig struct here would silently delete
	// them.
	targetPath := filepath.Join(cfg.XrayProfilesDir, name+".json")
	targetData, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("read target profile %q: %w", name, err)
	}

	// regenerate ALWAYS owns routing: mirror the active profile's routing onto
	// the target. When the active profile has a routing block, copy it verbatim
	// (raw) so unknown sub-keys like domainMatcher survive; when it has none,
	// map "routing" to nil so applyOwnedKeys DELETES the target's routing — a
	// faithful sync. Silently preserving the target's stale routing here would
	// violate the "refresh routing from the active profile" contract.
	owned := map[string]json.RawMessage{"routing": nil}
	if r, ok := activeRaw["routing"]; ok {
		owned["routing"] = r
	}
	out, err := applyOwnedKeys(targetData, owned)
	if err != nil {
		return fmt.Errorf("parse target profile %q: %w", name, err)
	}
	if err := fsutil.WriteFile(targetPath, out, 0o600); err != nil {
		return fmt.Errorf("write regenerated profile: %w", err)
	}
	output.Success(fmt.Sprintf("Profile %q routing refreshed from active profile %q", name, active))
	return nil
}

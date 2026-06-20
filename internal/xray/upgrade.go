package xray

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// proxyProtocols is the set of outbound protocols that represent a proxy.
var proxyProtocols = map[string]bool{
	"vless":    true,
	"hysteria": true,
	"vmess":    true,
	"trojan":   true,
}

// UpgradeProfileInbounds iterates every *.json file in cfg.XrayProfilesDir and
// replaces its Inbounds with the canonical inbounds produced by AssembleConfig
// (which carries streamSettings.sockopt.tproxy="tproxy"). Outbounds and Routing
// are left untouched. Profiles that have no recognisable proxy outbound are
// skipped with a warning rather than aborting the whole run.
// Returns the number of profiles that were actually changed.
func UpgradeProfileInbounds(cfg config.Config) (int, error) {
	entries, err := os.ReadDir(cfg.XrayProfilesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read profiles dir %s: %w", cfg.XrayProfilesDir, err)
	}

	changed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(cfg.XrayProfilesDir, entry.Name())
		n, err := upgradeProfile(path)
		if err != nil {
			return changed, fmt.Errorf("upgrade %s: %w", entry.Name(), err)
		}
		if n {
			changed++
		}
	}
	return changed, nil
}

// upgradeProfile upgrades a single profile file. Returns true if the file was
// changed (i.e. the inbound did not already have sockopt.tproxy).
func upgradeProfile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}

	var xc xrayconf.XrayConfig
	if err := json.Unmarshal(data, &xc); err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}

	// Already current: inbound has sockopt.tproxy.
	if len(xc.Inbounds) > 0 &&
		xc.Inbounds[0].StreamSettings != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt.Tproxy == "tproxy" {
		return false, nil
	}

	// Find the first proxy outbound.
	proxy, found := firstProxyOutbound(xc.Outbounds)
	if !found {
		log.Printf("ws proxy upgrade-config: skipping %s — no proxy outbound found", filepath.Base(path))
		return false, nil
	}

	// Build canonical inbounds and splice them in, keeping outbounds + routing.
	canonical := xrayconf.AssembleConfig(proxy)
	xc.Inbounds = canonical.Inbounds

	out, err := json.MarshalIndent(xc, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	return true, nil
}

// firstProxyOutbound returns the first outbound whose protocol is a known proxy
// protocol (vless, hysteria, vmess, trojan). "direct" and "freedom" are skipped.
func firstProxyOutbound(outbounds []xrayconf.Outbound) (xrayconf.Outbound, bool) {
	for _, ob := range outbounds {
		if proxyProtocols[ob.Protocol] {
			return ob, true
		}
	}
	return xrayconf.Outbound{}, false
}

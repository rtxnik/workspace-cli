package xray

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/fsutil"
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
// changed (inbound migration or outbound repair).
func upgradeProfile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}

	var xc xrayconf.XrayConfig
	if err := json.Unmarshal(data, &xc); err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}

	base := filepath.Base(path)

	// Inbound migration: splice canonical inbounds (sockopt.tproxy) unless the
	// inbound is already current. Requires a proxy outbound to build them.
	inboundCurrent := len(xc.Inbounds) > 0 &&
		xc.Inbounds[0].StreamSettings != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt != nil &&
		xc.Inbounds[0].StreamSettings.Sockopt.Tproxy == "tproxy"

	inboundChanged := false
	if !inboundCurrent {
		proxy, found := firstProxyOutbound(xc.Outbounds)
		if found {
			// D4-06: the canonical inbound is fixed at port 12345; normalizing a
			// non-canonical port is correct but must never be silent.
			if len(xc.Inbounds) > 0 && xc.Inbounds[0].Port != 0 && xc.Inbounds[0].Port != 12345 {
				log.Printf("ws proxy upgrade-config: profile %s inbound port %d normalized to 12345 (required by the TPROXY datapath)",
					base, xc.Inbounds[0].Port)
			}
			xc.Inbounds = xrayconf.AssembleConfig(proxy).Inbounds
			inboundChanged = true
		} else {
			log.Printf("ws proxy upgrade-config: skipping inbound migration for %s — no proxy outbound found", base)
		}
	}

	// Outbound repair: re-encode legacy cert pins, migrate h2 -> xhttp.
	repairs, err := RepairConfig(&xc)
	if err != nil {
		return false, fmt.Errorf("repair outbounds: %w", err)
	}
	for _, r := range repairs {
		log.Printf("ws proxy upgrade-config: %s — %s", base, r)
	}
	outboundChanged := len(repairs) > 0

	if !inboundChanged && !outboundChanged {
		return false, nil
	}

	// Rewrite only the keys this command owns, splicing them over the original
	// bytes so every hand-added top-level section (dns, api, policy, stats,
	// transport, …) and unknown routing sub-key survives. Only the field that
	// actually changed is re-serialized.
	owned := map[string]json.RawMessage{}
	if inboundChanged {
		// The inbound is wholesale-replaced with the canonical TPROXY block by
		// design (the datapath migration), so marshalling the typed inbounds is
		// correct here.
		inb, err := json.Marshal(xc.Inbounds)
		if err != nil {
			return false, fmt.Errorf("marshal inbounds: %w", err)
		}
		owned["inbounds"] = inb
	}
	if outboundChanged {
		// RepairConfig only ever rewrites streamSettings (see RepairOutbound), so
		// splice each repaired outbound's streamSettings back into the ORIGINAL
		// raw outbound object. Marshalling the typed xc.Outbounds instead would
		// drop outbound-level fields the struct does not model (sendThrough, mux,
		// proxySettings) — a silent, lossy round-trip.
		outb, err := spliceRepairedStreamSettings(data, xc.Outbounds)
		if err != nil {
			return false, fmt.Errorf("splice repaired outbounds: %w", err)
		}
		owned["outbounds"] = outb
	}

	out, err := applyOwnedKeys(data, owned)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	if err := fsutil.WriteFile(path, out, 0o600); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	return true, nil
}

// spliceRepairedStreamSettings returns the profile's outbounds array with each
// repaired outbound's streamSettings written back into the ORIGINAL raw outbound
// object, preserving outbound-level fields the typed xrayconf.Outbound does not
// model (sendThrough, mux, proxySettings). repaired MUST be the same slice that
// was unmarshalled from original — RepairConfig mutates in place and never adds
// or removes entries, so index i of repaired aligns with index i of the raw
// array. RepairOutbound preserves unknown streamSettings sub-keys itself (it
// round-trips streamSettings through map[string]any), so splicing its result is
// lossless. An outbound with no streamSettings is left untouched.
func spliceRepairedStreamSettings(original []byte, repaired []xrayconf.Outbound) (json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(original, &root); err != nil {
		return nil, err
	}
	var rawOut []map[string]json.RawMessage
	if err := json.Unmarshal(root["outbounds"], &rawOut); err != nil {
		return nil, fmt.Errorf("parse outbounds: %w", err)
	}
	for i := range repaired {
		if i >= len(rawOut) {
			break
		}
		if len(repaired[i].StreamSettings) > 0 {
			rawOut[i]["streamSettings"] = repaired[i].StreamSettings
		}
	}
	return json.Marshal(rawOut)
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

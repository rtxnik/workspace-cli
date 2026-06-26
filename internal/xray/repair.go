package xray

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"

	"github.com/rtxnik/workspace-cli/internal/hysteria2"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// lowerHex64 matches a cert pin already in the canonical form xray v26 expects
// (64 lowercase hex chars). Anything else (base64, uppercase hex, colon-hex)
// is re-encoded.
var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// RepairOutbound normalizes a stored outbound to the shapes a current xray-core
// (v26) accepts: a non-lowercase-hex cert pin is re-encoded to lowercase hex,
// and (Task 3) an h2 transport is migrated to XHTTP stream-one. It returns the
// (possibly) updated outbound, human-readable descriptions of what changed
// (empty = nothing), and an error only on an unrecoverable streamSettings
// parse failure. Unknown streamSettings keys are preserved.
func RepairOutbound(ob xrayconf.Outbound) (xrayconf.Outbound, []string, error) {
	if len(ob.StreamSettings) == 0 {
		return ob, nil, nil
	}
	var ss map[string]any
	if err := json.Unmarshal(ob.StreamSettings, &ss); err != nil {
		return ob, nil, fmt.Errorf("parse streamSettings: %w", err)
	}

	var changes []string
	changes = append(changes, repairPin(ss)...)
	changes = append(changes, repairH2(ss)...)

	if len(changes) == 0 {
		return ob, nil, nil
	}
	out, err := json.Marshal(ss)
	if err != nil {
		return ob, nil, fmt.Errorf("marshal streamSettings: %w", err)
	}
	ob.StreamSettings = out
	return ob, changes, nil
}

// repairPin re-encodes tlsSettings.pinnedPeerCertSha256 to lowercase hex when it
// is not already in that form. A pin that does not decode to 32 bytes in any
// accepted encoding is left unchanged with a warning (never corrupted).
func repairPin(ss map[string]any) []string {
	tls, ok := ss["tlsSettings"].(map[string]any)
	if !ok {
		return nil
	}
	v, ok := tls["pinnedPeerCertSha256"].(string)
	if !ok || v == "" || lowerHex64.MatchString(v) {
		return nil
	}
	hexPin, err := hysteria2.NormalizePinSHA256(v)
	if err != nil {
		log.Printf("ws proxy upgrade-config: cert pin %q is not a valid 32-byte sha256; leaving unchanged (%v)", v, err)
		return nil
	}
	tls["pinnedPeerCertSha256"] = hexPin
	return []string{"re-encoded cert pin to lowercase hex"}
}

// repairH2 migrates a stored h2 transport to its xray v26 replacement, XHTTP
// stream-one (xray removed the HTTP/2 transport). Mirrors internal/vless's
// generation of a parsed type=h2 URI so the two never drift.
func repairH2(ss map[string]any) []string {
	if net, _ := ss["network"].(string); net != "h2" {
		return nil
	}
	xhttp := map[string]any{"mode": "stream-one", "path": ""}
	if http, ok := ss["httpSettings"].(map[string]any); ok {
		if p, ok := http["path"].(string); ok {
			xhttp["path"] = p
		}
		if h := firstHost(http["host"]); h != "" {
			xhttp["host"] = h
		}
	}
	ss["network"] = "xhttp"
	ss["xhttpSettings"] = xhttp
	delete(ss, "httpSettings")
	return []string{"migrated h2 transport to xhttp stream-one"}
}

// firstHost reads a host string from the stored h2 httpSettings.host, which the
// pre-fix generator wrote as a []string; a plain string is tolerated too.
func firstHost(v any) string {
	switch h := v.(type) {
	case string:
		return h
	case []any:
		if len(h) > 0 {
			s, _ := h[0].(string)
			return s
		}
	}
	return ""
}

// RepairConfig applies RepairOutbound to every proxy outbound in xc, mutating it
// in place. A per-outbound parse failure is logged and skipped (the run is never
// aborted). Returns the combined repair descriptions across all outbounds.
func RepairConfig(xc *xrayconf.XrayConfig) ([]string, error) {
	var changes []string
	for i, ob := range xc.Outbounds {
		if !proxyProtocols[ob.Protocol] {
			continue
		}
		repaired, c, err := RepairOutbound(ob)
		if err != nil {
			log.Printf("ws proxy upgrade-config: skipping outbound %q — %v", ob.Tag, err)
			continue
		}
		if len(c) > 0 {
			xc.Outbounds[i] = repaired
			changes = append(changes, c...)
		}
	}
	return changes, nil
}

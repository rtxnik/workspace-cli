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

	changes := repairPin(ss)

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

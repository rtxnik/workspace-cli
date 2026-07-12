package xray

import "encoding/json"

// applyOwnedKeys re-serializes the original profile JSON with only the given
// top-level keys replaced, preserving every OTHER top-level key — including
// hand-added sections the fixed xrayconf.XrayConfig struct does not model
// (dns, api, policy, stats, transport, reverse, fakedns, observatory, …) and
// unknown routing sub-keys such as domainMatcher — as raw bytes (re-indented
// only). It is the lossless-rewrite primitive shared by RegenerateProfile and
// upgradeProfile, mirroring the map-patch cmd.setXrayLogLevel already uses
// in-tree so a whole-struct re-marshal never silently truncates a profile.
//
// original must be a JSON object. A key mapped to a non-nil value is
// set/overwritten; a key mapped to a nil value is DELETED (used to faithfully
// clear an owned section — e.g. regenerate syncing a target's routing to an
// active profile that has none); keys absent from owned are carried through
// untouched.
func applyOwnedKeys(original []byte, owned map[string]json.RawMessage) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage, len(owned))
	}
	for k, v := range owned {
		if v == nil {
			delete(raw, k)
			continue
		}
		raw[k] = v
	}
	return json.MarshalIndent(raw, "", "  ")
}

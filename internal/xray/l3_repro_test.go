package xray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// TestL3_08a_RegenerateDropsUnknownSections proves `ws proxy profile regenerate`
// preserves hand-added top-level sections on the target and carries the active
// profile's unknown routing sub-keys (domainMatcher) into it — instead of
// silently deleting everything the fixed XrayConfig struct does not model.
func TestL3_08a_RegenerateDropsUnknownSections(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}

	// Active profile: stock scaffold + a hand-added routing.domainMatcher that
	// regenerate must carry into the target (the struct round-trip drops it).
	activeJSON := `{
  "log": {"loglevel": "warning"},
  "inbounds": [{"tag":"transparent","port":12345,"protocol":"dokodemo-door","settings":{"network":"tcp,udp","followRedirect":true},"streamSettings":{"sockopt":{"tproxy":"tproxy"}}}],
  "outbounds": [{"tag":"proxy-1","protocol":"vless","settings":{"vnext":[]}},{"tag":"direct","protocol":"freedom","settings":{}}],
  "routing": {"domainStrategy":"IPIfNonMatch","domainMatcher":"mph","rules":[{"type":"field","network":"tcp,udp","balancerTag":"proxy-balancer"}]}
}`
	if err := os.WriteFile(filepath.Join(profilesDir, "base.json"), []byte(activeJSON), 0o600); err != nil {
		t.Fatalf("write active: %v", err)
	}

	// Target profile: stock scaffold + hand-added top-level dns and policy
	// sections that belong to the target and MUST survive regenerate.
	targetJSON := `{
  "log": {"loglevel": "warning"},
  "dns": {"servers":["1.1.1.1"],"queryStrategy":"UseIPv4"},
  "policy": {"levels":{"0":{"connIdle":300}}},
  "inbounds": [{"tag":"transparent","port":12345,"protocol":"dokodemo-door","settings":{"network":"tcp,udp","followRedirect":true},"streamSettings":{"sockopt":{"tproxy":"tproxy"}}}],
  "outbounds": [{"tag":"proxy-2","protocol":"vless","settings":{"vnext":[]}},{"tag":"direct","protocol":"freedom","settings":{}}],
  "routing": {"domainStrategy":"IPIfNonMatch","rules":[]}
}`
	if err := os.WriteFile(filepath.Join(profilesDir, "custom.json"), []byte(targetJSON), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	// Active-config symlink -> profiles/base.json (D-07 layout).
	cfgPath := filepath.Join(root, "config.json")
	if err := os.Symlink(filepath.Join("profiles", "base.json"), cfgPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	cfg := config.Config{XrayConfig: cfgPath, XrayProfilesDir: profilesDir}

	if err := RegenerateProfile(cfg, "custom"); err != nil {
		t.Fatalf("RegenerateProfile: %v", err)
	}

	out, err := os.ReadFile(filepath.Join(profilesDir, "custom.json"))
	if err != nil {
		t.Fatalf("read regenerated: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("regenerated profile is not a JSON object: %v", err)
	}

	// Hand-added top-level sections on the target MUST survive.
	if _, ok := got["dns"]; !ok {
		t.Errorf("regenerate dropped hand-added top-level \"dns\" section:\n%s", out)
	}
	if _, ok := got["policy"]; !ok {
		t.Errorf("regenerate dropped hand-added top-level \"policy\" section:\n%s", out)
	}

	// Regenerate copies the ACTIVE routing verbatim -> the target's routing must
	// now carry the active profile's domainMatcher (a struct-filtered copy drops it).
	rawRouting, ok := got["routing"]
	if !ok {
		t.Fatalf("regenerated profile lost its routing section:\n%s", out)
	}
	var routing map[string]json.RawMessage
	if err := json.Unmarshal(rawRouting, &routing); err != nil {
		t.Fatalf("routing is not an object: %v", err)
	}
	if _, ok := routing["domainMatcher"]; !ok {
		t.Errorf("regenerate did not carry active routing.domainMatcher into the target:\n%s", out)
	}
}

// TestRegenerate_ClearsRoutingWhenActiveHasNone hardens the routing-sync
// contract: when the active profile has no routing block, regenerate must
// faithfully clear the target's routing rather than silently keep the target's
// stale routing.
func TestRegenerate_ClearsRoutingWhenActiveHasNone(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Active profile deliberately has NO routing block.
	activeJSON := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[{"tag":"proxy-1","protocol":"vless","settings":{}}]}`
	if err := os.WriteFile(filepath.Join(profilesDir, "base.json"), []byte(activeJSON), 0o600); err != nil {
		t.Fatalf("write active: %v", err)
	}
	// Target has a routing block that must be cleared to mirror the active profile.
	targetJSON := `{"log":{"loglevel":"warning"},"outbounds":[],"routing":{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","outboundTag":"direct"}]}}`
	if err := os.WriteFile(filepath.Join(profilesDir, "custom.json"), []byte(targetJSON), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	cfgPath := filepath.Join(root, "config.json")
	if err := os.Symlink(filepath.Join("profiles", "base.json"), cfgPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cfg := config.Config{XrayConfig: cfgPath, XrayProfilesDir: profilesDir}

	if err := RegenerateProfile(cfg, "custom"); err != nil {
		t.Fatalf("RegenerateProfile: %v", err)
	}

	out, err := os.ReadFile(filepath.Join(profilesDir, "custom.json"))
	if err != nil {
		t.Fatalf("read regenerated: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("regenerated profile is not a JSON object: %v", err)
	}
	if _, ok := got["routing"]; ok {
		t.Errorf("regenerate must clear the target routing when the active profile has none:\n%s", out)
	}
}

// TestL3_08b_UpgradeConfigDropsUnknownSections proves `ws proxy upgrade-config`
// preserves hand-added top-level sections and unknown routing sub-keys while it
// migrates the inbound — instead of squeezing the whole profile through the
// fixed XrayConfig struct and deleting them.
func TestL3_08b_UpgradeConfigDropsUnknownSections(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}

	// A hand-customized profile with a LEGACY inbound (no streamSettings ->
	// triggers the tproxy migration) plus hand-added dns/policy/stats top-level
	// sections and a routing.domainMatcher, all of which must survive the rewrite.
	profileJSON := `{
  "log": {"loglevel": "warning"},
  "dns": {"servers":["1.1.1.1"],"queryStrategy":"UseIPv4"},
  "policy": {"levels":{"0":{"connIdle":300}}},
  "stats": {},
  "inbounds": [{"tag":"transparent","port":12345,"protocol":"dokodemo-door","settings":{"network":"tcp,udp","followRedirect":true}}],
  "outbounds": [{"tag":"proxy-1","protocol":"vless","settings":{"vnext":[{"address":"example.com","port":443,"users":[{"id":"11111111-1111-1111-1111-111111111111"}]}]}},{"tag":"direct","protocol":"freedom","settings":{}}],
  "routing": {"domainStrategy":"IPIfNonMatch","domainMatcher":"mph","rules":[{"type":"field","network":"tcp,udp","balancerTag":"proxy-balancer"}]}
}`
	profilePath := filepath.Join(profilesDir, "custom.json")
	if err := os.WriteFile(profilePath, []byte(profileJSON), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	cfg := config.Config{XrayProfilesDir: profilesDir}
	changed, err := UpgradeProfileInbounds(cfg)
	if err != nil {
		t.Fatalf("UpgradeProfileInbounds: %v", err)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1 (legacy inbound must be migrated)", changed)
	}

	out, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read upgraded: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("upgraded profile is not a JSON object: %v", err)
	}

	// Hand-added top-level sections MUST survive the upgrade rewrite.
	for _, key := range []string{"dns", "policy", "stats"} {
		if _, ok := got[key]; !ok {
			t.Errorf("upgrade dropped hand-added top-level %q section:\n%s", key, out)
		}
	}

	// The unknown routing sub-key domainMatcher MUST survive.
	rawRouting, ok := got["routing"]
	if !ok {
		t.Fatalf("upgrade lost the routing section:\n%s", out)
	}
	var routing map[string]json.RawMessage
	if err := json.Unmarshal(rawRouting, &routing); err != nil {
		t.Fatalf("routing is not an object: %v", err)
	}
	if _, ok := routing["domainMatcher"]; !ok {
		t.Errorf("upgrade dropped routing.domainMatcher:\n%s", out)
	}

	// Sanity: the inbound migration actually happened (the owned field was rewritten).
	var xc xrayconf.XrayConfig
	if err := json.Unmarshal(out, &xc); err != nil {
		t.Fatalf("unmarshal upgraded into struct: %v", err)
	}
	if len(xc.Inbounds) == 0 || xc.Inbounds[0].StreamSettings == nil ||
		xc.Inbounds[0].StreamSettings.Sockopt == nil ||
		xc.Inbounds[0].StreamSettings.Sockopt.Tproxy != "tproxy" {
		t.Errorf("inbound was not migrated to sockopt.tproxy:\n%s", out)
	}
}

// TestUpgrade_PreservesOutboundLevelFieldsOnRepair hardens the outbound-repair
// path: when upgrade-config repairs a proxy outbound (legacy base64 pin -> hex),
// outbound-level fields the typed struct does not model (sendThrough, mux) must
// survive — only the outbound's streamSettings is rewritten, not the whole typed
// outbound.
func TestUpgrade_PreservesOutboundLevelFieldsOnRepair(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Current inbound (no inbound migration) + a proxy outbound carrying a legacy
	// base64 pin (triggers the repair) AND hand-added outbound-level fields.
	profileJSON := `{
  "log": {"loglevel": "warning"},
  "inbounds": [{"tag":"transparent","port":12345,"protocol":"dokodemo-door","settings":{"network":"tcp,udp","followRedirect":true},"streamSettings":{"sockopt":{"tproxy":"tproxy"}}}],
  "outbounds": [
    {"tag":"proxy-1","protocol":"vless","sendThrough":"10.0.0.9","mux":{"enabled":true,"concurrency":8},"settings":{},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"pinnedPeerCertSha256":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}},
    {"tag":"direct","protocol":"freedom","settings":{}}
  ],
  "routing": {"domainStrategy":"IPIfNonMatch","rules":[]}
}`
	profilePath := filepath.Join(profilesDir, "custom.json")
	if err := os.WriteFile(profilePath, []byte(profileJSON), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := config.Config{XrayProfilesDir: profilesDir}
	changed, err := UpgradeProfileInbounds(cfg)
	if err != nil {
		t.Fatalf("UpgradeProfileInbounds: %v", err)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1 (pin repair must fire)", changed)
	}

	out, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read upgraded: %v", err)
	}
	// The base64 pin was repaired to hex (proves the outbound was actually rewritten).
	if strings.Contains(string(out), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") {
		t.Errorf("base64 pin not repaired:\n%s", out)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("upgraded profile is not a JSON object: %v", err)
	}
	var obs []map[string]json.RawMessage
	if err := json.Unmarshal(got["outbounds"], &obs); err != nil {
		t.Fatalf("outbounds is not an array: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("no outbounds after upgrade")
	}
	// The hand-added outbound-level fields MUST survive the repair.
	if _, ok := obs[0]["sendThrough"]; !ok {
		t.Errorf("upgrade dropped outbound-level sendThrough:\n%s", out)
	}
	if _, ok := obs[0]["mux"]; !ok {
		t.Errorf("upgrade dropped outbound-level mux:\n%s", out)
	}
}

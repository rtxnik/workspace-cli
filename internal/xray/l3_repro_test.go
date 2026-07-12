package xray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
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

package xray

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

func mkUpgradeTestCfg(t *testing.T) (config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	profilesDir := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.Config{
		XrayProfilesDir: profilesDir,
	}
	return cfg, profilesDir
}

// TestUpgradeInbounds verifies that a legacy profile (no streamSettings on inbound)
// gains sockopt.tproxy after UpgradeProfileInbounds, while outbounds and routing are preserved.
func TestUpgradeInbounds(t *testing.T) {
	cfg, profilesDir := mkUpgradeTestCfg(t)

	// Build a legacy profile: canonical AssembleConfig but strip streamSettings from inbound.
	proxy := xrayconf.Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		Settings: json.RawMessage(`{"vnext":[{"address":"example.com","port":443,"users":[{"id":"uid-1","encryption":"none","flow":"xtls-rprx-vision"}]}]}`),
	}
	xc := xrayconf.AssembleConfig(proxy)
	// Simulate a legacy inbound without streamSettings.
	xc.Inbounds[0].StreamSettings = nil

	data, err := json.MarshalIndent(xc, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	profilePath := filepath.Join(profilesDir, "primary.json")
	if err := os.WriteFile(profilePath, data, 0o600); err != nil {
		t.Fatalf("write legacy profile: %v", err)
	}

	// Run upgrade.
	changed, err := UpgradeProfileInbounds(cfg)
	if err != nil {
		t.Fatalf("UpgradeProfileInbounds: %v", err)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}

	// Read the upgraded profile.
	gotData, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read upgraded: %v", err)
	}
	var got xrayconf.XrayConfig
	if err := json.Unmarshal(gotData, &got); err != nil {
		t.Fatalf("unmarshal upgraded: %v", err)
	}

	// Inbound must now have sockopt.tproxy.
	if len(got.Inbounds) == 0 {
		t.Fatal("no inbounds after upgrade")
	}
	ib := got.Inbounds[0]
	if ib.StreamSettings == nil || ib.StreamSettings.Sockopt == nil || ib.StreamSettings.Sockopt.Tproxy != "tproxy" {
		t.Errorf("inbound missing sockopt.tproxy after upgrade: %+v", ib.StreamSettings)
	}

	// Outbounds and routing must be preserved from the original.
	if len(got.Outbounds) != 2 {
		t.Errorf("outbounds count = %d, want 2", len(got.Outbounds))
	}
	if got.Outbounds[0].Tag != "proxy-1" {
		t.Errorf("first outbound tag = %q, want %q", got.Outbounds[0].Tag, "proxy-1")
	}
	if got.Routing.DomainStrategy != "IPIfNonMatch" {
		t.Errorf("routing.domainStrategy = %q, want %q", got.Routing.DomainStrategy, "IPIfNonMatch")
	}

	// File permissions must be 0600.
	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms = %o, want 600", info.Mode().Perm())
	}
}

// TestUpgradeInboundsAlreadyCurrent verifies that a profile already having sockopt.tproxy
// is counted as unchanged (changed == 0).
func TestUpgradeInboundsAlreadyCurrent(t *testing.T) {
	cfg, profilesDir := mkUpgradeTestCfg(t)

	proxy := xrayconf.Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		Settings: json.RawMessage(`{"vnext":[{"address":"example.com","port":443,"users":[{"id":"uid-2","encryption":"none"}]}]}`),
	}
	xc := xrayconf.AssembleConfig(proxy)
	data, err := json.MarshalIndent(xc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "primary.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	changed, err := UpgradeProfileInbounds(cfg)
	if err != nil {
		t.Fatalf("UpgradeProfileInbounds: %v", err)
	}
	if changed != 0 {
		t.Errorf("changed = %d, want 0 (already current)", changed)
	}
}

// TestUpgradeInboundsNoProxyOutbound verifies that a profile with no recognisable
// proxy outbound is skipped with a warning, not failed.
func TestUpgradeInboundsNoProxyOutbound(t *testing.T) {
	cfg, profilesDir := mkUpgradeTestCfg(t)

	// Profile with only a "direct" outbound — no proxy outbound.
	xc := &xrayconf.XrayConfig{
		Outbounds: []xrayconf.Outbound{
			{Tag: "direct", Protocol: "freedom", Settings: json.RawMessage(`{}`)},
		},
	}
	data, _ := json.MarshalIndent(xc, "", "  ")
	if err := os.WriteFile(filepath.Join(profilesDir, "broken.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Should not error; just skip.
	changed, err := UpgradeProfileInbounds(cfg)
	if err != nil {
		t.Fatalf("UpgradeProfileInbounds returned error for no-proxy profile: %v", err)
	}
	if changed != 0 {
		t.Errorf("changed = %d, want 0", changed)
	}
}

func TestUpgradeInboundsWarnsOnNonCanonicalPort(t *testing.T) {
	cfg, profilesDir := mkUpgradeTestCfg(t)

	proxy := xrayconf.Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		Settings: json.RawMessage(`{"vnext":[{"address":"example.com","port":443,"users":[{"id":"uid-9"}]}]}`),
	}
	xc := xrayconf.AssembleConfig(proxy)
	xc.Inbounds[0].StreamSettings = nil // legacy: triggers an upgrade
	xc.Inbounds[0].Port = 9999          // non-canonical port
	data, err := json.MarshalIndent(xc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "primary.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	changed, err := UpgradeProfileInbounds(cfg)
	if err != nil {
		t.Fatalf("UpgradeProfileInbounds: %v", err)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}
	if !strings.Contains(buf.String(), "normalized to 12345") || !strings.Contains(buf.String(), "9999") {
		t.Errorf("missing port-normalization warning; log = %q", buf.String())
	}

	// The upgraded profile must be canonical: port 12345.
	gotData, _ := os.ReadFile(filepath.Join(profilesDir, "primary.json"))
	var got xrayconf.XrayConfig
	if err := json.Unmarshal(gotData, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Inbounds) == 0 || got.Inbounds[0].Port != 12345 {
		t.Errorf("inbound port = %v, want 12345", got.Inbounds)
	}
}

func TestUpgradeInboundsNoWarnOnCanonicalPort(t *testing.T) {
	cfg, profilesDir := mkUpgradeTestCfg(t)

	proxy := xrayconf.Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		Settings: json.RawMessage(`{"vnext":[{"address":"example.com","port":443,"users":[{"id":"uid-8"}]}]}`),
	}
	xc := xrayconf.AssembleConfig(proxy)
	xc.Inbounds[0].StreamSettings = nil // legacy: triggers an upgrade, port already 12345
	data, _ := json.MarshalIndent(xc, "", "  ")
	if err := os.WriteFile(filepath.Join(profilesDir, "primary.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if _, err := UpgradeProfileInbounds(cfg); err != nil {
		t.Fatalf("UpgradeProfileInbounds: %v", err)
	}
	if strings.Contains(buf.String(), "normalized to 12345") {
		t.Errorf("unexpected normalization warning for canonical port; log = %q", buf.String())
	}
}

func writeProfile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// currentInbound is a complete inbound block that already carries
// sockopt.tproxy, so the inbound migration is a no-op and only the outbound
// repair should fire. This is the regression for the restructure.
const currentInbound = `"inbounds":[{"tag":"transparent","port":12345,"protocol":"dokodemo-door","settings":{"network":"tcp,udp","followRedirect":true},"streamSettings":{"sockopt":{"tproxy":"tproxy"}}}]`

func TestUpgradeProfile_RepairsOutboundWithCurrentInbound(t *testing.T) {
	dir := t.TempDir()
	body := `{"log":{"loglevel":"warning"},` + currentInbound + `,"outbounds":[` +
		`{"tag":"proxy-1","protocol":"hysteria","settings":{"version":2,"address":"h.example","port":443},` +
		`"streamSettings":{"network":"hysteria","security":"tls","hysteriaSettings":{"version":2,"auth":"pw"},` +
		`"tlsSettings":{"serverName":"h.example","alpn":["h3"],"fingerprint":"chrome","pinnedPeerCertSha256":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}},` +
		`{"tag":"direct","protocol":"freedom","settings":{}}]}`
	p := writeProfile(t, dir, "primary.json", body)

	changed, err := upgradeProfile(p)
	if err != nil {
		t.Fatalf("upgradeProfile: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true (legacy base64 pin outbound)")
	}

	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") {
		t.Errorf("base64 pin still present after repair:\n%s", data)
	}
	if !strings.Contains(string(data), "0000000000000000000000000000000000000000000000000000000000000000") {
		t.Errorf("hex pin missing after repair:\n%s", data)
	}
}

func TestUpgradeProfile_Idempotent(t *testing.T) {
	dir := t.TempDir()
	body := `{"log":{"loglevel":"warning"},` + currentInbound + `,"outbounds":[` +
		`{"tag":"proxy-1","protocol":"hysteria","settings":{},"streamSettings":{"network":"hysteria","security":"tls",` +
		`"tlsSettings":{"pinnedPeerCertSha256":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}},` +
		`{"tag":"direct","protocol":"freedom","settings":{}}]}`
	p := writeProfile(t, dir, "primary.json", body)

	if _, err := upgradeProfile(p); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	changed, err := upgradeProfile(p)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if changed {
		t.Error("second pass must report no change (idempotent)")
	}
}

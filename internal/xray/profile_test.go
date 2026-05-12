package xray

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/vless"
)

// mkTestCfg returns a config.Config rooted in t.TempDir() with profiles dir
// pre-created and XrayConfig pointing at a not-yet-existing symlink path.
func mkTestCfg(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	return config.Config{XrayConfig: cfgPath, XrayProfilesDir: profilesDir}
}

func TestProfileAdd(t *testing.T) {
	cfg := mkTestCfg(t)
	uri := "vless://12345678-1234-1234-1234-123456789012@example.com:443?type=tcp&security=tls&sni=example.com#test"
	if err := AddProfile(cfg, "primary", uri, false); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	// File exists and is parseable VLESS xray config.
	target := filepath.Join(cfg.XrayProfilesDir, "primary.json")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	var xc vless.XrayConfig
	if err := json.Unmarshal(data, &xc); err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	if len(xc.Outbounds) == 0 || xc.Outbounds[0].Protocol != "vless" {
		t.Fatalf("expected first outbound = vless, got %+v", xc.Outbounds)
	}

	// Refuse overwrite without --force.
	if err := AddProfile(cfg, "primary", uri, false); err == nil {
		t.Fatal("expected overwrite refusal without --force")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error; got %v", err)
	}

	// Overwrite with --force succeeds.
	if err := AddProfile(cfg, "primary", uri, true); err != nil {
		t.Fatalf("AddProfile --force: %v", err)
	}

	// Reserved name rejected.
	if err := AddProfile(cfg, "config", uri, false); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("expected reserved-name error; got %v", err)
	}

	// Invalid URI rejected.
	if err := AddProfile(cfg, "p2", "not-a-vless-uri", false); err == nil {
		t.Error("expected URI parse error")
	}

	// Invalid name regex (capital letter) rejected.
	if err := AddProfile(cfg, "Foo", uri, false); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Errorf("expected regex-fail error; got %v", err)
	}
}

func TestProfileAddCopiesRouting(t *testing.T) {
	// Verifies D-05 routing-copy: AddProfile must lift Routing from the
	// currently-active profile rather than emit the default GenerateConfig
	// routing.
	cfg := mkTestCfg(t)
	uri := "vless://12345678-1234-1234-1234-123456789012@host.example:443?type=tcp&security=tls&sni=host.example#one"
	if err := AddProfile(cfg, "primary", uri, false); err != nil {
		t.Fatalf("seed primary: %v", err)
	}

	// Mutate primary.json's Routing to a sentinel set of rules so we can tell
	// whether the next AddProfile lifted it.
	primaryPath := filepath.Join(cfg.XrayProfilesDir, "primary.json")
	pdata, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	var primary vless.XrayConfig
	if err := json.Unmarshal(pdata, &primary); err != nil {
		t.Fatalf("parse primary: %v", err)
	}
	primary.Routing.Rules = append(primary.Routing.Rules, vless.Rule{
		Type:        "field",
		IP:          []string{"203.0.113.7/32"}, // TEST-NET-3 sentinel
		OutboundTag: "direct",
	})
	new, err := json.MarshalIndent(&primary, "", "  ")
	if err != nil {
		t.Fatalf("marshal primary: %v", err)
	}
	if err := os.WriteFile(primaryPath, new, 0o644); err != nil {
		t.Fatalf("write primary: %v", err)
	}

	// Symlink config.json → primary.json to mark primary as the active profile.
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	uri2 := "vless://87654321-1234-1234-1234-210987654321@host2.example:8443?type=tcp&security=tls&sni=host2.example#two"
	if err := AddProfile(cfg, "backup", uri2, false); err != nil {
		t.Fatalf("AddProfile backup: %v", err)
	}

	backupPath := filepath.Join(cfg.XrayProfilesDir, "backup.json")
	bdata, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	var backup vless.XrayConfig
	if err := json.Unmarshal(bdata, &backup); err != nil {
		t.Fatalf("parse backup: %v", err)
	}

	var found bool
	for _, r := range backup.Routing.Rules {
		for _, ip := range r.IP {
			if ip == "203.0.113.7/32" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("D-05 routing-copy: backup.json missing 203.0.113.7/32 rule from active primary; rules=%+v", backup.Routing.Rules)
	}
}

func TestListProfiles(t *testing.T) {
	cfg := mkTestCfg(t)
	uri := "vless://12345678-1234-1234-1234-123456789012@host1.example:443?type=tcp&security=tls&sni=host1.example#one"
	if err := AddProfile(cfg, "primary", uri, false); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	uri2 := "vless://87654321-1234-1234-1234-210987654321@host2.example:8443?type=xhttp&security=reality&sni=ozon.ru&pbk=key&sid=abc&spx=/x#two"
	if err := AddProfile(cfg, "backup", uri2, false); err != nil {
		t.Fatalf("seed backup: %v", err)
	}

	// Mark primary as active.
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := ListProfiles(cfg)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 profiles, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "backup" || got[1].Name != "primary" {
		t.Errorf("want sorted [backup, primary]; got [%s, %s]", got[0].Name, got[1].Name)
	}
	// Active flag flips on for primary, off for backup.
	if got[0].Active || !got[1].Active {
		t.Errorf("active flags wrong: backup.Active=%v primary.Active=%v", got[0].Active, got[1].Active)
	}
	// UUID is always masked in list output (D-13).
	if got[1].UUIDMasked != "12345678-****-****-****-************" {
		t.Errorf("list must mask UUID; got %q", got[1].UUIDMasked)
	}
	// Transport + Security + SNI surfaced.
	if got[0].Transport != "xhttp" || got[0].Security != "reality" || got[0].SNI != "ozon.ru" {
		t.Errorf("backup summary fields wrong: %+v", got[0])
	}
	if got[1].Transport != "tcp" || got[1].Security != "tls" || got[1].SNI != "host1.example" {
		t.Errorf("primary summary fields wrong: %+v", got[1])
	}
}

func TestListProfilesSkipsBadJSON(t *testing.T) {
	// Bad-JSON file must NOT cause ListProfiles to error; output.Warn (stderr)
	// suppresses it instead.
	cfg := mkTestCfg(t)
	uri := "vless://12345678-1234-1234-1234-123456789012@host.example:443?type=tcp&security=tls&sni=host.example#x"
	if err := AddProfile(cfg, "primary", uri, false); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.XrayProfilesDir, "broken.json"), []byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("seed broken: %v", err)
	}

	got, err := ListProfiles(cfg)
	if err != nil {
		t.Fatalf("ListProfiles should not error on a single bad file; got %v", err)
	}
	if len(got) != 1 || got[0].Name != "primary" {
		t.Errorf("want 1 good profile (primary); got %+v", got)
	}
}

func TestReadActiveProfileName(t *testing.T) {
	cfg := mkTestCfg(t)
	// No symlink → ErrNotExist.
	if _, err := ReadActiveProfileName(cfg); !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist; got %v", err)
	}
	// Seed primary + create symlink manually.
	uri := "vless://12345678-1234-1234-1234-123456789012@host:443?type=tcp&security=tls#x"
	if err := AddProfile(cfg, "primary", uri, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	name, err := ReadActiveProfileName(cfg)
	if err != nil {
		t.Fatalf("ReadActiveProfileName: %v", err)
	}
	if name != "primary" {
		t.Errorf("want primary; got %q", name)
	}

	// Regular file (not a symlink) → error mentioning "not a symlink".
	cfg2 := mkTestCfg(t)
	if err := os.WriteFile(cfg2.XrayConfig, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}
	_, err = ReadActiveProfileName(cfg2)
	if err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("want 'not a symlink' error; got %v", err)
	}
}

func TestRemoveProfile(t *testing.T) {
	cfg := mkTestCfg(t)
	uri := "vless://12345678-1234-1234-1234-123456789012@host:443?type=tcp&security=tls#x"
	if err := AddProfile(cfg, "primary", uri, false); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := AddProfile(cfg, "backup", uri, false); err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	// Make primary the active profile via direct symlink (no docker dependency).
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	// Happy path: remove inactive profile → file deleted, no error.
	if err := RemoveProfile(cfg, "backup"); err != nil {
		t.Errorf("RemoveProfile(backup): %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.XrayProfilesDir, "backup.json")); !os.IsNotExist(err) {
		t.Errorf("backup.json not deleted: %v", err)
	}

	// Refuse-active: try to remove currently-symlinked profile → error contains
	// "cannot remove active profile" AND the active profile file is NOT
	// deleted (T-22-active-delete).
	if err := RemoveProfile(cfg, "primary"); err == nil || !strings.Contains(err.Error(), "cannot remove active profile") {
		t.Errorf("expected refusal of active profile removal; got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.XrayProfilesDir, "primary.json")); err != nil {
		t.Errorf("primary.json should still exist after refused removal: %v", err)
	}

	// Reserved name rejected BEFORE any filesystem op (T-22-rm-injection).
	if err := RemoveProfile(cfg, "config"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("expected reserved-name error; got: %v", err)
	}

	// Nonexistent profile name → wrapped os.ErrNotExist sentinel
	// (Plan body says "wrapped os.IsNotExist"; errors.Is walks the wrap chain
	// whereas the legacy os.IsNotExist predicate does not).
	err := RemoveProfile(cfg, "nope")
	if err == nil {
		t.Fatal("expected error on nonexistent profile")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist error; got: %v", err)
	}

	// Invalid name (capital letter) → regex-fail error; no FS op attempted.
	// Pre-create a file named "Foo.json" so we can prove the validator runs
	// before any os.Remove call: a stray file at that exact path survives.
	stray := filepath.Join(cfg.XrayProfilesDir, "Foo.json")
	if err := os.WriteFile(stray, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed stray: %v", err)
	}
	if err := RemoveProfile(cfg, "Foo"); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Errorf("expected regex-fail error; got: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("stray Foo.json should be untouched after validator-fail; got: %v", err)
	}
}

func TestMaskCredentials(t *testing.T) {
	cfg := mkTestCfg(t)
	uri := "vless://abcd1234-5678-90ab-cdef-1234567890ab@host:443?type=tcp&security=tls&sni=host#x"
	if err := AddProfile(cfg, "primary", uri, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dp, err := LoadProfile(cfg, "primary")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if dp.UUID != "abcd1234-5678-90ab-cdef-1234567890ab" {
		t.Errorf("raw UUID surfaced via LoadProfile incorrect: %q", dp.UUID)
	}
	if got := MaskUUID(dp.UUID); got != "abcd1234-****-****-****-************" {
		t.Errorf("MaskUUID = %q; want abcd1234-****-****-****-************", got)
	}
}

package xray

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// TestMain defaults the D5-01 add-time validation seams to a hermetic
// online+pass mode for the whole package test run, so no test touches real
// Docker via AddProfile. Individual tests override with defer-restore.
// Integration tests (//go:build integration) drive real Docker through the
// docker package directly, not these seams, so stubbing here is harmless to
// their assertions.
func TestMain(m *testing.M) {
	verifyProxyReadyFn = func(config.Config) error { return nil }
	validateAtPathFn = func(config.Config, string) error { return nil }
	os.Exit(m.Run())
}

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
	var xc xrayconf.XrayConfig
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
	var primary xrayconf.XrayConfig
	if err := json.Unmarshal(pdata, &primary); err != nil {
		t.Fatalf("parse primary: %v", err)
	}
	primary.Routing.Rules = append(primary.Routing.Rules,
		// TEST-NET-3 sentinel — survives the unmarshal->marshal round-trip.
		json.RawMessage(`{"type":"field","ip":["203.0.113.7/32"],"outboundTag":"direct"}`),
	)
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
	var backup xrayconf.XrayConfig
	if err := json.Unmarshal(bdata, &backup); err != nil {
		t.Fatalf("parse backup: %v", err)
	}

	var found bool
	for _, r := range backup.Routing.Rules {
		if bytes.Contains(r, []byte(`"203.0.113.7/32"`)) {
			found = true
			break
		}
	}
	if !found {
		// Render rules as a single string for the failure message.
		var dump []string
		for _, r := range backup.Routing.Rules {
			dump = append(dump, string(r))
		}
		t.Errorf("D-05 routing-copy: backup.json missing 203.0.113.7/32 rule from active primary; rules=[%s]", strings.Join(dump, ", "))
	}
}

// TestAddProfilePreservesPortRule is the regression for the prod bug that
// motivated the D-05 routing-copy fix: a routing rule with a `port` field
// (e.g. `{"type":"field","port":"22","outboundTag":"direct"}`) must survive
// AddProfile's unmarshal -> marshal round-trip and appear verbatim in the
// derived profile. Pre-refactor the typed routing-rule struct in the vless
// package dropped the `port` field on the way through.
func TestAddProfilePreservesPortRule(t *testing.T) {
	cfg := mkTestCfg(t)
	uri1 := "vless://12345678-1234-1234-1234-123456789012@host.example:443?type=tcp&security=tls&sni=host.example#one"
	if err := AddProfile(cfg, "primary", uri1, false); err != nil {
		t.Fatalf("seed primary: %v", err)
	}

	// Read primary.json, prepend a port-bearing rule, write back.
	primaryPath := filepath.Join(cfg.XrayProfilesDir, "primary.json")
	pdata, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	var primary xrayconf.XrayConfig
	if err := json.Unmarshal(pdata, &primary); err != nil {
		t.Fatalf("parse primary: %v", err)
	}
	portRule := json.RawMessage(`{"type":"field","port":"22","outboundTag":"direct"}`)
	primary.Routing.Rules = append([]json.RawMessage{portRule}, primary.Routing.Rules...)
	newBytes, err := json.MarshalIndent(&primary, "", "  ")
	if err != nil {
		t.Fatalf("marshal primary: %v", err)
	}
	if err := os.WriteFile(primaryPath, newBytes, 0o644); err != nil {
		t.Fatalf("write primary: %v", err)
	}

	// Symlink config.json -> primary.json to mark primary as active.
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfg.XrayConfig); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	uri2 := "vless://87654321-1234-1234-1234-210987654321@host2.example:8443?type=tcp&security=tls&sni=host2.example#two"
	if err := AddProfile(cfg, "secondary", uri2, false); err != nil {
		t.Fatalf("AddProfile secondary: %v", err)
	}

	secondaryPath := filepath.Join(cfg.XrayProfilesDir, "secondary.json")
	sdata, err := os.ReadFile(secondaryPath)
	if err != nil {
		t.Fatalf("read secondary: %v", err)
	}
	var secondary xrayconf.XrayConfig
	if err := json.Unmarshal(sdata, &secondary); err != nil {
		t.Fatalf("parse secondary: %v", err)
	}

	if len(secondary.Routing.Rules) == 0 {
		t.Fatalf("secondary routing.rules empty; expected port-bearing rule at index 0")
	}
	// Compact the raw bytes so the substring check is whitespace-insensitive
	// (MarshalIndent re-indents RawMessage contents in the on-disk file).
	var compact bytes.Buffer
	if err := json.Compact(&compact, secondary.Routing.Rules[0]); err != nil {
		t.Fatalf("compact rule[0]: %v", err)
	}
	first := compact.Bytes()
	if !bytes.Contains(first, []byte(`"port":"22"`)) {
		t.Errorf("port field dropped on AddProfile round-trip; rule[0]=%s", string(first))
	}
	if !bytes.Contains(first, []byte(`"outboundTag":"direct"`)) {
		t.Errorf("outboundTag field missing on AddProfile round-trip; rule[0]=%s", string(first))
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

func TestAddProfileHysteria2(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{XrayProfilesDir: filepath.Join(dir, "profiles"), XrayConfig: filepath.Join(dir, "config.json")}
	uri := "hysteria2://AUTHREDACTED@example.com:443?alpn=h3&fp=chrome&obfs=salamander&obfs-password=OBFSREDACTED&security=tls&sni=example.com#hy2"
	if err := AddProfile(cfg, "hy2", uri, false); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.XrayProfilesDir, "hy2.json"))
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if !strings.Contains(string(data), `"protocol": "hysteria"`) {
		t.Errorf("profile JSON missing hysteria outbound:\n%s", data)
	}
}

func TestSummarizeProfileHysteria2(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{XrayProfilesDir: filepath.Join(dir, "profiles"), XrayConfig: filepath.Join(dir, "config.json")}
	uri := "hy2://pw@example.com:443?sni=example.com"
	if err := AddProfile(cfg, "hy2", uri, false); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	got, err := ListProfiles(cfg)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("profiles = %d, want 1", len(got))
	}
	p := got[0]
	if p.Transport != "hysteria" || p.Address != "example.com" || p.Port != 443 {
		t.Errorf("summary = %+v", p)
	}
	if p.UUIDMasked != "" {
		t.Errorf("hy2 UUID column = %q, want empty", p.UUIDMasked)
	}
}

// TestListProfilesPerms verifies that ListProfiles creates the profiles dir at
// 0700 (not 0755) when it does not yet exist. A user who runs `ws proxy list`
// before ever running `ws proxy profile add` must not end up with a
// group/world-readable xray tree.
func TestListProfilesPerms(t *testing.T) {
	dir := t.TempDir()
	// XrayProfilesDir must NOT exist yet — ListProfiles must create it.
	cfg := config.Config{
		XrayConfig:      filepath.Join(dir, "config.json"),
		XrayProfilesDir: filepath.Join(dir, "profiles"),
	}
	if _, err := os.Stat(cfg.XrayProfilesDir); err == nil {
		t.Fatal("pre-condition failed: profiles dir must not exist before calling ListProfiles")
	}
	if _, err := ListProfiles(cfg); err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	di, err := os.Stat(cfg.XrayProfilesDir)
	if err != nil {
		t.Fatalf("stat profiles dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("ListProfiles created profiles dir with perm %o, want 700", di.Mode().Perm())
	}
}

func TestAddProfilePerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XRAY_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("XRAY_PROFILES_DIR", filepath.Join(dir, "profiles"))
	cfg := config.Load()
	if err := AddProfile(cfg, "p1", "hy2://pw@h.example:443", false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(cfg.XrayProfilesDir, "p1.json"))
	if err != nil {
		t.Fatalf("stat profile file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("profile perm = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(cfg.XrayProfilesDir)
	if err != nil {
		t.Fatalf("stat profiles dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir perm = %o, want 700", di.Mode().Perm())
	}
}

// D5-01: a good force-add stages, validates, and commits under the final name;
// the in-container validation path is the sibling .tmp staging file.
func TestProfileAdd_ValidatesAndCommitsOnPass(t *testing.T) {
	cfg := mkTestCfg(t)
	origVerify, origValidate := verifyProxyReadyFn, validateAtPathFn
	defer func() { verifyProxyReadyFn, validateAtPathFn = origVerify, origValidate }()

	var gotContainerPath string
	verifyProxyReadyFn = func(config.Config) error { return nil }
	validateAtPathFn = func(_ config.Config, p string) error { gotContainerPath = p; return nil }

	uri := "vless://12345678-1234-1234-1234-123456789012@example.com:443?type=tcp&security=tls&sni=example.com#ok"
	if err := AddProfile(cfg, "committed", uri, false); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.XrayProfilesDir, "committed.json")); err != nil {
		t.Fatalf("expected committed profile: %v", err)
	}
	if !strings.HasPrefix(gotContainerPath, "/etc/xray/.committed.add-validating.") ||
		!strings.HasSuffix(gotContainerPath, ".json") {
		t.Errorf("unexpected in-container validation path: %q", gotContainerPath)
	}
	// No staging debris (stage lives at the bind root, not under profiles/).
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.XrayConfig), ".committed.add-validating.*.json"))
	if len(leftovers) != 0 {
		t.Errorf("staging file(s) not cleaned up: %v", leftovers)
	}
}

// D5-01 core invariant: a --force add whose validation FAILS must leave the
// pre-existing profile byte-for-byte unchanged (no clobber) and remove staging.
func TestProfileAdd_ForceRejectPreservesExisting(t *testing.T) {
	cfg := mkTestCfg(t)

	goodURI := "vless://12345678-1234-1234-1234-123456789012@example.com:443?type=tcp&security=tls&sni=example.com#good"
	if err := AddProfile(cfg, "primary", goodURI, false); err != nil {
		t.Fatalf("seed AddProfile: %v", err)
	}
	target := filepath.Join(cfg.XrayProfilesDir, "primary.json")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read seeded profile: %v", err)
	}

	origVerify, origValidate := verifyProxyReadyFn, validateAtPathFn
	defer func() { verifyProxyReadyFn, validateAtPathFn = origVerify, origValidate }()
	verifyProxyReadyFn = func(config.Config) error { return nil } // online
	validateAtPathFn = func(config.Config, string) error { return errors.New("xray: bad config") }

	otherURI := "vless://12345678-1234-1234-1234-123456789012@example.com:8443?type=tcp&security=tls&sni=example.com#other"
	if err := AddProfile(cfg, "primary", otherURI, true); err == nil {
		t.Fatal("expected --force AddProfile to fail when xray -test rejects the config")
	} else if !strings.Contains(err.Error(), "xray -test") {
		t.Errorf("expected xray -test rejection in error; got %v", err)
	}

	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read profile after failed force-add: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("force-add whose validation failed must NOT modify the existing profile")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.XrayConfig), ".primary.add-validating.*.json"))
	if len(leftovers) != 0 {
		t.Errorf("staging file(s) not cleaned up: %v", leftovers)
	}
}

// D5-01: a NEW add whose validation FAILS must not create the profile file.
func TestProfileAdd_RejectNoFileOnNewAdd(t *testing.T) {
	cfg := mkTestCfg(t)
	origVerify, origValidate := verifyProxyReadyFn, validateAtPathFn
	defer func() { verifyProxyReadyFn, validateAtPathFn = origVerify, origValidate }()
	verifyProxyReadyFn = func(config.Config) error { return nil }
	validateAtPathFn = func(config.Config, string) error { return errors.New("xray: bad config") }

	uri := "vless://12345678-1234-1234-1234-123456789012@example.com:443?type=tcp&security=tls&sni=example.com#new"
	if err := AddProfile(cfg, "brandnew", uri, false); err == nil {
		t.Fatal("expected new AddProfile to fail when xray -test rejects the config")
	}
	if _, err := os.Stat(filepath.Join(cfg.XrayProfilesDir, "brandnew.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a rejected new profile must not be written; stat err=%v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.XrayConfig), ".brandnew.add-validating.*.json"))
	if len(leftovers) != 0 {
		t.Errorf("staging file(s) not cleaned up: %v", leftovers)
	}
}

// D5-01: when the proxy is unreachable, validation is inconclusive — write the
// profile unvalidated (advisory) and do NOT call the validator.
func TestProfileAdd_OfflineWritesUnvalidated(t *testing.T) {
	cfg := mkTestCfg(t)
	origVerify, origValidate := verifyProxyReadyFn, validateAtPathFn
	defer func() { verifyProxyReadyFn, validateAtPathFn = origVerify, origValidate }()
	verifyProxyReadyFn = func(config.Config) error { return errors.New("proxy not running") }
	validateCalled := false
	validateAtPathFn = func(config.Config, string) error { validateCalled = true; return nil }

	uri := "vless://12345678-1234-1234-1234-123456789012@example.com:443?type=tcp&security=tls&sni=example.com#off"
	if err := AddProfile(cfg, "offline", uri, false); err != nil {
		t.Fatalf("offline AddProfile should still write: %v", err)
	}
	if validateCalled {
		t.Error("validateAtPathFn must NOT be called when the proxy is unreachable")
	}
	if _, err := os.Stat(filepath.Join(cfg.XrayProfilesDir, "offline.json")); err != nil {
		t.Errorf("offline add must write the profile unvalidated; stat err=%v", err)
	}
}

// D5-01: the hidden .json staging file at the bind root is invisible to
// ListProfiles' profiles/*.json glob. Observed at validation time, when the
// staging file is on disk.
func TestProfileAdd_StagingInvisibleToList(t *testing.T) {
	cfg := mkTestCfg(t)
	origVerify, origValidate := verifyProxyReadyFn, validateAtPathFn
	defer func() { verifyProxyReadyFn, validateAtPathFn = origVerify, origValidate }()

	var duringValidation []ProfileSummary
	var stagePresent bool
	verifyProxyReadyFn = func(config.Config) error { return nil }
	validateAtPathFn = func(config.Config, string) error {
		// The staging .json is on disk at the bind root right now.
		staged, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.XrayConfig), ".stg.add-validating.*.json"))
		stagePresent = len(staged) == 1
		duringValidation, _ = ListProfiles(cfg)
		return nil
	}

	uri := "vless://12345678-1234-1234-1234-123456789012@example.com:443?type=tcp&security=tls&sni=example.com#stg"
	if err := AddProfile(cfg, "stg", uri, false); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	if !stagePresent {
		t.Fatal("expected exactly one staging .json at the bind root during validation")
	}
	for _, p := range duringValidation {
		if strings.Contains(p.Name, "add-validating") {
			t.Errorf("staging file leaked into ListProfiles: %q", p.Name)
		}
	}
}

// TestListProfilesMaskingByteLevel is the security regression guard for the
// unified list path: the masked ProfileSummary feed (ListProfiles / Summary)
// must never carry a raw credential — proven byte-for-byte on the marshaled
// output — while the raw ListProfilesDetailed feed stays the unmasked superset.
// Fixtures are written as raw xray JSON so the exact secret bytes are under
// test control (matches show_test.go's raw-profile convention).
func TestListProfilesMaskingByteLevel(t *testing.T) {
	cfg := mkTestCfg(t)

	const (
		vlessUUID     = "11111111-2222-3333-4444-555555555555"
		vlessUUIDTail = "2222-3333-4444-555555555555" // masked tail that must never leak
		hy2Auth       = "SECRET-AUTH"
		hy2Obfs       = "SECRET-OBFS"
	)

	vlessProfile := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[
		{"tag":"proxy-1","protocol":"vless",
		 "settings":{"vnext":[{"address":"vless.example","port":443,
		   "users":[{"id":"` + vlessUUID + `"}]}]},
		 "streamSettings":{"network":"tcp","security":"tls",
		   "tlsSettings":{"serverName":"vless.example"}}},
		{"tag":"direct","protocol":"freedom","settings":{}}],
		"routing":{"domainStrategy":"IPIfNonMatch","rules":[]}}`
	if err := os.WriteFile(filepath.Join(cfg.XrayProfilesDir, "vlessp.json"), []byte(vlessProfile), 0o600); err != nil {
		t.Fatalf("write vless profile: %v", err)
	}

	hy2Profile := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[
		{"tag":"proxy-1","protocol":"hysteria","settings":{"version":2,"address":"hy2.example","port":443},
		 "streamSettings":{"network":"hysteria","security":"tls",
		   "tlsSettings":{"serverName":"hy2.example","allowInsecure":false},
		   "hysteriaSettings":{"version":2,"auth":"` + hy2Auth + `"},
		   "finalmask":{"udp":[{"type":"salamander","settings":{"password":"` + hy2Obfs + `"}}]}}},
		{"tag":"direct","protocol":"freedom","settings":{}}],
		"routing":{"domainStrategy":"IPIfNonMatch","rules":[]}}`
	if err := os.WriteFile(filepath.Join(cfg.XrayProfilesDir, "hy2p.json"), []byte(hy2Profile), 0o600); err != nil {
		t.Fatalf("write hy2 profile: %v", err)
	}

	// --- masked feed: ListProfiles masks the UUID and omits every secret ---
	summaries, err := ListProfiles(cfg)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	byName := make(map[string]ProfileSummary, len(summaries))
	for _, s := range summaries {
		byName[s.Name] = s
	}
	if got := byName["vlessp"].UUIDMasked; got != "11111111-****-****-****-************" {
		t.Errorf("vless UUIDMasked = %q; want 11111111-****-****-****-************", got)
	}
	if got := byName["hy2p"].UUIDMasked; got != "" {
		t.Errorf("hy2 UUIDMasked = %q; want empty", got)
	}

	blob, err := json.Marshal(summaries)
	if err != nil {
		t.Fatalf("marshal summaries: %v", err)
	}
	for _, secret := range []string{vlessUUIDTail, hy2Auth, hy2Obfs} {
		if bytes.Contains(blob, []byte(secret)) {
			t.Errorf("masked list output leaked secret %q:\n%s", secret, blob)
		}
	}

	// --- raw feed: ListProfilesDetailed IS the unmasked superset, yet its
	//     Summary() projection must still never leak a secret ---
	details, err := ListProfilesDetailed(cfg)
	if err != nil {
		t.Fatalf("ListProfilesDetailed: %v", err)
	}
	rawByName := make(map[string]DetailedProfile, len(details))
	for _, dp := range details {
		rawByName[dp.Name] = dp
	}
	if got := rawByName["vlessp"].UUID; got != vlessUUID {
		t.Errorf("detailed vless UUID = %q; want raw %q", got, vlessUUID)
	}
	if got := rawByName["hy2p"]; got.Auth != hy2Auth || got.ObfsPassword != hy2Obfs {
		t.Errorf("detailed hy2 secrets = %q/%q; want %q/%q", got.Auth, got.ObfsPassword, hy2Auth, hy2Obfs)
	}

	projected := make([]ProfileSummary, 0, len(details))
	for _, dp := range details {
		projected = append(projected, dp.Summary())
	}
	pblob, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("marshal projected summaries: %v", err)
	}
	for _, secret := range []string{vlessUUIDTail, hy2Auth, hy2Obfs} {
		if bytes.Contains(pblob, []byte(secret)) {
			t.Errorf("Summary() projection leaked secret %q:\n%s", secret, pblob)
		}
	}
}

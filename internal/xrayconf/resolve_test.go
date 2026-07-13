package xrayconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// seedProfileSymlink lays out a D-07 layout under root: profiles/<name>.json is
// a regular file and config.json is a relative symlink to it. Returns the link
// path, the profiles dir, and the resolved profile file path.
func seedProfileSymlink(t *testing.T, root, name, body string) (link, profilesDir, profileFile string) {
	t.Helper()
	profilesDir = filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	profileFile = filepath.Join(profilesDir, name+".json")
	if err := os.WriteFile(profileFile, []byte(body), 0o600); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	link = filepath.Join(root, "config.json")
	if err := os.Symlink(filepath.Join("profiles", name+".json"), link); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	return link, profilesDir, profileFile
}

func TestResolveConfigTarget_FollowsSymlinkWithinRoot(t *testing.T) {
	root := t.TempDir()
	link, profilesDir, profileFile := seedProfileSymlink(t, root, "primary", `{"log":{}}`)
	roots := []string{root, profilesDir}

	got, existed, err := ResolveConfigTarget(link, roots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !existed {
		t.Errorf("existed = false, want true")
	}
	if want, _ := filepath.EvalSymlinks(profileFile); got != want {
		t.Errorf("resolved = %q, want %q", got, want)
	}
}

func TestResolveConfigTarget_RefusesEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.json")
	if err := os.WriteFile(victim, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	link := filepath.Join(root, "config.json")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	roots := []string{root, filepath.Join(root, "profiles")}

	if _, _, err := ResolveConfigTarget(link, roots); err == nil {
		t.Fatal("expected a containment refusal for a symlink escaping the roots")
	}
}

func TestResolveConfigTarget_CustomProfilesDirAccepted(t *testing.T) {
	root := t.TempDir()
	customProfiles := t.TempDir() // an independently-configured XRAY_PROFILES_DIR
	profileFile := filepath.Join(customProfiles, "primary.json")
	if err := os.WriteFile(profileFile, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	link := filepath.Join(root, "config.json")
	if err := os.Symlink(profileFile, link); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	roots := []string{root, customProfiles} // profiles dir outside Dir(config.json)

	got, _, err := ResolveConfigTarget(link, roots)
	if err != nil {
		t.Fatalf("a symlink into a custom profiles dir must be accepted, got: %v", err)
	}
	if want, _ := filepath.EvalSymlinks(profileFile); got != want {
		t.Errorf("resolved = %q, want %q", got, want)
	}
}

func TestResolveConfigTarget_ColdInitRebuildsUnderAncestor(t *testing.T) {
	root := t.TempDir() // exists; config.json does not
	path := filepath.Join(root, "config.json")
	roots := []string{root, filepath.Join(root, "profiles")}

	got, existed, err := ResolveConfigTarget(path, roots)
	if err != nil {
		t.Fatalf("cold init must be creatable, got: %v", err)
	}
	if existed {
		t.Errorf("existed = true, want false")
	}
	if want, _ := filepath.EvalSymlinks(root); got != filepath.Join(want, "config.json") {
		t.Errorf("resolved = %q, want %q", got, filepath.Join(want, "config.json"))
	}
}

func TestResolveConfigTarget_RegularFileResolvesToItself(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	roots := []string{root, filepath.Join(root, "profiles")}

	got, existed, err := ResolveConfigTarget(path, roots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !existed {
		t.Errorf("existed = false, want true")
	}
	if want, _ := filepath.EvalSymlinks(path); got != want {
		t.Errorf("resolved = %q, want %q", got, want)
	}
}

func TestResolveConfigTarget_DanglingSymlinkErrors(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "config.json")
	if err := os.Symlink(filepath.Join("profiles", "gone.json"), link); err != nil {
		t.Fatalf("seed dangling symlink: %v", err)
	}
	roots := []string{root, filepath.Join(root, "profiles")}

	if _, _, err := ResolveConfigTarget(link, roots); err == nil {
		t.Fatal("expected an error for a dangling symlink")
	}
}

func TestWithin_HandlesFilesystemRoot(t *testing.T) {
	// filepath.Rel-based containment must not choke when a root is "/"
	// (a naive root+separator prefix would form "//" and reject everything).
	if !within("/config.json", "/") {
		t.Error(`within("/config.json", "/") = false, want true`)
	}
	if within("/etc/evil.json", "/home/u/.config/xray") {
		t.Error("an outside path must not be contained")
	}
}

// TestResolveThenWritePreservesSymlink is the integration guard for the fix:
// resolving config.json (a symlink) and writing the RESOLVED target keeps the
// symlink intact with an unchanged link target — init no longer clobbers it.
func TestResolveThenWritePreservesSymlink(t *testing.T) {
	root := t.TempDir()
	link, profilesDir, _ := seedProfileSymlink(t, root, "primary",
		`{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[],"routing":{"rules":[]}}`)
	roots := []string{root, profilesDir}

	target, _, err := ResolveConfigTarget(link, roots)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	xc := AssembleConfig(Outbound{Tag: "proxy-1", Protocol: "vless", Settings: json.RawMessage(`{}`)})
	if err := WriteConfig(target, xc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.json is no longer a symlink; active-profile pointer destroyed")
	}
	if tgt, _ := os.Readlink(link); tgt != filepath.Join("profiles", "primary.json") {
		t.Errorf("symlink target changed to %q", tgt)
	}
	// The write landed on the resolved profile file and is valid JSON.
	data, err := os.ReadFile(link) // read THROUGH the link
	if err != nil {
		t.Fatalf("read through link: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatalf("written config is not valid JSON: %v", err)
	}
}

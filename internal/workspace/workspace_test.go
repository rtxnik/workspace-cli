package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// TestCreateRemovesWhatItCreated: a step of Create that fails after the
// workspace directory was made removes the directory again and returns the
// step's error unchanged; a directory that was there before is left alone.
func TestCreateRemovesWhatItCreated(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		WorkspacesDir: filepath.Join(root, "workspaces"),
		ProfilesDir:   filepath.Join(root, "profiles"),
		SharedDir:     filepath.Join(root, "shared"),
	}
	// The profile has its devcontainer.json and no Dockerfile: the second
	// copy fails, after the directory and the first file are made.
	if err := os.MkdirAll(filepath.Join(cfg.ProfilesDir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ProfilesDir, "go", "devcontainer.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Create(cfg, "api", "go", false)
	if err == nil || !strings.HasPrefix(err.Error(), "copy Dockerfile: ") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Create returned %v; want the failed copy's error, unchanged", err)
	}
	if _, statErr := os.Lstat(filepath.Join(cfg.WorkspacesDir, "api")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the workspace directory a failed Create made is still there: %v", statErr)
	}

	kept := filepath.Join(cfg.WorkspacesDir, "web", "notes.md")
	if err := os.MkdirAll(filepath.Dir(kept), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Create(cfg, "web", "go", false); err == nil {
		t.Fatal("Create succeeded without a Dockerfile")
	}
	if _, statErr := os.Stat(kept); statErr != nil {
		t.Errorf("a failed Create removed a workspace directory it did not make: %v", statErr)
	}

	if err := os.WriteFile(filepath.Join(cfg.ProfilesDir, "go", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Create(cfg, "api", "go", false); err != nil {
		t.Fatalf("Create after the failure: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.WorkspacesDir, "api", ".devcontainer", "Dockerfile")); statErr != nil {
		t.Errorf("a successful Create left no Dockerfile: %v", statErr)
	}
}

func TestStripJSONCComments(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"no comments",
			`{"key": "value"}`,
			`{"key": "value"}`,
		},
		{
			"single-line comment",
			"// comment\n{\"key\": \"value\"}",
			"\n{\"key\": \"value\"}",
		},
		{
			"inline comment",
			"{\"key\": \"value\"} // trailing",
			"{\"key\": \"value\"} ",
		},
		{
			"url in string preserved",
			`{"url": "https://example.com"}`,
			`{"url": "https://example.com"}`,
		},
		{
			"multiple comments",
			"// first\n{\"a\": 1}\n// second\n",
			"\n{\"a\": 1}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stripJSONCComments(tt.input)
			if err != nil {
				t.Fatalf("stripJSONCComments(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("stripJSONCComments(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseDevpodStatus(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"running",
			"18:33:52 info Workspace 'dotfiles' is 'Running'",
			"Running",
		},
		{
			"stopped",
			"18:33:52 info Workspace 'test-ws' is 'Stopped'",
			"Stopped",
		},
		{
			"multiline",
			"some noise\n18:33:52 info Workspace 'app' is 'Busy'\nmore noise\n",
			"Busy",
		},
		{
			"no status",
			"some random output",
			"",
		},
		{
			"empty",
			"",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDevpodStatus(tt.input)
			if got != tt.expected {
				t.Errorf("parseDevpodStatus(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestPatchProxyNetworkAtomicRewrite pins patchProxyNetwork's observable
// contract: devcontainer.json is rewritten in place as valid JSON with the
// network runArgs and proxy-route postStartCommand added, the existing
// postStartCommand is preserved under "setup", no temp file is left behind,
// and the file stays a regular 0644 file.
func TestPatchProxyNetworkAtomicRewrite(t *testing.T) {
	dir := t.TempDir()
	dcPath := filepath.Join(dir, "devcontainer.json")
	seed := "// JSONC comment must survive the strip+parse round trip\n" +
		"{\n\t\"image\": \"example\",\n\t\"runArgs\": [\"--hostname=dev\"],\n\t\"postStartCommand\": \"echo hi\"\n}"
	if err := os.WriteFile(dcPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := patchProxyNetwork(dcPath, "proxy-net", "10.0.0.2"); err != nil {
		t.Fatalf("patchProxyNetwork: %v", err)
	}

	info, err := os.Lstat(dcPath)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("devcontainer.json mode = %s, want regular file", info.Mode())
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 644", info.Mode().Perm())
	}

	data, err := os.ReadFile(dcPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var dc map[string]any
	if err := json.Unmarshal(data, &dc); err != nil {
		t.Fatalf("rewritten devcontainer.json is not valid JSON: %v", err)
	}
	runArgs, _ := dc["runArgs"].([]any)
	joined := fmt.Sprintf("%v", runArgs)
	if !strings.Contains(joined, "--network=proxy-net") || !strings.Contains(joined, "--cap-add=NET_ADMIN") {
		t.Errorf("runArgs missing network patch: %v", runArgs)
	}
	post, _ := dc["postStartCommand"].(map[string]any)
	if post == nil || post["proxy-route"] == nil {
		t.Fatalf("postStartCommand missing proxy-route: %v", dc["postStartCommand"])
	}
	if post["setup"] != "echo hi" {
		t.Errorf("existing postStartCommand not preserved under setup: %v", post)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (temp remnant?): %v", len(entries), entries)
	}
}

package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

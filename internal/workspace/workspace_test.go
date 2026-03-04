package workspace

import "testing"

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
			got := stripJSONCComments(tt.input)
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

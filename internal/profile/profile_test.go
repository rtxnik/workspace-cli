package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDockerfileBase(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "standard FROM",
			content:  "FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04\n\nRUN apt-get update",
			expected: "mcr.microsoft.com/devcontainers/base:ubuntu-24.04",
		},
		{
			name:     "FROM with AS",
			content:  "FROM ubuntu:22.04 AS builder\nRUN make",
			expected: "ubuntu:22.04",
		},
		{
			name:     "empty file",
			content:  "",
			expected: "",
		},
		{
			name:     "comment before FROM",
			content:  "# base image\nFROM alpine:3.19\n",
			expected: "alpine:3.19",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Dockerfile")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got := parseDockerfileBase(path)
			if got != tt.expected {
				t.Errorf("parseDockerfileBase() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestIsBuiltin(t *testing.T) {
	tests := []struct {
		name     string
		profile  string
		expected bool
	}{
		{name: "default is builtin", profile: "default", expected: true},
		{name: "devops is builtin", profile: "devops", expected: true},
		{name: "go is builtin", profile: "go", expected: true},
		{name: "k8s is builtin", profile: "k8s", expected: true},
		{name: "matrix is builtin", profile: "matrix", expected: true},
		{name: "meta is builtin", profile: "meta", expected: true},
		{name: "proxy is builtin", profile: "proxy", expected: true},
		{name: "python is builtin", profile: "python", expected: true},
		{name: "rust is builtin", profile: "rust", expected: true},
		{name: "synopra is builtin", profile: "synopra", expected: true},
		{name: "web is builtin", profile: "web", expected: true},
		{name: "custom profile is not builtin", profile: "mycustom", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsBuiltin(tt.profile)
			if got != tt.expected {
				t.Errorf("IsBuiltin(%q) = %v, want %v", tt.profile, got, tt.expected)
			}
		})
	}
}

func TestParseMiseTools(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "standard tools section",
			content:  "[tools]\ngo = \"latest\"\ngolangci-lint = \"latest\"\nnode = \"lts\"\n",
			expected: "go, golangci-lint, node",
		},
		{
			name:     "empty tools section",
			content:  "[tools]\n\n[settings]\n",
			expected: "",
		},
		{
			name:     "tools with comments",
			content:  "[tools]\n# main language\nrust = \"latest\"\ncargo-binstall = \"latest\"\n",
			expected: "rust, cargo-binstall",
		},
		{
			name:     "no tools section",
			content:  "[settings]\nlegacy_version_file = true\n",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mise.toml")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got := parseMiseTools(path)
			if got != tt.expected {
				t.Errorf("parseMiseTools() = %q, want %q", got, tt.expected)
			}
		})
	}
}

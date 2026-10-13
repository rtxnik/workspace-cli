package profile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
)

// TestCheckDeletable: a built-in profile, an invalid name, a profile that is
// not there — a dangling symlink included — are refused with the words
// Delete has always used; a permission denied while looking is that error,
// not "not found"; a profile that is there is deletable, and Delete refuses
// what CheckDeletable refuses.
func TestCheckDeletable(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{ProfilesDir: dir}
	if err := os.Mkdir(filepath.Join(dir, "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, want string }{
		{"default", `cannot delete built-in profile "default"`},
		{"nosuch", `profile "nosuch" not found`},
		{"dangling", `profile "dangling" not found`},
	} {
		if err := CheckDeletable(cfg, c.name); err == nil || err.Error() != c.want {
			t.Errorf("CheckDeletable(%q) = %v; want %q", c.name, err, c.want)
		}
		if err := Delete(cfg, c.name); err == nil || err.Error() != c.want {
			t.Errorf("Delete(%q) = %v; want %q", c.name, err, c.want)
		}
	}
	if err := CheckDeletable(cfg, "Bad Name"); err == nil || err.Error() != ValidateName("Bad Name").Error() {
		t.Errorf("CheckDeletable(%q) = %v; want the name's own refusal", "Bad Name", err)
	}
	if err := CheckDeletable(cfg, "mine"); err != nil {
		t.Errorf("CheckDeletable(mine) = %v; want nil", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads a directory it has no permission on")
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := CheckDeletable(cfg, "mine"); !errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "not found") {
		t.Errorf("CheckDeletable under a directory it cannot search = %v; want the permission error, not not found", err)
	}
}

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
		{
			name:     "over-long line before FROM yields empty",
			content:  strings.Repeat("x", 100000) + "\nFROM alpine:3.19\n",
			expected: "",
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
		{
			name:     "scan error suppresses partial list",
			content:  "[tools]\ngo = \"latest\"\n" + strings.Repeat("x", 100000) + "\nnode = \"lts\"\n",
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

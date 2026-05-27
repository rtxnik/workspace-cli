package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoStatus holds the git health of a single repository.
type RepoStatus struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Branch   string `json:"branch"`
	Clean    bool   `json:"clean"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	NoRemote bool   `json:"no_remote"`
	Error    string `json:"error,omitempty"`
}

// ProbeRepo runs git against the repo at path and returns its health.
func ProbeRepo(path string) RepoStatus {
	rs := RepoStatus{Name: filepath.Base(path), Path: path}

	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		rs.Error = "not a git repository"
		return rs
	}
	rs.Exists = true

	out, err := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		rs.Error = fmt.Sprintf("branch: %v", err)
		return rs
	}
	rs.Branch = strings.TrimSpace(string(out))

	porcelain, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		rs.Error = fmt.Sprintf("status: %v", err)
		return rs
	}
	rs.Clean = len(strings.TrimSpace(string(porcelain))) == 0

	revList, err := exec.Command("git", "-C", path, "rev-list", "--left-right", "--count", "HEAD...@{upstream}").Output()
	if err != nil {
		rs.NoRemote = true
		return rs
	}
	fmt.Sscanf(strings.TrimSpace(string(revList)), "%d\t%d", &rs.Ahead, &rs.Behind)

	return rs
}

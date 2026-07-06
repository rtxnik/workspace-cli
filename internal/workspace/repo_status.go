package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/procx"
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

	// Repo health probes parse git's stdout, so procx.Run (Output semantics)
	// keeps stderr out of the parsed bytes; the deadline keeps a wedged git
	// (e.g. a repo on an unresponsive filesystem) from hanging the sweep.
	out, err := procx.Run(context.Background(), timeoutProbe, "git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		rs.Error = fmt.Sprintf("branch: %v", err)
		return rs
	}
	rs.Branch = strings.TrimSpace(string(out))

	porcelain, err := procx.Run(context.Background(), timeoutProbe, "git", "-C", path, "status", "--porcelain")
	if err != nil {
		rs.Error = fmt.Sprintf("status: %v", err)
		return rs
	}
	rs.Clean = len(strings.TrimSpace(string(porcelain))) == 0

	revList, err := procx.Run(context.Background(), timeoutProbe, "git", "-C", path, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		rs.NoRemote = true
		return rs
	}
	// Deterministic git output ("N\tM"); on parse failure Ahead/Behind stay 0.
	_, _ = fmt.Sscanf(strings.TrimSpace(string(revList)), "%d\t%d", &rs.Ahead, &rs.Behind)

	return rs
}

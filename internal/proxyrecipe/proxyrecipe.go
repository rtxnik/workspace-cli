// Package proxyrecipe pins the proxy image recipe (Dockerfile + entrypoint.sh)
// to a known-good content digest embedded in the ws binary, and verifies an
// on-disk recipe against it. This closes the C5 contract gap: the ws binary and
// the proxy image can no longer silently drift apart, because BuildProxyImage
// refuses to build (and the doctor refuses to bless) a recipe that does not
// match the pin.
package proxyrecipe

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed recipe.lock
var recipeLockRaw []byte

// Pinned is the committed known-good recipe manifest.
type Pinned struct {
	DatapathMode string            `json:"datapath_mode"`
	DotfilesRef  string            `json:"dotfiles_ref"`
	Files        map[string]string `json:"files"` // bare filename -> lowercase sha256 hex
}

// FileMismatch records a recipe file whose on-disk hash differs from the pin.
type FileMismatch struct {
	File string
	Want string
	Got  string
}

// Result is the outcome of verifying an on-disk recipe against the pin.
type Result struct {
	OK             bool
	Mode           string // the pinned datapath mode (informational)
	CombinedDigest string // sha256 over the sorted on-disk file hashes
	Mismatches     []FileMismatch
	Missing        []string
}

// DriftSummary names what drifted (file names only — never file contents).
func (r Result) DriftSummary() string {
	var parts []string
	for _, m := range r.Mismatches {
		parts = append(parts, m.File+" (content changed)")
	}
	for _, f := range r.Missing {
		parts = append(parts, f+" (missing)")
	}
	if len(parts) == 0 {
		return "no drift"
	}
	return strings.Join(parts, ", ")
}

// Load parses the embedded recipe.lock pin.
func Load() (Pinned, error) {
	var p Pinned
	if err := json.Unmarshal(recipeLockRaw, &p); err != nil {
		return Pinned{}, fmt.Errorf("parse embedded recipe.lock: %w", err)
	}
	return p, nil
}

// Verify hashes <profilesDir>/proxy/<file> for every pinned file and compares to
// the embedded pin.
func Verify(profilesDir string) (Result, error) {
	p, err := Load()
	if err != nil {
		return Result{}, err
	}
	return verify(p, filepath.Join(profilesDir, "proxy"))
}

// verify is the pure core: compare each pinned file's sha256 in recipeDir.
func verify(p Pinned, recipeDir string) (Result, error) {
	res := Result{OK: true, Mode: p.DatapathMode}

	names := make([]string, 0, len(p.Files))
	for name := range p.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	var digestLines []string
	for _, name := range names {
		want := strings.ToLower(p.Files[name])
		data, err := os.ReadFile(filepath.Join(recipeDir, name))
		if err != nil {
			if os.IsNotExist(err) {
				res.OK = false
				res.Missing = append(res.Missing, name)
				continue
			}
			return Result{}, fmt.Errorf("read recipe file %q: %w", name, err)
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		digestLines = append(digestLines, name+"="+got)
		if got != want {
			res.OK = false
			res.Mismatches = append(res.Mismatches, FileMismatch{File: name, Want: want, Got: got})
		}
	}

	combined := sha256.Sum256([]byte(strings.Join(digestLines, "\n")))
	res.CombinedDigest = hex.EncodeToString(combined[:])
	return res, nil
}

package xrayconf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveConfigTarget resolves path to the concrete file a write must land on,
// following an active-profile symlink to its target while refusing any target
// that escapes every directory in allowedRoots. allowedRoots are the
// directories a legitimate xray-config target may live in — for the active
// config that is {filepath.Dir(cfg.XrayConfig), cfg.XrayProfilesDir}.
//
//   - path exists (regular file or symlink): the resolved target must lie
//     within one allowed root, else an error (containment refusal); existed=true.
//   - path missing: the target is rebuilt beneath the nearest existing (resolved)
//     ancestor so cold init / a fresh install stays creatable; existed=false.
//   - dangling / unresolvable symlink: error (fail-closed; never clobber).
//
// Resolution is unconditional — a non-symlink resolves to itself; there is no
// bool toggle. The caller writes the returned path so an active-profile symlink
// is preserved (the write lands on the profile file, not on the link) and the
// write stays atomic.
func ResolveConfigTarget(path string, allowedRoots []string) (string, bool, error) {
	roots := make([]string, 0, len(allowedRoots))
	for _, r := range allowedRoots {
		rr, err := resolvePathAllowingMissing(r)
		if err != nil {
			return "", false, fmt.Errorf("resolve config root %q: %w", r, err)
		}
		roots = append(roots, rr)
	}

	_, statErr := os.Lstat(path)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("stat config path: %w", statErr)
	}

	resolved, err := resolvePathAllowingMissing(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve config path: %w", err)
	}
	if !withinAny(resolved, roots) {
		return "", false, fmt.Errorf("resolved config target %q escapes the allowed config directories", resolved)
	}
	return resolved, existed, nil
}

// resolvePathAllowingMissing resolves every symlink in p, tolerating a
// non-existent tail: it EvalSymlinks the deepest existing ancestor and
// re-appends the missing remainder. A dangling symlink component surfaces the
// EvalSymlinks error (fail-closed). For a fully existing p it equals
// filepath.EvalSymlinks(p).
func resolvePathAllowingMissing(p string) (string, error) {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	missing := ""
	cur := p
	for {
		if _, err := os.Lstat(cur); err == nil {
			resolved, evErr := filepath.EvalSymlinks(cur)
			if evErr != nil {
				return "", evErr
			}
			if missing == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, missing), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil // reached the filesystem root; nothing left to resolve
		}
		missing = filepath.Join(filepath.Base(cur), missing)
		cur = parent
	}
}

// withinAny reports whether target is inside any of roots.
func withinAny(target string, roots []string) bool {
	for _, r := range roots {
		if within(target, r) {
			return true
		}
	}
	return false
}

// within reports whether target is root itself or lives beneath it, using
// filepath.Rel so it is correct even when root is the filesystem root "/".
func within(target, root string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

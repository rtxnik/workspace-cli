package xray

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
)

// MigrateLegacy detects legacy regular-file ~/.config/xray/config.json and
// converts it to the profiles/primary.json + symlink layout per D-07.
//
// Detection uses os.Lstat (NOT os.Stat — Stat follows symlinks and would
// silently miss the already-migrated case; RESEARCH §7 Pitfall 3).
//
// Returns (migrated bool, err error):
//   - (false, nil) — fresh install (config.json missing) OR already-symlink (idempotent no-op).
//   - (true, nil)  — migration just performed (regular file → primary.json + relative symlink).
//   - (false, err) — regular-file + existing profiles/primary.json conflict; NEITHER
//     file is mutated (RESEARCH §7 "most likely failure mode"). Manual remediation only.
func MigrateLegacy(cfg config.Config) (bool, error) {
	info, err := os.Lstat(cfg.XrayConfig)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // fresh install; profile add will create config.json later
		}
		return false, fmt.Errorf("lstat %s: %w", cfg.XrayConfig, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil // already migrated; idempotent no-op
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is neither file nor symlink (mode=%s)", cfg.XrayConfig, info.Mode())
	}

	// Regular file → migrate.
	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", cfg.XrayProfilesDir, err)
	}
	primaryPath := filepath.Join(cfg.XrayProfilesDir, "primary.json")

	// CRITICAL refuse-to-clobber (RESEARCH §7 most-likely-failure-mode): operator's
	// real Mac state can have BOTH a regular-file config.json AND a populated
	// profiles/primary.json (created out-of-band by the ad-hoc xray-switch shell).
	// Use os.Lstat here too so a stray symlink at primaryPath is treated as a
	// real conflict, never silently followed.
	if _, err := os.Lstat(primaryPath); err == nil {
		return false, fmt.Errorf(
			"cannot migrate: %s is a regular file but %s already exists. "+
				"Manual remediation: inspect both, decide canonical, remove the other, then re-run `ws proxy profile use primary`.",
			cfg.XrayConfig, primaryPath,
		)
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("lstat %s: %w", primaryPath, err)
	}

	if err := os.Rename(cfg.XrayConfig, primaryPath); err != nil {
		return false, fmt.Errorf("rename %s -> %s: %w", cfg.XrayConfig, primaryPath, err)
	}

	// Relative symlink target (NOT absolute) so the link survives chezmoi apply
	// rewrites and DevPod rebuilds. Wave-3 AtomicSymlink reused here rather than
	// a bare os.Symlink so the create itself is atomic against any concurrent
	// reader between os.Rename and link materialisation (and so a future
	// failure-mode-improvement of AtomicSymlink lands here too without touching
	// migrate.go).
	relativeTarget := filepath.Join("profiles", "primary.json")
	if err := AtomicSymlink(relativeTarget, cfg.XrayConfig); err != nil {
		return false, fmt.Errorf(
			"create symlink (note: %s already moved to %s; restore manually if needed): %w",
			cfg.XrayConfig, primaryPath, err,
		)
	}
	return true, nil
}

// EnsureMigrated is the cmd-side wrapper invoked by every `ws proxy profile *`
// PersistentPreRunE.
//
// When allowMigrate is true (default — --no-migrate NOT set), invokes
// MigrateLegacy and emits a [migrated] info log to stderr on first conversion.
//
// When allowMigrate is false (--no-migrate is set) AND migration WOULD have
// triggered (regular-file config.json), returns an error directing the operator
// to either drop --no-migrate or migrate manually. Inspects state via os.Lstat
// without mutating anything (PROXY-MIG-02 + memory feedback_no_auto_state_mutation).
func EnsureMigrated(cfg config.Config, allowMigrate bool) error {
	if !allowMigrate {
		info, err := os.Lstat(cfg.XrayConfig)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // fresh install; nothing to migrate
			}
			return fmt.Errorf("lstat %s: %w", cfg.XrayConfig, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // already migrated
		}
		if info.Mode().IsRegular() {
			return fmt.Errorf(
				"migration required but --no-migrate is set; either run `ws proxy profile add primary <uri>` manually OR drop --no-migrate to auto-migrate %s",
				cfg.XrayConfig,
			)
		}
		return fmt.Errorf("%s is neither file nor symlink (mode=%s)", cfg.XrayConfig, info.Mode())
	}
	migrated, err := MigrateLegacy(cfg)
	if err != nil {
		return err
	}
	if migrated {
		output.Info(fmt.Sprintf(
			"[migrated] Renamed %s to %s and created symlink",
			cfg.XrayConfig,
			filepath.Join(cfg.XrayProfilesDir, "primary.json"),
		))
	}
	return nil
}

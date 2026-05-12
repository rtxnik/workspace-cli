package xray

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/output"
)

// xrayRestartLivenessTimeout is the deadline for post-restart liveness check
// per RESEARCH §6 and D-10. 15s = practical middle ground given Dockerfile
// HEALTHCHECK 30s interval + 10s timeout + 5s start-period + 3 retries.
const xrayRestartLivenessTimeout = 15 * time.Second

// Test seams: production wires these to real implementations; tests override.
// Kept as function-typed vars (not interfaces) because the surface is tiny and
// per-test seam swapping is more ergonomic than a mock object.
var (
	validateProfileFn     = realValidateProfile
	restartProxyFn        = docker.ProxyRestart
	waitForHealthFn       = docker.WaitForHealth
	bindMountIsWholeDirFn = docker.BindMountIsWholeDir
)

// AtomicSymlink replaces linkPath with a symlink to target atomically.
// Linux: create a temp symlink in the same directory then os.Rename it
// over linkPath. Observers see either the old target or the new target,
// never an in-between "missing" state.
//
// D-04 enforcement: NEVER shell out to `ln -sfn`; NEVER use a non-atomic
// remove-then-create sequence (opens a window where the symlink is missing).
func AtomicSymlink(target, linkPath string) error {
	dir := filepath.Dir(linkPath)
	base := filepath.Base(linkPath)
	tmp := filepath.Join(dir, "."+base+".tmp."+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("create temp symlink: %w", err)
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

// ValidateProfile shells out via docker.ProxyExec to
// `docker exec dev-proxy xray run -test -config /etc/xray/profiles/<name>.json`.
// Non-zero exit returns an error wrapping the docker stderr verbatim.
//
// Pre-condition (per PROXY-PROFILE-15): the dev-proxy bind must mount the whole
// ~/.config/xray/ directory so /etc/xray/profiles/<name>.json is visible
// inside the container. SwitchTo verifies this via BindMountIsWholeDir before
// calling ValidateProfile.
func ValidateProfile(cfg config.Config, name string) error {
	return validateProfileFn(cfg, name)
}

func realValidateProfile(cfg config.Config, name string) error {
	containerProfilePath := "/etc/xray/profiles/" + name + ".json"
	out, err := docker.ProxyExec(cfg, "xray", "run", "-test", "-config", containerProfilePath)
	if err != nil {
		return fmt.Errorf("xray -test failed for profile %q: %w (output: %s)", name, err, string(out))
	}
	return nil
}

// SwitchTo orchestrates Validate → AtomicSwap → Restart → WaitForHealth.
//
// D-10 + memory feedback_no_auto_state_mutation enforcement: ANY step 2/3/4
// failure surfaces output.RenderError and returns the wrapped error. NO
// auto-rollback. NO retry. The symlink is left pointing at the new
// (potentially broken) target so the operator decides next move with full
// information. The tripwire test TestManualRecoveryOnFailedSwitch asserts
// this contract — adding auto-rollback breaks CI.
func SwitchTo(cfg config.Config, name string) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}

	// Capture previous active for error message ONLY (never for rollback).
	previousActive, _ := ReadActiveProfileName(cfg) // "" if missing — non-fatal

	// PROXY-PROFILE-15 precondition: whole-dir bind must be in place so
	// /etc/xray/profiles/<name>.json is visible inside the container.
	if ok, err := bindMountIsWholeDirFn(cfg); err == nil && !ok {
		return fmt.Errorf(
			"dev-proxy is using the legacy single-file bind mount; run `ws proxy down && ws proxy up` once to switch to the whole-directory bind (the CLI will not auto-recreate the container — your decision)",
		)
	}

	// Pre-flight: target profile file exists on the host.
	target := filepath.Join(cfg.XrayProfilesDir, name+".json")
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("profile %q not found at %s: %w", name, target, err)
	}

	runner := output.NewStepRunner(
		output.Step{Name: "Validate target profile (xray -test)", Fn: func() error {
			return ValidateProfile(cfg, name)
		}},
		output.Step{Name: "Atomic symlink swap", Fn: func() error {
			relativeTarget := filepath.Join("profiles", name+".json")
			return AtomicSymlink(relativeTarget, cfg.XrayConfig)
		}},
		output.Step{Name: "Restart dev-proxy", Fn: func() error {
			return restartProxyFn(cfg)
		}},
		output.Step{Name: fmt.Sprintf("Wait for liveness (<=%s)", xrayRestartLivenessTimeout), Fn: func() error {
			return waitForHealthFn(cfg, xrayRestartLivenessTimeout)
		}},
	)
	if err := runner.Run(); err != nil {
		ctx := map[string]string{"Error": err.Error()}
		if previousActive != "" {
			ctx["Previous profile"] = previousActive
		}
		suggestions := []string{}
		if previousActive != "" {
			suggestions = append(suggestions, fmt.Sprintf("Restore previous: ws proxy profile use %s", previousActive))
		}
		suggestions = append(suggestions, "Inspect logs: docker logs dev-proxy --tail 50")
		fmt.Fprintln(os.Stderr, output.RenderError(output.ErrorDetail{
			Title:       fmt.Sprintf("Switch to %q failed", name),
			Context:     ctx,
			Suggestions: suggestions,
		}))
		// NO AUTO-ROLLBACK. NO RETRY. The symlink stays where it is.
		// Operator decides next move with full information.
		return fmt.Errorf("switch to %q failed (previous=%q): %w", name, previousActive, err)
	}
	output.Success(fmt.Sprintf("Switched to %q", name))
	return nil
}

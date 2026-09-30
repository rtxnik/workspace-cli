package xray

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/fsutil"
	"github.com/rtxnik/workspace-cli/internal/output"
)

// xrayRestartLivenessTimeout is the deadline for the post-restart liveness
// check. The restart stops and starts the container, which resets Docker's
// health status to "starting" just as a fresh create does, so the switch waits
// the same docker.ProxyHealthBudget as `ws proxy up` and the recreate path.
const xrayRestartLivenessTimeout = docker.ProxyHealthBudget

// Test seams: production wires these to real implementations; tests override.
// Kept as function-typed vars (not interfaces) because the surface is tiny and
// per-test seam swapping is more ergonomic than a mock object.
var (
	validateProfileFn     = realValidateProfile
	restartProxyFn        = docker.RestartContainerNoVerify
	waitForHealthFn       = docker.WaitForHealth
	bindMountIsWholeDirFn = docker.BindMountIsWholeDir
)

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

// SwitchTo orchestrates Validate → AtomicSwap → Restart → WaitForHealth on
// the step runner, and prints nothing itself.
//
// D-10 + memory feedback_no_auto_state_mutation enforcement: ANY step 2/3/4
// failure returns an error that carries the switch's Problem — the previous
// profile, the active one once the swap is done, and the steps that recover —
// for the root to print. NO auto-rollback. NO retry. The symlink is left
// pointing at the new (potentially broken) target so the operator decides
// next move with full information. The tripwire test
// TestManualRecoveryOnFailedSwitch asserts this contract — adding
// auto-rollback breaks CI. The pre-swap errors — an invalid name, the legacy
// bind, a missing file — are plain errors.
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

	swapped := false
	if err := output.Run(
		output.Task{Title: "Validate target profile (xray -test)", Run: func(*output.Log) error {
			return ValidateProfile(cfg, name)
		}},
		output.Task{Title: "Atomic symlink swap", Run: func(*output.Log) error {
			relativeTarget := filepath.Join("profiles", name+".json")
			if err := fsutil.AtomicSymlink(relativeTarget, cfg.XrayConfig); err != nil {
				return err
			}
			swapped = true
			return nil
		}},
		output.Task{Title: "Restart dev-proxy", Run: func(*output.Log) error {
			return restartProxyFn(cfg)
		}},
		output.Task{Title: fmt.Sprintf("Wait for liveness (<=%s)", xrayRestartLivenessTimeout), Run: func(*output.Log) error {
			return waitForHealthFn(cfg, xrayRestartLivenessTimeout)
		}},
	); err != nil {
		// NO AUTO-ROLLBACK. NO RETRY. The symlink stays where it is.
		// Operator decides next move with full information.
		return &output.ProblemError{
			P:   switchProblem(cfg, name, previousActive, swapped, err),
			Err: fmt.Errorf("switch to %q failed (previous=%q): %w", name, previousActive, err),
		}
	}
	return nil
}

// switchProblem is the Problem of a switch that failed after its pre-flight:
// the failed step's error and last lines as the cause, the previous profile
// and — once the swap is done — the active one, not rolled back, and the
// steps that recover.
func switchProblem(cfg config.Config, name, previous string, swapped bool, err error) output.Problem {
	cause := err.Error()
	var te *output.TaskError
	if errors.As(err, &te) && len(te.Tail) > 0 {
		cause += "\n" + strings.Join(te.Tail, "\n")
	}
	p := output.Problem{Title: fmt.Sprintf("Switch to %q failed", name), Cause: cause}
	if previous != "" {
		p.Facts = append(p.Facts, output.Fact{K: "Previous", V: previous})
	}
	if swapped {
		p.Facts = append(p.Facts, output.Fact{K: "Active", V: name + " (not rolled back)"})
	}
	if previous != "" {
		p.Steps = append(p.Steps, output.Remedy{Label: "Restore previous", Cmd: "ws proxy profile use " + previous})
	}
	p.Steps = append(p.Steps, output.Remedy{Label: "Inspect logs", Cmd: "docker logs " + cfg.ProxyContainer + " --tail 50"})
	return p
}

// SwitchToSymlinkOnly performs Validate -> AtomicSwap and stops. NO
// restart. NO wait-for-health. Used by `ws proxy profile use --no-reload`
// and by tests that want to exercise the swap without docker side-effects.
//
// D-10: same pre-flight rigor as SwitchTo (ValidateProfileName,
// bind-mount check, target-file existence, xray -test) — failures
// return early without committing the symlink swap. Post-swap there is
// nothing left to fail; the function returns nil after a successful
// fsutil.AtomicSymlink.
//
// The ~10-line pre-flight duplication with SwitchTo is intentional —
// see plan decision D-symlink-only-path: a shared helper would force
// changing SwitchTo's call shape and risk perturbing the
// TestManualRecoveryOnFailedSwitch tripwire.
func SwitchToSymlinkOnly(cfg config.Config, name string) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	if ok, err := bindMountIsWholeDirFn(cfg); err == nil && !ok {
		return fmt.Errorf(
			"dev-proxy is using the legacy single-file bind mount; run `ws proxy down && ws proxy up` once to switch to the whole-directory bind (the CLI will not auto-recreate the container — your decision)",
		)
	}
	target := filepath.Join(cfg.XrayProfilesDir, name+".json")
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("profile %q not found at %s: %w", name, target, err)
	}
	if err := ValidateProfile(cfg, name); err != nil {
		return err
	}
	relativeTarget := filepath.Join("profiles", name+".json")
	return fsutil.AtomicSymlink(relativeTarget, cfg.XrayConfig)
}

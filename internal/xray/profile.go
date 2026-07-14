package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/fsutil"
	"github.com/rtxnik/workspace-cli/internal/hysteria2"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/vless"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// D5-01 test seams: production wires these to the real Docker-backed
// implementations; tests override them for hermetic runs.
//
//   - verifyProxyReadyFn reports whether the proxy container is reachable AND
//     uses the whole-dir bind, so a host-staged file is visible to `xray -test`
//     inside the container. A nil return => validation can run (hard gate); a
//     non-nil return => inconclusive (advisory).
//   - validateAtPathFn runs `xray -test` against an in-container config path.
var (
	verifyProxyReadyFn = docker.VerifyProxyReadyForReload
	validateAtPathFn   = realValidateProfileAtPath
)

// realValidateProfileAtPath runs `xray run -test -config <containerPath>` inside
// the proxy container; a non-zero exit returns an error wrapping xray's stderr.
//
// This intentionally duplicates the 3-line exec of switch.go's
// realValidateProfile rather than sharing it: switch.go owns the
// TestManualRecoveryOnFailedSwitch tripwire and the codebase prefers a tiny
// duplication over perturbing that file (cf. SwitchToSymlinkOnly, switch.go).
func realValidateProfileAtPath(cfg config.Config, containerPath string) error {
	out, err := docker.ProxyExec(cfg, "xray", "run", "-test", "-config", containerPath)
	if err != nil {
		return fmt.Errorf("xray -test failed: %w (output: %s)", err, string(out))
	}
	return nil
}

// ProfileSummary is the row shape used by `list` output (table + JSON).
// UUIDMasked is always masked here (D-13) — list never emits raw UUIDs.
type ProfileSummary struct {
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Protocol   string `json:"protocol,omitempty"` // "" for vless (default), "hysteria2" for hy2
	Transport  string `json:"transport"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Security   string `json:"security"`
	SNI        string `json:"sni,omitempty"`
	UUIDMasked string `json:"uuid,omitempty"`
}

// AddProfile parses uri via the protocol parser, copies routing rules from the
// currently-active profile (D-05), and writes cfg.XrayProfilesDir/<name>.json.
// Returns an error if name is reserved/invalid (ValidateProfileName), URI is
// invalid (generateProfileConfig), or target file exists and !force.
//
// Per RESEARCH §3: uses generateProfileConfig + manual json.MarshalIndent
// because the routing block needs to be overridden with the active profile's
// rules before write. Legacy node-append helpers are NEVER invoked here.
func AddProfile(cfg config.Config, name, uri string, force bool) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}

	targetCfg, err := GenerateProfileConfig(uri)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o700); err != nil {
		return fmt.Errorf("create profiles dir: %w", err)
	}

	target := filepath.Join(cfg.XrayProfilesDir, name+".json")
	if _, statErr := os.Stat(target); statErr == nil && !force {
		return fmt.Errorf("profile %q already exists at %s (use --force to overwrite)", name, target)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat target %s: %w", target, statErr)
	}

	// D-05: copy routing rules from the currently-active profile so per-host
	// rules (e.g. port:22 → direct) and balancer wiring persist across `add`.
	if active, readErr := os.Readlink(cfg.XrayConfig); readErr == nil {
		activePath := active
		if !filepath.IsAbs(active) {
			activePath = filepath.Join(filepath.Dir(cfg.XrayConfig), active)
		}
		if data, rerr := os.ReadFile(activePath); rerr == nil {
			var activeCfg xrayconf.XrayConfig
			if uerr := json.Unmarshal(data, &activeCfg); uerr == nil {
				targetCfg.Routing = activeCfg.Routing
			}
		}
	}

	data, err := json.MarshalIndent(targetCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return writeProfileValidated(cfg, name, target, data)
}

// writeProfileValidated commits data to target behind an `xray -test` gate (D5-01).
//
// When the proxy is reachable with the whole-dir bind (verifyProxyReadyFn
// returns nil), data is staged to a hidden .json file, validated inside the
// container, and only atomically renamed over target on success — so a config
// xray rejects never lands under the final name and never clobbers an existing
// good profile.
// When the proxy is unreachable, validation is inconclusive: emit a loud
// advisory and write unvalidated (the gate at `ws proxy profile use` still
// hard-validates before anything goes live).
//
// The stage lives at the bind ROOT (filepath.Dir(cfg.XrayConfig) →
// /etc/xray/), NOT under profiles/, for two reasons that must both hold:
//   - it MUST end in .json, because `xray -test` infers the config format from
//     the file extension (a .tmp stage fails with "Failed to get format",
//     independent of the config's validity — that would make the gate a no-op);
//   - it MUST stay out of the profiles/*.json glob so a half-committed profile
//     is never visible to ListProfiles.
//
// A dotfile-named .json at the bind root satisfies both: it is visible to the
// container yet never scanned (ListProfiles/upgrade only read profiles/).
func writeProfileValidated(cfg config.Config, name, target string, data []byte) error {
	if err := verifyProxyReadyFn(cfg); err != nil {
		output.Warn(fmt.Sprintf(
			"could not validate %q with xray -test (%v); wrote it UNVALIDATED — "+
				"it will be validated on `ws proxy profile use %s`", name, err, name))
		if werr := fsutil.WriteFile(target, data, 0o600); werr != nil {
			return fmt.Errorf("write profile %s: %w", target, werr)
		}
		return nil
	}

	stageDir := filepath.Dir(cfg.XrayConfig) // whole-dir bind root -> /etc/xray

	// Best-effort sweep of stale staging files for THIS name left by a prior
	// crashed add (nanos-unique names never collide; single-user CLI).
	if debris, gerr := filepath.Glob(filepath.Join(stageDir, "."+name+".add-validating.*.json")); gerr == nil {
		for _, f := range debris {
			_ = os.Remove(f)
		}
	}

	stage := filepath.Join(stageDir,
		"."+name+".add-validating."+strconv.FormatInt(time.Now().UnixNano(), 10)+".json")
	if err := fsutil.WriteFile(stage, data, 0o600); err != nil {
		return fmt.Errorf("stage profile for validation: %w", err)
	}

	containerPath := "/etc/xray/" + filepath.Base(stage)
	if err := validateAtPathFn(cfg, containerPath); err != nil {
		_ = os.Remove(stage)
		return fmt.Errorf("generated config for %q rejected by xray -test (the on-disk profile was not modified): %w", name, err)
	}
	if err := os.Rename(stage, target); err != nil {
		_ = os.Remove(stage)
		return fmt.Errorf("commit validated profile %s: %w", target, err)
	}
	return nil
}

// ListProfilesDetailed enumerates cfg.XrayProfilesDir/*.json and parses each
// file exactly once via LoadProfile into a raw DetailedProfile (secrets
// UNMASKED), returning the slice sorted by Name. Profiles whose name is invalid
// (ValidateProfileName, run inside LoadProfile) or whose JSON cannot be parsed
// are logged via output.Warn (stderr) and skipped — enumeration never errors on
// a single bad profile.
//
// This is the raw superset feed for the --reveal path; callers rendering to a
// human MUST project via DetailedProfile.Summary (or mask each field) unless
// --reveal is explicitly set (D-13).
func ListProfilesDetailed(cfg config.Config) ([]DetailedProfile, error) {
	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o700); err != nil {
		return nil, fmt.Errorf("ensure profiles dir: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(cfg.XrayProfilesDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("glob profiles: %w", err)
	}

	out := make([]DetailedProfile, 0, len(matches))
	for _, p := range matches {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		dp, perr := LoadProfile(cfg, name)
		if perr != nil {
			output.Warn(fmt.Sprintf("skip profile %q: %v", name, perr))
			continue
		}
		out = append(out, dp)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListProfiles enumerates cfg.XrayProfilesDir/*.json and returns the list-safe
// (masked) ProfileSummary rows, sorted by Name. It parses each profile exactly
// once through ListProfilesDetailed and projects every row via
// DetailedProfile.Summary (D-13: list never emits raw secrets). Profiles that
// fail to parse are warned + skipped — list never errors on a single bad
// profile. Active is decided by a single ReadActiveProfileName read compared
// against each profile name.
func ListProfiles(cfg config.Config) ([]ProfileSummary, error) {
	details, err := ListProfilesDetailed(cfg)
	if err != nil {
		return nil, err
	}

	activeName, _ := ReadActiveProfileName(cfg)

	out := make([]ProfileSummary, 0, len(details))
	for _, dp := range details {
		summary := dp.Summary()
		summary.Active = (dp.Name == activeName)
		out = append(out, summary)
	}
	return out, nil
}

// ReadActiveProfileName resolves cfg.XrayConfig as a symlink and returns the
// active profile name (basename with .json stripped).
//
//   - returns ("", os.ErrNotExist) if cfg.XrayConfig does not exist
//   - returns ("", error) if cfg.XrayConfig exists but is not a symlink
//     (Plan 22-04 migration handles the regular-file case before we get here)
func ReadActiveProfileName(cfg config.Config) (string, error) {
	info, err := os.Lstat(cfg.XrayConfig)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("lstat %s: %w", cfg.XrayConfig, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("%s is not a symlink; run migration first", cfg.XrayConfig)
	}
	target, err := os.Readlink(cfg.XrayConfig)
	if err != nil {
		return "", fmt.Errorf("readlink %s: %w", cfg.XrayConfig, err)
	}
	return strings.TrimSuffix(filepath.Base(target), ".json"), nil
}

// RemoveProfile deletes cfg.XrayProfilesDir/<name>.json after refusing to
// remove the currently-active profile (D-08 + PROXY-PROFILE-05 + threat
// T-22-active-delete). The active-profile check uses ReadActiveProfileName,
// which returns "" with no error when cfg.XrayConfig does not exist — that
// fresh-install branch lets a removal proceed without refusal. Plan 22-05.
//
// Behaviour:
//   - ValidateProfileName runs FIRST so reserved / regex-invalid names error
//     before any filesystem op is attempted (T-22-rm-injection).
//   - When name == active, returns
//     `cannot remove active profile %q (run `+"`"+`ws proxy profile use <other>`+"`"+` first)`
//     WITHOUT deleting the file.
//   - On a non-existent profile, returns a wrapped os.IsNotExist error so
//     callers can distinguish from real I/O failures.
func RemoveProfile(cfg config.Config, name string) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	active, _ := ReadActiveProfileName(cfg) // "" on missing/non-symlink — non-fatal here
	if active == name {
		return fmt.Errorf("cannot remove active profile %q (run `ws proxy profile use <other>` first)", name)
	}
	target := filepath.Join(cfg.XrayProfilesDir, name+".json")
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove profile %q at %s: %w", name, target, err)
	}
	return nil
}

// GenerateProfileConfig parses a proxy share URI and builds the full xray
// config for it, dispatching on URI scheme. New protocols plug in here.
// This is the single authoritative dispatch point; proxyengine.BuildConfig
// reuses it via xray.GenerateProfileConfig.
func GenerateProfileConfig(uri string) (*xrayconf.XrayConfig, error) {
	scheme, _, ok := strings.Cut(uri, "://")
	if !ok {
		return nil, fmt.Errorf("unsupported proxy URI scheme (want vless://, hysteria2://, or hy2://)")
	}
	switch strings.ToLower(scheme) {
	case "vless":
		parsed, err := vless.Parse(uri)
		if err != nil {
			return nil, err
		}
		cfg, err := vless.GenerateConfig(parsed, "proxy-1")
		if err != nil {
			return nil, fmt.Errorf("generate config: %w", err)
		}
		return cfg, nil
	case "hysteria2", "hy2":
		parsed, err := hysteria2.Parse(uri)
		if err != nil {
			return nil, err
		}
		if parsed.AllowInsecure && parsed.PinSHA256 == "" {
			output.Warn("hysteria2 'insecure' is unsupported on xray-core v26.2.6; ignoring. For a self-signed endpoint, pin the cert: add ?pinSHA256=<sha256> (run 'ws proxy doctor' to print it).")
		}
		cfg, err := hysteria2.GenerateConfig(parsed, "proxy-1")
		if err != nil {
			return nil, fmt.Errorf("generate config: %w", err)
		}
		return cfg, nil
	default:
		return nil, fmt.Errorf("unsupported proxy URI scheme (want vless://, hysteria2://, or hy2://)")
	}
}

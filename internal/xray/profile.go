package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/vless"
)

// ProfileSummary is the row shape used by `list` output (table + JSON).
// UUIDMasked is always masked here (D-13) — list never emits raw UUIDs.
type ProfileSummary struct {
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Transport  string `json:"transport"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Security   string `json:"security"`
	SNI        string `json:"sni,omitempty"`
	UUIDMasked string `json:"uuid"`
}

// AddProfile parses uri, copies routing rules from the currently-active
// profile (D-05), and writes cfg.XrayProfilesDir/<name>.json. Returns an
// error if name is reserved/invalid (ValidateProfileName), URI is invalid
// (vless.Parse), or target file exists and !force.
//
// Per RESEARCH §3: uses vless.GenerateConfig + manual json.MarshalIndent
// because the routing block needs to be overridden with the active profile's
// rules before write. Legacy node-append helpers are NEVER invoked here.
func AddProfile(cfg config.Config, name, uri string, force bool) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	parsed, err := vless.Parse(uri)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o755); err != nil {
		return fmt.Errorf("create profiles dir: %w", err)
	}

	target := filepath.Join(cfg.XrayProfilesDir, name+".json")
	if _, statErr := os.Stat(target); statErr == nil && !force {
		return fmt.Errorf("profile %q already exists at %s (use --force to overwrite)", name, target)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat target %s: %w", target, statErr)
	}

	targetCfg, err := vless.GenerateConfig(parsed, "proxy-1")
	if err != nil {
		return fmt.Errorf("generate config: %w", err)
	}

	// D-05: copy routing rules from the currently-active profile so per-host
	// rules (e.g. port:22 → direct) and balancer wiring persist across `add`.
	if active, readErr := os.Readlink(cfg.XrayConfig); readErr == nil {
		activePath := active
		if !filepath.IsAbs(active) {
			activePath = filepath.Join(filepath.Dir(cfg.XrayConfig), active)
		}
		if data, rerr := os.ReadFile(activePath); rerr == nil {
			var activeCfg vless.XrayConfig
			if uerr := json.Unmarshal(data, &activeCfg); uerr == nil {
				targetCfg.Routing = activeCfg.Routing
			}
		}
	}

	data, err := json.MarshalIndent(targetCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return fmt.Errorf("write profile %s: %w", target, err)
	}
	return nil
}

// ListProfiles enumerates cfg.XrayProfilesDir/*.json, resolves the active
// profile via os.Readlink(cfg.XrayConfig), and returns a slice sorted by
// Name. Profiles whose JSON cannot be parsed are logged via output.Warn
// (stderr) and skipped — list never errors on a single bad profile.
func ListProfiles(cfg config.Config) ([]ProfileSummary, error) {
	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure profiles dir: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(cfg.XrayProfilesDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("glob profiles: %w", err)
	}

	activeName, _ := ReadActiveProfileName(cfg)

	out := make([]ProfileSummary, 0, len(matches))
	for _, p := range matches {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		summary, perr := summarizeProfile(p, name)
		if perr != nil {
			output.Warn(fmt.Sprintf("skip profile %q: %v", name, perr))
			continue
		}
		summary.Active = (name == activeName)
		out = append(out, summary)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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

// summarizeProfile reads a profile JSON and extracts the first VLESS outbound
// into a ProfileSummary (UUID masked per D-13).
func summarizeProfile(path, name string) (ProfileSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfileSummary{}, fmt.Errorf("read: %w", err)
	}
	var xc vless.XrayConfig
	if err := json.Unmarshal(data, &xc); err != nil {
		return ProfileSummary{}, fmt.Errorf("parse: %w", err)
	}
	s := ProfileSummary{Name: name}
	for _, ob := range xc.Outbounds {
		if ob.Protocol != "vless" {
			continue
		}
		var settings struct {
			Vnext []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
				Users   []struct {
					ID string `json:"id"`
				} `json:"users"`
			} `json:"vnext"`
		}
		if err := json.Unmarshal(ob.Settings, &settings); err != nil {
			return ProfileSummary{}, fmt.Errorf("parse settings: %w", err)
		}
		if len(settings.Vnext) == 0 {
			return ProfileSummary{}, fmt.Errorf("no vnext in VLESS outbound")
		}
		s.Address = settings.Vnext[0].Address
		s.Port = settings.Vnext[0].Port
		if len(settings.Vnext[0].Users) > 0 {
			s.UUIDMasked = MaskUUID(settings.Vnext[0].Users[0].ID)
		} else {
			s.UUIDMasked = MaskUUID("")
		}

		var ss struct {
			Network         string `json:"network"`
			Security        string `json:"security"`
			RealitySettings struct {
				ServerName string `json:"serverName"`
			} `json:"realitySettings"`
			TLSSettings struct {
				ServerName string `json:"serverName"`
			} `json:"tlsSettings"`
		}
		if len(ob.StreamSettings) > 0 {
			if err := json.Unmarshal(ob.StreamSettings, &ss); err != nil {
				return ProfileSummary{}, fmt.Errorf("parse stream: %w", err)
			}
		}
		s.Transport = ss.Network
		s.Security = ss.Security
		switch ss.Security {
		case "reality":
			s.SNI = ss.RealitySettings.ServerName
		case "tls":
			s.SNI = ss.TLSSettings.ServerName
		}
		return s, nil
	}
	return ProfileSummary{}, fmt.Errorf("no VLESS outbound")
}

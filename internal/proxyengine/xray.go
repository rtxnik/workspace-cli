package proxyengine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/hysteria2"
	"github.com/rtxnik/workspace-cli/internal/vless"
	"github.com/rtxnik/workspace-cli/internal/xray"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// XrayEngine is the xray-core backend behind the Engine seam.
//
// Wiring choice (b) from the task brief: BuildConfig scheme-dispatches the URI
// here and calls the protocol builders (vless / hysteria2) directly, then
// marshals the neutral xrayconf.XrayConfig. It does NOT route through
// xray.generateProfileConfig — that unexported helper stays as-is so existing
// callers (xray.AddProfile) are untouched and the xray package never imports
// proxyengine. Validate delegates to xray.ValidateProfile, giving a single
// one-way edge proxyengine → xray with no cycle.
type XrayEngine struct{}

// BuildConfig parses p.URI, builds the xray config for the matching protocol,
// and returns it as indented JSON (two-space indent, matching what AddProfile
// writes to disk). The "proxy-1" outbound tag mirrors generateProfileConfig so
// produced configs are shape-identical to the existing path.
func (XrayEngine) BuildConfig(p Profile) ([]byte, error) {
	cfg, err := buildXrayConfig(p.URI)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	return data, nil
}

// Validate delegates to the existing backend validator (xray run -test), which
// shells into the dev-proxy container. Kept here so the seam owns both halves
// of the build → validate contract even though the implementation lives in xray.
func (XrayEngine) Validate(cfg config.Config, profileName string) error {
	return xray.ValidateProfile(cfg, profileName)
}

// buildXrayConfig dispatches a share URI to the matching protocol builder. This
// mirrors xray.generateProfileConfig's scheme switch; the two are kept in sync
// deliberately rather than sharing the unexported helper to avoid an
// xray ↔ proxyengine import cycle (see XrayEngine doc).
func buildXrayConfig(uri string) (*xrayconf.XrayConfig, error) {
	switch {
	case strings.HasPrefix(uri, "vless://"):
		parsed, err := vless.Parse(uri)
		if err != nil {
			return nil, err
		}
		cfg, err := vless.GenerateConfig(parsed, "proxy-1")
		if err != nil {
			return nil, fmt.Errorf("generate config: %w", err)
		}
		return cfg, nil
	case strings.HasPrefix(uri, "hysteria2://"), strings.HasPrefix(uri, "hy2://"):
		parsed, err := hysteria2.Parse(uri)
		if err != nil {
			return nil, err
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

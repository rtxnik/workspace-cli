package proxyengine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/xray"
)

// XrayEngine is the xray-core backend behind the Engine seam.
//
// BuildConfig routes through xray.GenerateProfileConfig — the single
// authoritative URI-scheme dispatch. Validate delegates to xray.ValidateProfile.
// Both give a one-way edge proxyengine → xray with no import cycle.
type XrayEngine struct{}

// BuildConfig parses p.URI via xray.GenerateProfileConfig and returns the
// resulting config as indented JSON (two-space indent, matching AddProfile).
func (XrayEngine) BuildConfig(p Profile) ([]byte, error) {
	xc, err := xray.GenerateProfileConfig(p.URI)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(xc, "", "  ")
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

// Probe performs a live tunnel-connectivity check. It fetches the host's
// direct egress IP (plain HTTP GET baseline) and the container's egress IP
// (docker exec curl), then compares them. Tunneled=true only when both are
// valid, non-empty, and different — proving traffic exits via the tunnel.
func (XrayEngine) Probe(cfg config.Config) (ProbeResult, error) {
	start := time.Now()

	directIP, err := fetchDirectIP(context.Background())
	if err != nil {
		return ProbeResult{}, fmt.Errorf("direct baseline: %w", err)
	}

	proxiedIP, err := fetchProxiedIP(cfg)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("proxied egress: %w", err)
	}

	return ProbeResult{
		DirectIP:  directIP,
		ProxiedIP: proxiedIP,
		Tunneled:  tunneled(directIP, proxiedIP),
		Latency:   time.Since(start),
	}, nil
}

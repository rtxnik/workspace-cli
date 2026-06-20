package hysteria2

import (
	"encoding/json"
	"fmt"

	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// BuildOutbound builds the Xray-core outbound for a Hysteria2 profile. The wire
// protocol is "hysteria" with version 2 (Xray-core's hysteria2 implementation,
// PR XTLS/Xray-core#5508). The auth password lives in hysteriaSettings, NOT in
// the protocol-level settings; finalmask (salamander) is a sibling of
// hysteriaSettings under streamSettings.
func BuildOutbound(cfg Config, tag string) (xrayconf.Outbound, error) {
	settings, err := json.Marshal(map[string]any{
		"version": 2,
		"address": cfg.Address,
		"port":    cfg.Port,
	})
	if err != nil {
		return xrayconf.Outbound{}, fmt.Errorf("marshal settings: %w", err)
	}

	tls := map[string]any{
		"serverName":  cfg.SNI,
		"alpn":        cfg.ALPN,
		"fingerprint": cfg.Fingerprint,
	}
	if cfg.PinSHA256 != "" {
		tls["pinnedPeerCertSha256"] = cfg.PinSHA256
	}
	// allowInsecure is intentionally never emitted: xray-core v26.2.6 TLSConfig.Build()
	// hard-errors on allowInsecure:true after 2026-06-01. Use pinnedPeerCertSha256 for
	// self-signed endpoints.
	hy := map[string]any{"version": 2, "auth": cfg.Auth}
	if cfg.HopPorts != "" {
		udphop := map[string]any{"port": cfg.HopPorts}
		if cfg.HopInterval > 0 {
			udphop["interval"] = cfg.HopInterval
		}
		hy["udphop"] = udphop
	}
	if cfg.Up != "" {
		hy["up"] = cfg.Up
	}
	if cfg.Down != "" {
		hy["down"] = cfg.Down
	}
	if cfg.Congestion != "" {
		hy["congestion"] = cfg.Congestion
	}
	stream := map[string]any{
		"network":          "hysteria",
		"security":         "tls",
		"tlsSettings":      tls,
		"hysteriaSettings": hy,
	}
	if cfg.Obfs == "salamander" {
		stream["finalmask"] = map[string]any{
			"udp": []map[string]any{
				{
					"type":     "salamander",
					"settings": map[string]any{"password": cfg.ObfsPassword},
				},
			},
		}
	}
	streamSettings, err := json.Marshal(stream)
	if err != nil {
		return xrayconf.Outbound{}, fmt.Errorf("marshal streamSettings: %w", err)
	}

	return xrayconf.Outbound{
		Tag:            tag,
		Protocol:       "hysteria",
		Settings:       json.RawMessage(settings),
		StreamSettings: json.RawMessage(streamSettings),
	}, nil
}

// GenerateConfig builds a full xray config for a Hysteria2 profile, reusing the
// shared transparent-proxy scaffold from internal/xrayconf.
func GenerateConfig(cfg Config, tag string) (*xrayconf.XrayConfig, error) {
	ob, err := BuildOutbound(cfg, tag)
	if err != nil {
		return nil, err
	}
	return xrayconf.AssembleConfig(ob), nil
}

// WriteNewConfig writes a fresh xray config for a Hysteria2 profile to path.
func WriteNewConfig(path string, cfg Config) error {
	xc, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		return err
	}
	return xrayconf.WriteConfig(path, xc)
}

package hysteria2

import (
	"encoding/json"
	"fmt"

	"github.com/rtxnik/workspace-cli/internal/vless"
)

// BuildOutbound builds the Xray-core outbound for a Hysteria2 profile. The wire
// protocol is "hysteria" with version 2 (Xray-core's hysteria2 implementation,
// PR XTLS/Xray-core#5508). The auth password lives in hysteriaSettings, NOT in
// the protocol-level settings; finalmask (salamander) is a sibling of
// hysteriaSettings under streamSettings.
func BuildOutbound(cfg Config, tag string) (vless.Outbound, error) {
	settings, err := json.Marshal(map[string]any{
		"version": 2,
		"address": cfg.Address,
		"port":    cfg.Port,
	})
	if err != nil {
		return vless.Outbound{}, fmt.Errorf("marshal settings: %w", err)
	}

	stream := map[string]any{
		"network":  "hysteria",
		"security": "tls",
		"tlsSettings": map[string]any{
			"serverName":    cfg.SNI,
			"allowInsecure": cfg.AllowInsecure,
			"alpn":          cfg.ALPN,
			"fingerprint":   cfg.Fingerprint,
		},
		"hysteriaSettings": map[string]any{
			"version": 2,
			"auth":    cfg.Auth,
		},
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
		return vless.Outbound{}, fmt.Errorf("marshal streamSettings: %w", err)
	}

	return vless.Outbound{
		Tag:            tag,
		Protocol:       "hysteria",
		Settings:       json.RawMessage(settings),
		StreamSettings: json.RawMessage(streamSettings),
	}, nil
}

// GenerateConfig builds a full xray config for a Hysteria2 profile, reusing the
// shared transparent-proxy scaffold from internal/vless.
func GenerateConfig(cfg Config, tag string) (*vless.XrayConfig, error) {
	ob, err := BuildOutbound(cfg, tag)
	if err != nil {
		return nil, err
	}
	return vless.AssembleConfig(ob), nil
}

// WriteNewConfig writes a fresh xray config for a Hysteria2 profile to path.
func WriteNewConfig(path string, cfg Config) error {
	xc, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		return err
	}
	return vless.WriteConfig(path, xc)
}

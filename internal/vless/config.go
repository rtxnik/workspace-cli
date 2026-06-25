package vless

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// GenerateConfig creates a new xray config from a parsed VLESS URI.
func GenerateConfig(cfg VLESSConfig, tag string) (*xrayconf.XrayConfig, error) {
	outbound, err := buildOutbound(cfg, tag)
	if err != nil {
		return nil, err
	}
	return xrayconf.AssembleConfig(outbound), nil
}

// WriteNewConfig creates a new xray config file from a VLESS URI.
func WriteNewConfig(path string, cfg VLESSConfig) error {
	xray, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		return err
	}
	return xrayconf.WriteConfig(path, xray)
}

// AddNode adds a new VLESS outbound to an existing xray config.
func AddNode(path string, cfg VLESSConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var xray xrayconf.XrayConfig
	if err := json.Unmarshal(data, &xray); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Determine next proxy tag number.
	nextNum := 1
	for _, ob := range xray.Outbounds {
		if ob.Protocol == "vless" {
			nextNum++
		}
	}
	tag := fmt.Sprintf("proxy-%d", nextNum)

	outbound, err := buildOutbound(cfg, tag)
	if err != nil {
		return err
	}

	// Insert before the "direct" outbound.
	var newOutbounds []xrayconf.Outbound
	for _, ob := range xray.Outbounds {
		if ob.Tag == "direct" {
			newOutbounds = append(newOutbounds, outbound)
		}
		newOutbounds = append(newOutbounds, ob)
	}
	xray.Outbounds = newOutbounds

	return xrayconf.WriteConfig(path, &xray)
}

func buildOutbound(cfg VLESSConfig, tag string) (xrayconf.Outbound, error) {
	settings, err := json.Marshal(map[string]any{
		"vnext": []map[string]any{
			{
				"address": cfg.Address,
				"port":    cfg.Port,
				"users": []map[string]any{
					{
						"id":         cfg.UUID,
						"encryption": cfg.Encryption,
						"flow":       cfg.Flow,
					},
				},
			},
		},
	})
	if err != nil {
		return xrayconf.Outbound{}, fmt.Errorf("marshal settings: %w", err)
	}

	stream, err := buildStreamSettings(cfg)
	if err != nil {
		return xrayconf.Outbound{}, err
	}

	return xrayconf.Outbound{
		Tag:            tag,
		Protocol:       "vless",
		Settings:       json.RawMessage(settings),
		StreamSettings: json.RawMessage(stream),
	}, nil
}

func buildStreamSettings(cfg VLESSConfig) ([]byte, error) {
	ss := map[string]any{
		"network":  cfg.Network,
		"security": cfg.Security,
	}

	// Security settings.
	switch cfg.Security {
	case "reality":
		ss["realitySettings"] = map[string]any{
			"serverName":  cfg.SNI,
			"fingerprint": cfg.Fp,
			"publicKey":   cfg.PublicKey,
			"shortId":     cfg.ShortID,
			"spiderX":     cfg.SpiderX,
		}
	case "tls":
		ss["tlsSettings"] = map[string]any{
			"serverName":  cfg.SNI,
			"fingerprint": cfg.Fp,
		}
	}

	// Transport settings.
	switch cfg.Network {
	case "tcp":
		if cfg.HeaderType == "http" {
			ss["tcpSettings"] = map[string]any{
				"header": map[string]any{
					"type": "http",
					"request": map[string]any{
						"path": []string{cfg.Path},
						"headers": map[string]any{
							"Host": []string{cfg.Host},
						},
					},
				},
			}
		}
	case "ws":
		ss["wsSettings"] = map[string]any{
			"path":    cfg.Path,
			"headers": map[string]any{"Host": cfg.Host},
		}
	case "grpc":
		ss["grpcSettings"] = map[string]any{
			"serviceName": cfg.ServiceName,
			"multiMode":   false,
		}
	case "h2":
		// xray v26 removed the HTTP/2 transport ("migrated to XHTTP stream-one
		// H2 & H3"), rejecting network:"h2". Emit the equivalent XHTTP
		// stream-one config so an imported h2 URI loads on a current xray.
		ss["network"] = "xhttp"
		xhttp := map[string]any{
			"mode": "stream-one",
			"path": cfg.Path,
		}
		if cfg.Host != "" {
			xhttp["host"] = cfg.Host
		}
		ss["xhttpSettings"] = xhttp
	case "httpupgrade":
		ss["httpupgradeSettings"] = map[string]any{
			"path": cfg.Path,
			"host": cfg.Host,
		}
	case "xhttp":
		ss["xhttpSettings"] = map[string]any{
			"path": cfg.Path,
			"mode": cfg.Mode,
		}
	}

	return json.Marshal(ss)
}

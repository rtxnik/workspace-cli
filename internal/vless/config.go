package vless

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// XrayConfig represents a full xray configuration.
type XrayConfig struct {
	Log       LogConfig       `json:"log"`
	Inbounds  []Inbound       `json:"inbounds"`
	Outbounds []Outbound      `json:"outbounds"`
	Routing   RoutingConfig   `json:"routing"`
}

type LogConfig struct {
	Level string `json:"loglevel"`
}

type Inbound struct {
	Tag      string         `json:"tag"`
	Port     int            `json:"port"`
	Protocol string         `json:"protocol"`
	Settings InboundSetting `json:"settings"`
	Sniffing *Sniffing      `json:"sniffing,omitempty"`
}

type InboundSetting struct {
	Network        string `json:"network"`
	FollowRedirect bool   `json:"followRedirect"`
}

type Sniffing struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride"`
}

type Outbound struct {
	Tag            string          `json:"tag"`
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings json.RawMessage `json:"streamSettings,omitempty"`
}

type RoutingConfig struct {
	DomainStrategy string            `json:"domainStrategy"`
	Balancers      []Balancer        `json:"balancers,omitempty"`
	Rules          []json.RawMessage `json:"rules"`
}

type Balancer struct {
	Tag      string           `json:"tag"`
	Selector []string         `json:"selector"`
	Strategy BalancerStrategy `json:"strategy"`
}

type BalancerStrategy struct {
	Type string `json:"type"`
}

// GenerateConfig creates a new xray config from a parsed VLESS URI.
func GenerateConfig(cfg VLESSConfig, tag string) (*XrayConfig, error) {
	outbound, err := buildOutbound(cfg, tag)
	if err != nil {
		return nil, err
	}

	return &XrayConfig{
		Log: LogConfig{Level: "warning"},
		Inbounds: []Inbound{
			{
				Tag:      "transparent",
				Port:     12345,
				Protocol: "dokodemo-door",
				Settings: InboundSetting{
					Network:        "tcp,udp",
					FollowRedirect: true,
				},
				Sniffing: &Sniffing{
					Enabled:      true,
					DestOverride: []string{"http", "tls", "quic"},
				},
			},
		},
		Outbounds: []Outbound{
			outbound,
			{
				Tag:      "direct",
				Protocol: "freedom",
				Settings: json.RawMessage(`{}`),
			},
		},
		Routing: RoutingConfig{
			DomainStrategy: "IPIfNonMatch",
			Balancers: []Balancer{
				{
					Tag:      "proxy-balancer",
					Selector: []string{"proxy-"},
					Strategy: BalancerStrategy{Type: "roundRobin"},
				},
			},
			Rules: []json.RawMessage{
				json.RawMessage(`{"type":"field","ip":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","127.0.0.0/8"],"outboundTag":"direct"}`),
				json.RawMessage(`{"type":"field","network":"tcp,udp","balancerTag":"proxy-balancer"}`),
			},
		},
	}, nil
}

// WriteNewConfig creates a new xray config file from a VLESS URI.
func WriteNewConfig(path string, cfg VLESSConfig) error {
	xray, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		return err
	}
	return writeConfig(path, xray)
}

// AddNode adds a new VLESS outbound to an existing xray config.
func AddNode(path string, cfg VLESSConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var xray XrayConfig
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
	var newOutbounds []Outbound
	for _, ob := range xray.Outbounds {
		if ob.Tag == "direct" {
			newOutbounds = append(newOutbounds, outbound)
		}
		newOutbounds = append(newOutbounds, ob)
	}
	xray.Outbounds = newOutbounds

	return writeConfig(path, &xray)
}

func writeConfig(path string, xray *XrayConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(xray, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o644)
}

func buildOutbound(cfg VLESSConfig, tag string) (Outbound, error) {
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
		return Outbound{}, fmt.Errorf("marshal settings: %w", err)
	}

	stream, err := buildStreamSettings(cfg)
	if err != nil {
		return Outbound{}, err
	}

	return Outbound{
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
		ss["httpSettings"] = map[string]any{
			"host": []string{cfg.Host},
			"path": cfg.Path,
		}
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

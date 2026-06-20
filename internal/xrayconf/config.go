package xrayconf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// XrayConfig represents a full xray configuration.
type XrayConfig struct {
	Log       LogConfig     `json:"log"`
	Inbounds  []Inbound     `json:"inbounds"`
	Outbounds []Outbound    `json:"outbounds"`
	Routing   RoutingConfig `json:"routing"`
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

// AssembleConfig wraps a single proxy outbound in the standard transparent-proxy
// scaffold: a dokodemo-door inbound on :12345, a freedom "direct" outbound, and
// the proxy-balancer routing block. The proxy outbound's Tag must start with
// "proxy-" to match the balancer selector. Shared by every protocol's config
// builder (vless, hysteria2, ...).
func AssembleConfig(proxy Outbound) *XrayConfig {
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
			proxy,
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
	}
}

// WriteConfig marshals xc (indented) and writes it to path, creating parent
// dirs. Exported so sibling protocol packages can reuse the writer.
func WriteConfig(path string, xc *XrayConfig) error {
	return writeConfig(path, xc)
}

func writeConfig(path string, xray *XrayConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(xray, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o600)
}

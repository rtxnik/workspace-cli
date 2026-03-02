package vless

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// VLESSConfig holds parsed VLESS URI parameters.
type VLESSConfig struct {
	UUID       string
	Address    string
	Port       int
	Encryption string
	Flow       string

	// Transport
	Network string // tcp, ws, grpc, h2, httpupgrade, xhttp

	// Security
	Security  string // reality, tls, none
	SNI       string
	PublicKey string
	ShortID   string
	Fp        string // fingerprint
	SpiderX   string

	// Network-specific
	Path        string
	Host        string
	ServiceName string
	Mode        string // xhttp mode

	// TCP-HTTP header
	HeaderType string

	// Remark
	Remark string
}

// Parse parses a VLESS URI (vless://UUID@HOST:PORT?params#remark) into a VLESSConfig.
func Parse(uri string) (VLESSConfig, error) {
	if !strings.HasPrefix(uri, "vless://") {
		return VLESSConfig{}, fmt.Errorf("not a VLESS URI: must start with vless://")
	}

	u, err := url.Parse(uri)
	if err != nil {
		return VLESSConfig{}, fmt.Errorf("parse URI: %w", err)
	}

	if u.User == nil {
		return VLESSConfig{}, fmt.Errorf("missing UUID in URI")
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return VLESSConfig{}, fmt.Errorf("invalid port %q: %w", u.Port(), err)
	}

	q := u.Query()

	cfg := VLESSConfig{
		UUID:       u.User.Username(),
		Address:    u.Hostname(),
		Port:       port,
		Encryption: q.Get("encryption"),
		Flow:       q.Get("flow"),

		Network:  q.Get("type"),
		Security: q.Get("security"),
		SNI:      q.Get("sni"),
		Fp:       q.Get("fp"),

		PublicKey: q.Get("pbk"),
		ShortID:  q.Get("sid"),
		SpiderX:  q.Get("spx"),

		Path:        q.Get("path"),
		Host:        q.Get("host"),
		ServiceName: q.Get("serviceName"),
		Mode:        q.Get("mode"),
		HeaderType:  q.Get("headerType"),

		Remark: u.Fragment,
	}

	// Defaults.
	if cfg.Encryption == "" {
		cfg.Encryption = "none"
	}
	if cfg.Network == "" {
		cfg.Network = "tcp"
	}
	if cfg.Security == "" {
		cfg.Security = "none"
	}
	if cfg.Fp == "" {
		cfg.Fp = "chrome"
	}

	return cfg, nil
}

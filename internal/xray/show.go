package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// DetailedProfile is the row shape used by `show` (and full-detail `list`).
// Raw fields (UUID/PublicKey/ShortID/SpiderX/Auth/ObfsPassword) are unmasked;
// callers MUST apply MaskUUID/MaskShort before rendering unless --reveal is set (D-13).
type DetailedProfile struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol,omitempty"` // "" vless, "hysteria2" hy2
	Transport     string `json:"transport"`
	Address       string `json:"address"`
	Port          int    `json:"port"`
	Security      string `json:"security"`
	SNI           string `json:"sni,omitempty"`
	UUID          string `json:"uuid,omitempty"`
	PublicKey     string `json:"publicKey,omitempty"`
	ShortID       string `json:"shortId,omitempty"`
	SpiderX       string `json:"spiderX,omitempty"`
	Auth          string `json:"auth,omitempty"`
	Obfs          string `json:"obfs,omitempty"`
	ObfsPassword  string `json:"obfsPassword,omitempty"`
	AllowInsecure bool   `json:"allowInsecure,omitempty"`
	Active        bool   `json:"active"`
}

// MaskUUID preserves first 8 hex chars of a UUID then masks the rest.
// Returns "" for empty input; "****" if UUID length != 36 (per RESEARCH §8).
func MaskUUID(uuid string) string {
	if uuid == "" {
		return ""
	}
	if len(uuid) != 36 {
		return "****"
	}
	return uuid[:8] + "-****-****-****-************"
}

// MaskShort returns "****" for non-empty input; "" for empty (per RESEARCH §8).
func MaskShort(s string) string {
	if s == "" {
		return ""
	}
	return "****"
}

// LoadProfile reads cfg.XrayProfilesDir/<name>.json and extracts the first
// proxy outbound (vless or hysteria). Active is set by comparing <name> against
// the symlink target's basename.
func LoadProfile(cfg config.Config, name string) (DetailedProfile, error) {
	if err := ValidateProfileName(name); err != nil {
		return DetailedProfile{}, err
	}
	profilePath := filepath.Join(cfg.XrayProfilesDir, name+".json")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return DetailedProfile{}, fmt.Errorf("read profile %q: %w", name, err)
	}
	var xc xrayconf.XrayConfig
	if err := json.Unmarshal(data, &xc); err != nil {
		return DetailedProfile{}, fmt.Errorf("parse profile %q: %w", name, err)
	}

	dp := DetailedProfile{Name: name}

	var found bool
	for _, ob := range xc.Outbounds {
		switch ob.Protocol {
		case "vless":
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
				return DetailedProfile{}, fmt.Errorf("parse outbound settings in profile %q: %w", name, err)
			}
			if len(settings.Vnext) == 0 {
				return DetailedProfile{}, fmt.Errorf("no vnext in VLESS outbound in profile %q", name)
			}
			dp.Address = settings.Vnext[0].Address
			dp.Port = settings.Vnext[0].Port
			if len(settings.Vnext[0].Users) > 0 {
				dp.UUID = settings.Vnext[0].Users[0].ID
			}
			var ss struct {
				Network         string `json:"network"`
				Security        string `json:"security"`
				RealitySettings struct {
					ServerName string `json:"serverName"`
					PublicKey  string `json:"publicKey"`
					ShortID    string `json:"shortId"`
					SpiderX    string `json:"spiderX"`
				} `json:"realitySettings"`
				TLSSettings struct {
					ServerName string `json:"serverName"`
				} `json:"tlsSettings"`
			}
			if len(ob.StreamSettings) > 0 {
				if err := json.Unmarshal(ob.StreamSettings, &ss); err != nil {
					return DetailedProfile{}, fmt.Errorf("parse stream settings in profile %q: %w", name, err)
				}
			}
			dp.Transport = ss.Network
			dp.Security = ss.Security
			switch ss.Security {
			case "reality":
				dp.SNI = ss.RealitySettings.ServerName
				dp.PublicKey = ss.RealitySettings.PublicKey
				dp.ShortID = ss.RealitySettings.ShortID
				dp.SpiderX = ss.RealitySettings.SpiderX
			case "tls":
				dp.SNI = ss.TLSSettings.ServerName
			}
			found = true
		case "hysteria":
			if err := loadHysteria(&dp, ob); err != nil {
				return DetailedProfile{}, err
			}
			found = true
		default:
			continue
		}
		break // first proxy outbound only
	}

	if !found {
		return DetailedProfile{}, fmt.Errorf("no proxy outbound in profile %q", name)
	}

	active, _ := ReadActiveProfileName(cfg)
	dp.Active = (active == name)

	return dp, nil
}

// loadHysteria fills dp from a hysteria (hy2) outbound, including the raw
// secrets (auth, obfs password). Callers MUST mask via MaskShort unless
// --reveal is set (D-13). UUID/REALITY fields stay empty.
func loadHysteria(dp *DetailedProfile, ob xrayconf.Outbound) error {
	var settings struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
	}
	if err := json.Unmarshal(ob.Settings, &settings); err != nil {
		return fmt.Errorf("parse hysteria settings: %w", err)
	}
	dp.Protocol = "hysteria2"
	dp.Transport = "hysteria"
	dp.Address = settings.Address
	dp.Port = settings.Port

	var ss struct {
		Security    string `json:"security"`
		TLSSettings struct {
			ServerName    string `json:"serverName"`
			AllowInsecure bool   `json:"allowInsecure"`
		} `json:"tlsSettings"`
		HysteriaSettings struct {
			Auth string `json:"auth"`
		} `json:"hysteriaSettings"`
		FinalMask struct {
			Udp []struct {
				Type     string `json:"type"`
				Settings struct {
					Password string `json:"password"`
				} `json:"settings"`
			} `json:"udp"`
		} `json:"finalmask"`
	}
	if len(ob.StreamSettings) > 0 {
		if err := json.Unmarshal(ob.StreamSettings, &ss); err != nil {
			return fmt.Errorf("parse hysteria stream: %w", err)
		}
	}
	dp.Security = ss.Security
	dp.SNI = ss.TLSSettings.ServerName
	dp.AllowInsecure = ss.TLSSettings.AllowInsecure
	dp.Auth = ss.HysteriaSettings.Auth
	for _, m := range ss.FinalMask.Udp {
		if m.Type == "salamander" {
			dp.Obfs = "salamander"
			dp.ObfsPassword = m.Settings.Password
			break
		}
	}
	return nil
}

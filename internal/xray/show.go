package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/vless"
)

// DetailedProfile is the row shape used by `show` (and full-detail `list`).
// Raw fields (UUID/PublicKey/ShortID/SpiderX) are unmasked; callers MUST apply
// MaskUUID/MaskShort before rendering unless --reveal is set (D-13).
type DetailedProfile struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Address   string `json:"address"`
	Port      int    `json:"port"`
	Security  string `json:"security"`
	SNI       string `json:"sni,omitempty"`
	UUID      string `json:"uuid"`
	PublicKey string `json:"publicKey,omitempty"`
	ShortID   string `json:"shortId,omitempty"`
	SpiderX   string `json:"spiderX,omitempty"`
	Active    bool   `json:"active"`
}

// MaskUUID preserves first 8 hex chars of a UUID then masks the rest.
// Returns "****" if UUID length != 36 (per RESEARCH §8).
func MaskUUID(uuid string) string {
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
// VLESS outbound. Returns DetailedProfile; Active is determined by comparing
// <name> against the symlink target's basename.
func LoadProfile(cfg config.Config, name string) (DetailedProfile, error) {
	if err := ValidateProfileName(name); err != nil {
		return DetailedProfile{}, err
	}
	profilePath := filepath.Join(cfg.XrayProfilesDir, name+".json")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return DetailedProfile{}, fmt.Errorf("read profile %q: %w", name, err)
	}
	var xc vless.XrayConfig
	if err := json.Unmarshal(data, &xc); err != nil {
		return DetailedProfile{}, fmt.Errorf("parse profile %q: %w", name, err)
	}

	dp := DetailedProfile{Name: name}

	// Find first VLESS outbound.
	var found bool
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
		break // first VLESS outbound only
	}

	if !found {
		return DetailedProfile{}, fmt.Errorf("no VLESS outbound in profile %q", name)
	}

	active, _ := ReadActiveProfileName(cfg)
	dp.Active = (active == name)

	return dp, nil
}

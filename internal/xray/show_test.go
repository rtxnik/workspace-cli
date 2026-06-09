package xray

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
)

func TestMaskUUID(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", "****"},
		{"too short", "abc", "****"},
		{"not a uuid", "not-a-uuid", "****"},
		{"35 chars", "12345678-1234-1234-1234-12345678901", "****"},
		{"37 chars", "12345678-1234-1234-1234-1234567890123", "****"},
		{"valid lowercase", "12345678-1234-1234-1234-123456789012", "12345678-****-****-****-************"},
		{"valid hex first group", "abcdef01-1234-1234-1234-123456789012", "abcdef01-****-****-****-************"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskUUID(tc.in); got != tc.want {
				t.Errorf("MaskUUID(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMaskShort(t *testing.T) {
	if got := MaskShort(""); got != "" {
		t.Errorf("MaskShort(\"\") = %q; want \"\"", got)
	}
	if got := MaskShort("secret"); got != "****" {
		t.Errorf("MaskShort(\"secret\") = %q; want \"****\"", got)
	}
	if got := MaskShort("x"); got != "****" {
		t.Errorf("MaskShort(\"x\") = %q; want \"****\"", got)
	}
}

func TestLoadProfileHysteria2(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{XrayProfilesDir: filepath.Join(dir, "profiles"), XrayConfig: filepath.Join(dir, "config.json")}
	if err := os.MkdirAll(cfg.XrayProfilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[
		{"tag":"proxy-1","protocol":"hysteria","settings":{"version":2,"address":"example.com","port":443},
		 "streamSettings":{"network":"hysteria","security":"tls",
		   "tlsSettings":{"serverName":"example.com","allowInsecure":false},
		   "hysteriaSettings":{"version":2,"auth":"AUTHREDACTED"},
		   "finalmask":{"udp":[{"type":"salamander","settings":{"password":"OBFSREDACTED"}}]}}},
		{"tag":"direct","protocol":"freedom","settings":{}}],"routing":{"domainStrategy":"IPIfNonMatch","rules":[]}}`
	if err := os.WriteFile(filepath.Join(cfg.XrayProfilesDir, "hy2.json"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}

	dp, err := LoadProfile(cfg, "hy2")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if dp.Protocol != "hysteria2" || dp.Transport != "hysteria" {
		t.Errorf("protocol/transport = %s/%s", dp.Protocol, dp.Transport)
	}
	if dp.Address != "example.com" || dp.Port != 443 {
		t.Errorf("address/port = %s/%d", dp.Address, dp.Port)
	}
	if dp.SNI != "example.com" || dp.Security != "tls" {
		t.Errorf("sni/security = %s/%s", dp.SNI, dp.Security)
	}
	if dp.Auth != "AUTHREDACTED" {
		t.Errorf("auth = %q", dp.Auth)
	}
	if dp.Obfs != "salamander" || dp.ObfsPassword != "OBFSREDACTED" {
		t.Errorf("obfs = %q/%q", dp.Obfs, dp.ObfsPassword)
	}
	if dp.UUID != "" {
		t.Errorf("hy2 UUID = %q, want empty", dp.UUID)
	}
}

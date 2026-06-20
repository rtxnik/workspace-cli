package hysteria2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// zeroPinHex is a 64-character hex string representing 32 zero bytes,
// accepted by normalizePinSHA256 and used as a placeholder in golden tests.
const zeroPinHex = "0000000000000000000000000000000000000000000000000000000000000000"

// goldenCase describes one golden test fixture.
type goldenCase struct {
	name string
	uri  string
	file string
}

var goldenCases = []goldenCase{
	{
		name: "base",
		uri:  "hy2://pw@h.example:443?sni=h.example",
		file: "base.golden.json",
	},
	{
		name: "obfs",
		uri:  "hy2://pw@h.example:443?sni=h.example&obfs=salamander&obfs-password=OBFS",
		file: "obfs.golden.json",
	},
	{
		name: "pin",
		uri:  "hy2://pw@h.example:443?sni=h.example&pinSHA256=" + zeroPinHex,
		file: "pin.golden.json",
	},
	{
		name: "udphop",
		uri:  "hy2://pw@h.example:443,5000-6000?sni=h.example&hopInterval=30&up=50mbps&down=200mbps&congestion=brutal",
		file: "udphop.golden.json",
	},
}

// TestHysteriaGolden compares the JSON output of GenerateConfig against
// committed golden files for 4 representative URI shapes. Run with
// UPDATE_GOLDEN=1 to regenerate the golden files from current output.
func TestHysteriaGolden(t *testing.T) {
	for _, tc := range goldenCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(tc.uri)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.uri, err)
			}
			xc, err := GenerateConfig(cfg, "proxy-1")
			if err != nil {
				t.Fatalf("GenerateConfig: %v", err)
			}
			got, err := json.MarshalIndent(xc, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			goldenPath := filepath.Join("testdata", tc.file)

			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("golden updated: %s", goldenPath)
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to generate)", goldenPath, err)
			}

			if string(got) != string(want) {
				t.Errorf("GenerateConfig output differs from golden %s.\ngot:\n%s\nwant:\n%s", tc.file, got, want)
			}
		})
	}
}

// TestHysteriaStreamKeysAllowed asserts that the hy2 outbound streamSettings
// top-level keys are a subset of the allowed set. A stray or typo'd key (e.g.
// "hysteriaOSsettings" instead of "hysteriaSettings") would be silently ignored
// by xray-core's JSON loader, producing a tunnel with no auth — this guard
// catches that class of mistake at test time.
func TestHysteriaStreamKeysAllowed(t *testing.T) {
	allowed := map[string]bool{
		"network":          true,
		"security":         true,
		"tlsSettings":      true,
		"hysteriaSettings": true,
		"finalmask":        true,
	}

	// Use salamander+pin so all optional keys are present in one shot.
	cfg, err := Parse("hysteria2://pw@h.example:443?obfs=salamander&obfs-password=OBFS&pinSHA256=" + zeroPinHex)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	xc, err := GenerateConfig(cfg, "proxy-1")
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	var ss map[string]json.RawMessage
	if err := json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss); err != nil {
		t.Fatalf("unmarshal streamSettings: %v", err)
	}
	for k := range ss {
		if !allowed[k] {
			t.Errorf("unexpected streamSettings key %q (typo would be silently dropped by xray)", k)
		}
	}
	// Verify expected keys are actually present.
	for _, must := range []string{"network", "security", "tlsSettings", "hysteriaSettings", "finalmask"} {
		if _, ok := ss[must]; !ok {
			t.Errorf("expected streamSettings key %q is missing", must)
		}
	}
}

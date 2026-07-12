package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/proxyengine"
)

func TestProxyInitHysteria2(t *testing.T) {
	dir := t.TempDir()
	xrayConfig := filepath.Join(dir, "config.json")
	t.Setenv("XRAY_CONFIG", xrayConfig) // proxyInitCmd.Run calls config.Load(), which honors XRAY_CONFIG
	out, _, err := execCapture(t, "proxy", "init", "hysteria2://pw@h.example:443?sni=h.example")
	if err != nil {
		t.Fatalf("init: %v (%s)", err, out)
	}
	data, rerr := os.ReadFile(xrayConfig)
	if rerr != nil {
		t.Fatalf("read config: %v", rerr)
	}
	if !strings.Contains(string(data), `"protocol": "hysteria"`) {
		t.Errorf("init did not write a hysteria config:\n%s", data)
	}
}

// TestProxyInitHasNoAddFlag guards the v0.9.0 removal of the deprecated
// `ws proxy init --add` flag: it must no longer be registered on the command.
func TestProxyInitHasNoAddFlag(t *testing.T) {
	if f := proxyInitCmd.Flags().Lookup("add"); f != nil {
		t.Fatalf("`ws proxy init` still registers the removed --add flag: %+v", f)
	}
}

// TestTestDNSVerdict_JSONNotWeakerThanHuman pins that `ws proxy test --json`
// applies the same severity split as the human path (SEC2-02): a proven DNS
// leak or a broken TCP tunnel is a non-zero outcome; an inconclusive UDP leg is
// its own verdict (never reported as "tunneled"/green) but not a failure; a
// tunnelled DNS leg is green. Pure -- no docker, no network.
func TestTestDNSVerdict_JSONNotWeakerThanHuman(t *testing.T) {
	tunneled := proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "198.51.100.9", Tunneled: true}
	broken := proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "203.0.113.7", Tunneled: false}

	cases := []struct {
		name        string
		result      proxyengine.ProbeResult
		dnsExit     string
		wantVerdict string
		wantNonZero bool
	}{
		{"proven DNS leak exits non-zero", tunneled, tunneled.DirectIP, "leak", true},
		{"tunnelled DNS is green", tunneled, tunneled.ProxiedIP, "tunneled", false},
		{"inconclusive DNS is advisory, not green, not a leak", tunneled, "", "inconclusive", false},
		{"broken TCP tunnel skips DNS and exits non-zero", broken, "", "skipped", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict, nonZero := testDNSVerdict(c.result, c.dnsExit)
			if verdict != c.wantVerdict {
				t.Errorf("verdict = %q, want %q", verdict, c.wantVerdict)
			}
			if nonZero != c.wantNonZero {
				t.Errorf("exitNonZero = %v, want %v", nonZero, c.wantNonZero)
			}
		})
	}
}

// TestTestJSONResult_WireIsBackwardCompatibleSuperset pins that the JSON object
// keeps the four existing fields (directIP/proxiedIP/tunneled/latencyMs) so
// existing consumers do not break, and adds the dns verdict field.
func TestTestJSONResult_WireIsBackwardCompatibleSuperset(t *testing.T) {
	b, err := json.Marshal(testJSONResult{
		DirectIP: "203.0.113.7", ProxiedIP: "198.51.100.9", Tunneled: true,
		LatencyMs: 42, DNS: "leak", DNSExitIP: "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"directIP":"203.0.113.7"`, `"proxiedIP":"198.51.100.9"`,
		`"tunneled":true`, `"latencyMs":42`, `"dns":"leak"`, `"dnsExitIP":"203.0.113.7"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("wire %s missing %s", s, want)
		}
	}
}

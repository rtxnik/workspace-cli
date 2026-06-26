package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/vless"
	"github.com/rtxnik/workspace-cli/internal/xray"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// xrayConfigCase is one COMPLETE xray config (log+inbounds+outbounds+routing)
// to feed to `xray -test`. Shared with the docker_e2e gate (Task 2).
type xrayConfigCase struct {
	name string
	json []byte
}

// realityPubKey is a real, well-formed X25519 public key (base64url of 32
// bytes). REALITY public keys are non-secret (shared with clients), so a fixed
// test value is fine — but it MUST be a valid 32-byte key: xray rejects a
// malformed pbk ("invalid password"), which is exactly what placeholder values
// like "pub123" trip.
const realityPubKey = "Mfgq_bxUlJoJKJUf9iX4kMAuxww70_mYytF2AWnElzQ"

// vlessMatrix is the VLESS URI matrix H6 validates against the pinned xray-core,
// one URI per parser-supported transport that xray v26.2.6 accepts.
//
// `type=h2` is included: xray v26 REMOVED the HTTP/2 transport ("migrated to
// XHTTP stream-one H2 & H3"), so ws migrates a parsed h2 transport to an xhttp
// stream-one config at generation (internal/vless/config.go). The vless-h2-tls
// entry proves that migrated config loads on the pinned xray.
var vlessMatrix = []struct{ name, uri string }{
	{"vless-tcp-reality", "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&flow=xtls-rprx-vision&type=tcp&security=reality&sni=www.google.com&fp=chrome&pbk=" + realityPubKey + "&sid=ab&spx=%2F#my-node"},
	{"vless-tcp-http-header", "vless://22222222-2222-2222-2222-222222222222@example.com:80?type=tcp&security=none&headerType=http&host=cdn.example.com&path=%2F#http-node"},
	{"vless-ws-tls", "vless://33333333-3333-3333-3333-333333333333@ws.example.com:443?type=ws&security=tls&sni=ws.example.com&fp=firefox&host=ws.example.com&path=%2Fvless-ws#ws-tls"},
	{"vless-grpc-reality", "vless://44444444-4444-4444-4444-444444444444@grpc.example.com:443?type=grpc&security=reality&sni=www.google.com&fp=chrome&pbk=" + realityPubKey + "&sid=cd&serviceName=mygrpc#grpc-node"},
	{"vless-httpupgrade-tls", "vless://66666666-6666-6666-6666-666666666666@hu.example.com:443?type=httpupgrade&security=tls&sni=hu.example.com&fp=safari&host=hu.example.com&path=%2Fupgrade#hu-node"},
	{"vless-xhttp-reality", "vless://77777777-7777-7777-7777-777777777777@xhttp.example.com:443?type=xhttp&security=reality&sni=www.google.com&fp=chrome&pbk=" + realityPubKey + "&sid=ef&path=%2Fxpath&mode=auto#xhttp-node"},
	{"vless-h2-tls", "vless://88888888-8888-8888-8888-888888888888@h2.example.com:443?type=h2&security=tls&sni=h2.example.com&fp=chrome&host=h2.example.com&path=%2Fh2path#h2-node"},
}

// repoRoot derives the repository root (dir holding go.mod) from this test
// file's location: cmd/<file> -> root. (Named repoRoot, not moduleRoot, to
// avoid shadowing the local moduleRoot var in proxy_e2e_test.go.)
func repoRoot() string {
	_, testFile, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(testFile))
}

// committedGoldens lists the committed golden config files that H6 feeds to
// `xray -test`. Every committed golden is included — each emits VALIDLY under
// the pinned xray-core and is also covered by a byte-stability test in
// internal/hysteria2/golden_test.go or internal/xrayconf/config_test.go:
//
//   - pin.golden.json emits the cert pin as hex (xray v26 hex-decodes
//     tlsSettings.pinnedPeerCertSha256).
//   - assemble_vless.golden.json uses a valid UUID + x25519 REALITY pubkey.
func committedGoldens() []string {
	root := repoRoot()
	return []string{
		filepath.Join(root, "internal", "hysteria2", "testdata", "base.golden.json"),
		filepath.Join(root, "internal", "hysteria2", "testdata", "obfs.golden.json"),
		filepath.Join(root, "internal", "hysteria2", "testdata", "pin.golden.json"),
		filepath.Join(root, "internal", "hysteria2", "testdata", "udphop.golden.json"),
		filepath.Join(root, "internal", "xrayconf", "testdata", "assemble_vless.golden.json"),
	}
}

// collectXrayConfigs assembles every config the H6 gate validates: the
// committed goldens (read from disk) plus the generated VLESS matrix.
func collectXrayConfigs(t *testing.T) []xrayConfigCase {
	t.Helper()
	var cases []xrayConfigCase

	for _, p := range committedGoldens() {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read golden %s: %v", p, err)
		}
		cases = append(cases, xrayConfigCase{name: "golden:" + filepath.Base(p), json: data})
	}

	for _, m := range vlessMatrix {
		cfg, err := vless.Parse(m.uri)
		if err != nil {
			t.Fatalf("vless.Parse(%s): %v", m.name, err)
		}
		xc, err := vless.GenerateConfig(cfg, "proxy-1")
		if err != nil {
			t.Fatalf("vless.GenerateConfig(%s): %v", m.name, err)
		}
		data, err := json.MarshalIndent(xc, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", m.name, err)
		}
		cases = append(cases, xrayConfigCase{name: "matrix:" + m.name, json: data})
	}

	cases = append(cases, repairedLegacyConfigs(t)...)
	return cases
}

// repairedLegacyConfigs builds configs in the pre-fix on-disk shapes (a base64
// cert pin; a network:"h2" transport) and runs them through xray.RepairConfig.
// Feeding the REPAIRED output to `xray -test` proves the self-heal produces a
// config a current xray accepts -- including stored keys the generator never
// emits.
func repairedLegacyConfigs(t *testing.T) []xrayConfigCase {
	t.Helper()
	legacyHy2 := xrayconf.AssembleConfig(xrayconf.Outbound{
		Tag: "proxy-1", Protocol: "hysteria",
		Settings: []byte(`{"version":2,"address":"h.example","port":443}`),
		StreamSettings: []byte(`{"network":"hysteria","security":"tls","hysteriaSettings":{"version":2,"auth":"pw"},` +
			`"tlsSettings":{"serverName":"h.example","alpn":["h3"],"fingerprint":"chrome","pinnedPeerCertSha256":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}`),
	})
	legacyH2 := xrayconf.AssembleConfig(xrayconf.Outbound{
		Tag: "proxy-1", Protocol: "vless",
		Settings: []byte(`{"vnext":[{"address":"h2.example","port":443,"users":[{"id":"88888888-8888-8888-8888-888888888888","encryption":"none","flow":""}]}]}`),
		StreamSettings: []byte(`{"network":"h2","security":"tls","tlsSettings":{"serverName":"h2.example","fingerprint":"chrome"},` +
			`"httpSettings":{"host":["h2.example"],"path":"/h2path"}}`),
	})

	var cases []xrayConfigCase
	for name, xc := range map[string]*xrayconf.XrayConfig{"repaired-hy2-pin": legacyHy2, "repaired-vless-h2": legacyH2} {
		if _, err := xray.RepairConfig(xc); err != nil {
			t.Fatalf("RepairConfig(%s): %v", name, err)
		}
		data, err := json.MarshalIndent(xc, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		cases = append(cases, xrayConfigCase{name: "repaired:" + name, json: data})
	}
	return cases
}

func TestGoldenAndMatrixConfigsGenerate(t *testing.T) {
	cases := collectXrayConfigs(t)

	// 5 committed goldens + 7 matrix transports + 2 repaired-legacy = 14.
	if got, want := len(cases), 14; got != want {
		t.Fatalf("collected %d configs, want %d", got, want)
	}

	for _, c := range cases {
		var xc xrayconf.XrayConfig
		if err := json.Unmarshal(c.json, &xc); err != nil {
			t.Errorf("%s: not valid xray JSON: %v", c.name, err)
			continue
		}
		if len(xc.Inbounds) == 0 {
			t.Errorf("%s: no inbounds — not a complete config", c.name)
		}
		if len(xc.Outbounds) == 0 {
			t.Errorf("%s: no outbounds — not a complete config", c.name)
		}
	}
}

func TestRepairedLegacyConfigsAreHealed(t *testing.T) {
	for _, c := range repairedLegacyConfigs(t) {
		if strings.Contains(string(c.json), `"network": "h2"`) {
			t.Errorf("%s still has network h2:\n%s", c.name, c.json)
		}
		if strings.Contains(string(c.json), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") {
			t.Errorf("%s still has base64 pin:\n%s", c.name, c.json)
		}
	}
}

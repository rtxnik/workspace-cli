package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/vless"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

// xrayConfigCase is one COMPLETE xray config (log+inbounds+outbounds+routing)
// to feed to `xray -test`. Shared with the docker_e2e gate (Task 2).
type xrayConfigCase struct {
	name string
	json []byte
}

// vlessMatrix is the representative VLESS URI matrix — one URI per transport
// the parser supports. Mirrors internal/vless/parser_test.go.
var vlessMatrix = []struct{ name, uri string }{
	{"vless-tcp-reality", "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&flow=xtls-rprx-vision&type=tcp&security=reality&sni=www.google.com&fp=chrome&pbk=pub123&sid=ab&spx=%2F#my-node"},
	{"vless-tcp-http-header", "vless://22222222-2222-2222-2222-222222222222@example.com:80?type=tcp&security=none&headerType=http&host=cdn.example.com&path=%2F#http-node"},
	{"vless-ws-tls", "vless://33333333-3333-3333-3333-333333333333@ws.example.com:443?type=ws&security=tls&sni=ws.example.com&fp=firefox&host=ws.example.com&path=%2Fvless-ws#ws-tls"},
	{"vless-grpc-reality", "vless://44444444-4444-4444-4444-444444444444@grpc.example.com:443?type=grpc&security=reality&sni=www.google.com&fp=chrome&pbk=grpc-pub&sid=cd&serviceName=mygrpc#grpc-node"},
	{"vless-h2-tls", "vless://55555555-5555-5555-5555-555555555555@h2.example.com:443?type=h2&security=tls&sni=h2.example.com&fp=chrome&host=h2.example.com&path=%2Fh2path#h2-node"},
	{"vless-httpupgrade-tls", "vless://66666666-6666-6666-6666-666666666666@hu.example.com:443?type=httpupgrade&security=tls&sni=hu.example.com&fp=safari&host=hu.example.com&path=%2Fupgrade#hu-node"},
	{"vless-xhttp-reality", "vless://77777777-7777-7777-7777-777777777777@xhttp.example.com:443?type=xhttp&security=reality&sni=www.google.com&fp=chrome&pbk=xhttp-pub&sid=ef&path=%2Fxpath&mode=auto#xhttp-node"},
}

// moduleRoot derives the repository root (dir holding go.mod) from this test
// file's location: cmd/<file> -> root.
func moduleRoot() string {
	_, testFile, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(testFile))
}

// committedGoldens lists the committed golden config files (complete configs).
func committedGoldens() []string {
	root := moduleRoot()
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
	return cases
}

func TestGoldenAndMatrixConfigsGenerate(t *testing.T) {
	cases := collectXrayConfigs(t)

	// 5 committed goldens + 7 matrix transports = 12.
	if got, want := len(cases), 12; got != want {
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

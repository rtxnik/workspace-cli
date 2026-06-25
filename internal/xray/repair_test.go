package xray

import (
	"encoding/json"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/vless"
	"github.com/rtxnik/workspace-cli/internal/xrayconf"
)

func ssOf(t *testing.T, ob xrayconf.Outbound) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ob.StreamSettings, &m); err != nil {
		t.Fatalf("unmarshal streamSettings: %v", err)
	}
	return m
}

func hy2Outbound(pin string) xrayconf.Outbound {
	return xrayconf.Outbound{
		Tag:      "proxy-1",
		Protocol: "hysteria",
		StreamSettings: json.RawMessage(`{"network":"hysteria","security":"tls","sockopt":{"keepme":true},` +
			`"tlsSettings":{"serverName":"h.example","fingerprint":"chrome","pinnedPeerCertSha256":"` + pin + `"}}`),
	}
}

const wantHexPin = "0000000000000000000000000000000000000000000000000000000000000000"

func TestRepairOutbound_PinBase64ToHex(t *testing.T) {
	got, changes, err := RepairOutbound(hy2Outbound("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want one", changes)
	}
	tls := ssOf(t, got)["tlsSettings"].(map[string]any)
	if tls["pinnedPeerCertSha256"] != wantHexPin {
		t.Errorf("pin = %v, want %s", tls["pinnedPeerCertSha256"], wantHexPin)
	}
}

func TestRepairOutbound_PinUpperAndColonToLower(t *testing.T) {
	upper := "00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00"
	got, changes, err := RepairOutbound(hy2Outbound(upper))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want one", changes)
	}
	tls := ssOf(t, got)["tlsSettings"].(map[string]any)
	if tls["pinnedPeerCertSha256"] != wantHexPin {
		t.Errorf("pin = %v, want %s", tls["pinnedPeerCertSha256"], wantHexPin)
	}
}

func TestRepairOutbound_PinAlreadyHex_NoChange(t *testing.T) {
	_, changes, err := RepairOutbound(hy2Outbound(wantHexPin))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v, want none (idempotent)", changes)
	}
}

func TestRepairOutbound_PinCorrupt_WarnUnchanged(t *testing.T) {
	got, changes, err := RepairOutbound(hy2Outbound("not-a-valid-pin"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v, want none for corrupt pin", changes)
	}
	tls := ssOf(t, got)["tlsSettings"].(map[string]any)
	if tls["pinnedPeerCertSha256"] != "not-a-valid-pin" {
		t.Errorf("corrupt pin must be left unchanged, got %v", tls["pinnedPeerCertSha256"])
	}
}

func TestRepairOutbound_PreservesUnknownKeys(t *testing.T) {
	got, _, err := RepairOutbound(hy2Outbound("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	ss := ssOf(t, got)
	if _, ok := ss["sockopt"]; !ok {
		t.Errorf("unknown streamSettings key 'sockopt' must be preserved, got keys %v", ss)
	}
}

func TestRepairOutbound_NoStreamSettings_NoChange(t *testing.T) {
	_, changes, err := RepairOutbound(xrayconf.Outbound{Tag: "direct", Protocol: "freedom"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v, want none", changes)
	}
}

func h2Outbound() xrayconf.Outbound {
	return xrayconf.Outbound{
		Tag:      "proxy-1",
		Protocol: "vless",
		StreamSettings: json.RawMessage(`{"network":"h2","security":"tls",` +
			`"tlsSettings":{"serverName":"h2.example","fingerprint":"chrome"},` +
			`"httpSettings":{"host":["h2.example"],"path":"/h2path"}}`),
	}
}

func TestRepairOutbound_H2ToXHTTP(t *testing.T) {
	got, changes, err := RepairOutbound(h2Outbound())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want one", changes)
	}
	ss := ssOf(t, got)
	if ss["network"] != "xhttp" {
		t.Errorf("network = %v, want xhttp", ss["network"])
	}
	if _, ok := ss["httpSettings"]; ok {
		t.Errorf("httpSettings must be removed")
	}
	xh := ss["xhttpSettings"].(map[string]any)
	if xh["mode"] != "stream-one" || xh["path"] != "/h2path" || xh["host"] != "h2.example" {
		t.Errorf("xhttpSettings = %v", xh)
	}
}

func TestRepairOutbound_H2_AlreadyXHTTP_NoChange(t *testing.T) {
	ob := xrayconf.Outbound{
		Protocol:       "vless",
		StreamSettings: json.RawMessage(`{"network":"xhttp","security":"tls","xhttpSettings":{"mode":"auto","path":"/p"}}`),
	}
	_, changes, err := RepairOutbound(ob)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v, want none", changes)
	}
}

// TestRepairOutbound_H2_MatchesGenerator is the anti-drift guard: the repaired
// h2 streamSettings must equal what the generator emits for the equivalent
// VLESSConfig (Network:"h2", which the generator migrates identically). It
// compares the transport keys the repair touches; the security block is
// untouched by repair and identical by construction.
func TestRepairOutbound_H2_MatchesGenerator(t *testing.T) {
	cfg := vless.VLESSConfig{
		UUID: "88888888-8888-8888-8888-888888888888", Address: "h2.example", Port: 443,
		Encryption: "none", Network: "h2", Security: "tls",
		SNI: "h2.example", Fp: "chrome", Host: "h2.example", Path: "/h2path",
	}
	gen, err := vless.GenerateConfig(cfg, "proxy-1")
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	var genSS map[string]any
	if err := json.Unmarshal(gen.Outbounds[0].StreamSettings, &genSS); err != nil {
		t.Fatalf("unmarshal generated: %v", err)
	}

	got, _, err := RepairOutbound(h2Outbound())
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	gotSS := ssOf(t, got)

	for _, k := range []string{"network", "xhttpSettings"} {
		gb, _ := json.Marshal(gotSS[k])
		wb, _ := json.Marshal(genSS[k])
		if string(gb) != string(wb) {
			t.Errorf("key %q drift:\n repaired:  %s\n generator: %s", k, gb, wb)
		}
	}
}

func TestRepairConfig_RepairsProxyOutboundsOnly(t *testing.T) {
	xc := &xrayconf.XrayConfig{
		Outbounds: []xrayconf.Outbound{
			h2Outbound(),
			{Tag: "direct", Protocol: "freedom"},
		},
	}
	changes, err := RepairConfig(xc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want one (h2 only)", changes)
	}
	var ss map[string]any
	if err := json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ss["network"] != "xhttp" {
		t.Errorf("proxy outbound not repaired in place: network=%v", ss["network"])
	}
}

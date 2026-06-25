package xray

import (
	"encoding/json"
	"testing"

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

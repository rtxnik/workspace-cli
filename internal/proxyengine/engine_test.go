package proxyengine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
)

// TestXrayEngineBuildConfig is the TDD seed for Task 6: Default().BuildConfig
// must scheme-dispatch a share URI and emit valid xray JSON for each protocol.
func TestXrayEngineBuildConfig(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "hysteria2",
			uri:  "hy2://pw@h.example:443",
			want: `"protocol": "hysteria"`,
		},
		{
			name: "vless",
			uri:  "vless://11111111-1111-1111-1111-111111111111@v.example:443?type=tcp&security=none#p",
			want: `"protocol": "vless"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := proxyengine.Default().BuildConfig(proxyengine.Profile{URI: tt.uri})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("bad config: missing %q in:\n%s", tt.want, out)
			}
		})
	}
}

// TestXrayEngineBuildConfigUnsupported asserts an unknown scheme is rejected.
func TestXrayEngineBuildConfigUnsupported(t *testing.T) {
	if _, err := proxyengine.Default().BuildConfig(proxyengine.Profile{URI: "ss://nope"}); err == nil {
		t.Fatal("expected error for unsupported scheme, got nil")
	}
}

// fakeEngine is a swappable double proving the interface is small enough to
// implement in a test (the seam's whole point — a future backend plugs in here).
type fakeEngine struct {
	cfg       []byte
	buildErr  error
	validated string
	valErr    error
}

func (f *fakeEngine) BuildConfig(p proxyengine.Profile) ([]byte, error) {
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	return f.cfg, nil
}

func (f *fakeEngine) Validate(_ config.Config, profileName string) error {
	f.validated = profileName
	return f.valErr
}

func (f *fakeEngine) Probe(_ config.Config) (proxyengine.ProbeResult, error) {
	return proxyengine.ProbeResult{}, nil
}

// compile-time assertion: fakeEngine satisfies Engine.
var _ proxyengine.Engine = (*fakeEngine)(nil)

func TestFakeEngineSatisfiesInterface(t *testing.T) {
	var eng proxyengine.Engine = &fakeEngine{cfg: []byte("{}"), valErr: errors.New("boom")}

	out, err := eng.BuildConfig(proxyengine.Profile{URI: "x"})
	if err != nil || string(out) != "{}" {
		t.Fatalf("BuildConfig = %q, %v", out, err)
	}
	if err := eng.Validate(config.Config{}, "p1"); err == nil {
		t.Fatal("expected fake Validate error")
	}
}

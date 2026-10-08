package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
)

func TestProxyInitHysteria2(t *testing.T) {
	dir := t.TempDir()
	xrayConfig := filepath.Join(dir, "config.json")
	t.Setenv("XRAY_CONFIG", xrayConfig) // proxyInitCmd.RunE calls config.Load(), which honors XRAY_CONFIG
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

// TestTunnelReport pins ws proxy test's report (phase-5 §3.6) in each state
// of the run: the UDP/DNS leg's mark and word, and the verdict the caption
// carries — a leak's over the tunnel's.
func TestTunnelReport(t *testing.T) {
	up := proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "198.51.100.9", Tunneled: true, Latency: 182600 * time.Microsecond}
	down := proxyengine.ProbeResult{DirectIP: "203.0.113.7", ProxiedIP: "203.0.113.7", Latency: 95 * time.Millisecond}
	report := func(r proxyengine.ProbeResult, tunneled, dns output.Fact, latency, caption string) output.KV {
		return output.KV{Title: "Tunnel", Pairs: []output.Fact{
			{K: "Direct IP", V: r.DirectIP}, {K: "Proxied IP", V: r.ProxiedIP}, tunneled, {K: "Latency", V: latency}, dns,
		}, Caption: caption}
	}
	yes, no := output.StateFact("Tunneled", output.StateOK, "yes"), output.StateFact("Tunneled", output.StateFail, "no")
	for _, c := range []struct {
		name    string
		result  proxyengine.ProbeResult
		dnsExit string
		want    output.KV
	}{
		{"tunnelled", up, "198.51.100.9", report(up, yes, output.StateFact("UDP/DNS", output.StateOK, "tunnelled (exit 198.51.100.9)"),
			"182ms", "Tunnel active — exit IPs differ")},
		{"leak", up, "203.0.113.7", report(up, yes, output.StateFact("UDP/DNS", output.StateFail, "leak (exit 203.0.113.7 is the direct IP)"),
			"182ms", "UDP/DNS LEAK -- resolver saw your real IP 203.0.113.7 (untunnelled)")},
		{"inconclusive", up, "", report(up, yes, output.StateFact("UDP/DNS", output.StateUnknown, "inconclusive"),
			"182ms", "Tunnel active — exit IPs differ")},
		{"down", down, "", report(down, no, output.StateFact("UDP/DNS", output.StateIdle, "not probed"),
			"95ms", "Tunnel NOT active — direct and proxied exit IPs are the same")},
	} {
		if got := tunnelReport(c.result, c.dnsExit); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
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

// TestProtectionSummary covers the pure human status line for workspace route
// protection: UNPROTECTED takes priority (with fix hint), then UNKNOWN, then
// all-protected; empty list yields no line.
func TestProtectionSummary(t *testing.T) {
	cases := []struct {
		name            string
		prot            []docker.RouteProtection
		wantContains    string
		wantUnprotected bool
	}{
		{"none connected", nil, "", false},
		{
			"one unprotected dominates",
			[]docker.RouteProtection{
				{Name: "a", Verdict: docker.RouteProtected},
				{Name: "b", Verdict: docker.RouteUnprotected},
			},
			"UNPROTECTED", true,
		},
		{
			"unknown when no unprotected",
			[]docker.RouteProtection{{Name: "a", Verdict: docker.RouteUnknown}},
			"UNKNOWN", false,
		},
		{
			"all protected",
			[]docker.RouteProtection{{Name: "a", Verdict: docker.RouteProtected}},
			"protected", false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, anyUnprot := protectionSummary(c.prot)
			if c.wantContains == "" {
				if line != "" {
					t.Errorf("want empty line, got %q", line)
				}
			} else if !strings.Contains(line, c.wantContains) {
				t.Errorf("line %q must contain %q", line, c.wantContains)
			}
			if anyUnprot != c.wantUnprotected {
				t.Errorf("anyUnprotected = %v, want %v", anyUnprot, c.wantUnprotected)
			}
		})
	}
}

// TestProtectionStatusString maps each verdict to its stable JSON token.
func TestProtectionStatusString(t *testing.T) {
	cases := map[docker.RouteProtectionVerdict]string{
		docker.RouteProtected:   "protected",
		docker.RouteUnprotected: "unprotected",
		docker.RouteUnknown:     "unknown",
	}
	for v, want := range cases {
		if got := protectionStatusString(v); got != want {
			t.Errorf("protectionStatusString(%v) = %q, want %q", v, got, want)
		}
	}
}

// TestChecksCaption pins the caption every Checks report closes with (phase-5
// §3.4): "N of M checks passed", N counting the ok lines only, then the
// failed, degraded and unknown counts in that order, each only when it is not
// zero. An unknown is never counted as passed.
func TestChecksCaption(t *testing.T) {
	item := func(st output.State) output.Check { return output.Check{Name: "x", State: st} }
	for _, c := range []struct {
		states []output.State
		want   string
	}{
		{[]output.State{output.StateOK, output.StateOK}, "2 of 2 checks passed"},
		{[]output.State{output.StateFail, output.StateOK, output.StateUnknown, output.StateUnknown}, "1 of 4 checks passed, 1 failed, 2 unknown"},
		{[]output.State{output.StateOK, output.StateAdvisory, output.StateUnknown, output.StateOK}, "2 of 4 checks passed, 1 degraded, 1 unknown"},
		{[]output.State{output.StateAdvisory, output.StateFail, output.StateUnknown, output.StateOK}, "1 of 4 checks passed, 1 failed, 1 degraded, 1 unknown"},
		{[]output.State{output.StateUnknown}, "0 of 1 checks passed, 1 unknown"},
	} {
		items := make([]output.Check, 0, len(c.states))
		for _, st := range c.states {
			items = append(items, item(st))
		}
		if got := checksCaption(items); got != c.want {
			t.Errorf("checksCaption(%v) = %q, want %q", c.states, got, c.want)
		}
	}
}

// TestProxyCheckReport pins ws proxy check's report (phase-5 §3.4): one line
// per prerequisite, in ProxyCheck's order — ok, failed, or unknown for a check
// the daemon was not there to answer — and the caption that counts them.
func TestProxyCheckReport(t *testing.T) {
	names := []string{"Docker running", "Xray config exists", "Proxy image built", "Proxy container running"}
	results := func(passed, skipped [4]bool) []docker.CheckResult {
		r := make([]docker.CheckResult, 4)
		for i := range r {
			r[i] = docker.CheckResult{Name: names[i], Passed: passed[i], Skipped: skipped[i]}
		}
		return r
	}
	ok, fail, unknown := output.StateOK, output.StateFail, output.StateUnknown
	for _, c := range []struct {
		name    string
		results []docker.CheckResult
		states  []output.State
		caption string
	}{
		{"no daemon", results([4]bool{false, true, false, false}, [4]bool{false, false, true, true}),
			[]output.State{fail, ok, unknown, unknown}, "1 of 4 checks passed, 1 failed, 2 unknown"},
		{"all pass", results([4]bool{true, true, true, true}, [4]bool{}),
			[]output.State{ok, ok, ok, ok}, "4 of 4 checks passed"},
		{"no image, stopped", results([4]bool{true, true, false, false}, [4]bool{}),
			[]output.State{ok, ok, fail, fail}, "2 of 4 checks passed, 2 failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := proxyCheckReport(c.results)
			if got.Title != "Proxy prerequisites" {
				t.Errorf("title %q, want %q", got.Title, "Proxy prerequisites")
			}
			if len(got.Items) != len(names) {
				t.Fatalf("%d lines, want %d: %+v", len(got.Items), len(names), got.Items)
			}
			for i, it := range got.Items {
				if it.Name != names[i] || it.State != c.states[i] || it.Note != "" {
					t.Errorf("line %d = %+v, want %q in state %v with no note", i+1, it, names[i], c.states[i])
				}
			}
			if got.Caption != c.caption {
				t.Errorf("caption %q, want %q", got.Caption, c.caption)
			}
		})
	}
}

// TestProxyCheckExitCode pins ws proxy check's exit code (phase-5 §3.4, owner
// ruling 2): rc 1 unless all four prerequisites are ok, with no message, since
// the report already says why; rc 0 when they are. Before phase 5 it exited 0
// with every check failing, so `ws proxy check && ws proxy up` went on into a
// certain failure.
func TestProxyCheckExitCode(t *testing.T) {
	xray := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(xray, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_CONFIG", xray)

	t.Setenv("DOCKER_HOST", startFakeDocker(t, fakeDockerState{Down: true}))
	out, _, err := execCapture(t, "proxy", "check")
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) || cerr.code != 1 || cerr.msg != "" {
		t.Errorf("with no daemon: err = %v, want a silent exit 1", err)
	}
	if !strings.Contains(out.String(), "1 of 4 checks passed, 1 failed, 2 unknown") {
		t.Errorf("with no daemon the report is not on stdout:\n%s", out)
	}

	t.Setenv("DOCKER_HOST", startFakeDocker(t, fakeDockerState{
		Container: &fakeContainer{Running: true, Health: "healthy", StartedAt: "2026-10-07T10:00:00Z", Image: "devpod-proxy"},
		Labels:    map[string]string{},
	}))
	if out, _, err := execCapture(t, "proxy", "check"); err != nil || !strings.Contains(out.String(), "4 of 4 checks passed") {
		t.Errorf("with everything ok: err = %v, want none; stdout:\n%s", err, out)
	}
}

// TestProxyStatusReport pins ws proxy status's report (phase-5 §3.3): a Proxy
// block whose state and health carry their mark and word, and, when a
// workspace is connected or the scan failed, a Workspaces block with one
// route verdict per workspace and the protection summary as its caption.
func TestProxyStatusReport(t *testing.T) {
	cfg := config.Config{ProxyNetwork: "ws-proxy", ProxyIP: "172.28.0.2"}
	type pair struct {
		k, v  string
		state output.State
		mark  bool // the value carries a state
	}
	check := func(t *testing.T, name string, kv output.KV, title string, want []pair, caption string) {
		t.Helper()
		if kv.Title != title || kv.Caption != caption {
			t.Errorf("%s: title %q caption %q; want %q, %q", name, kv.Title, kv.Caption, title, caption)
		}
		if len(kv.Pairs) != len(want) {
			t.Fatalf("%s: %d pairs, want %d: %+v", name, len(kv.Pairs), len(want), kv.Pairs)
		}
		for i, w := range want {
			got := kv.Pairs[i]
			var wantFact output.Fact
			if w.mark {
				wantFact = output.StateFact(w.k, w.state, w.v)
			} else {
				wantFact = output.Fact{K: w.k, V: w.v}
			}
			if got != wantFact {
				t.Errorf("%s: pair %d = %+v, want %+v", name, i+1, got, wantFact)
			}
		}
	}

	up := docker.Status{Running: true, Health: "healthy", Uptime: "10h18m28s", Image: "devpod-proxy"}
	prot := []docker.RouteProtection{
		{Name: "api", Verdict: docker.RouteProtected, Detail: "default via 172.28.0.2"},
		{Name: "web-frontend", Verdict: docker.RouteUnprotected, Detail: "default via 172.28.0.1 (not the proxy 172.28.0.2)"},
		{Name: "ml", Verdict: docker.RouteUnknown, Detail: "route unreadable: exit status 1"},
	}
	blocks := proxyStatusReport(up, cfg, prot, nil)
	if len(blocks) != 2 {
		t.Fatalf("running with workspaces: %d blocks, want 2", len(blocks))
	}
	check(t, "running", blocks[0], "Proxy", []pair{
		{"State", "running", output.StateOK, true},
		{"Health", "healthy", output.StateOK, true},
		{"Uptime", "10h18m28s", 0, false},
		{"Image", "devpod-proxy", 0, false},
		{"Network", "ws-proxy (172.28.0.2)", 0, false},
	}, "")
	check(t, "running", blocks[1], "Workspaces", []pair{
		{"api", "protected", output.StateOK, true},
		{"web-frontend", "unprotected: default via 172.28.0.1 (not the proxy 172.28.0.2)", output.StateFail, true},
		{"ml", "unknown: route unreadable: exit status 1", output.StateUnknown, true},
	}, "1 of 3 workspace(s) UNPROTECTED — route not via proxy (run: ws proxy fix-routes)")

	for health, want := range map[string]pair{
		"unhealthy":  {"Health", "unhealthy", output.StateFail, true},
		"starting":   {"Health", "starting", output.StateBusy, true},
		"restarting": {"Health", "restarting", output.StateUnknown, true},
	} {
		st := up
		st.Health = health
		check(t, health, proxyStatusReport(st, cfg, nil, nil)[0], "Proxy", []pair{
			{"State", "running", output.StateOK, true}, want,
			{"Uptime", "10h18m28s", 0, false}, {"Image", "devpod-proxy", 0, false},
			{"Network", "ws-proxy (172.28.0.2)", 0, false},
		}, "")
	}

	blocks = proxyStatusReport(docker.Status{}, cfg, nil, errors.New("inspect network: not found"))
	if len(blocks) != 2 {
		t.Fatalf("stopped, scan failed: %d blocks, want 2", len(blocks))
	}
	check(t, "stopped", blocks[0], "Proxy", []pair{
		{"State", "stopped", output.StateIdle, true},
		{"Network", "ws-proxy (172.28.0.2)", 0, false},
	}, "")
	check(t, "stopped", blocks[1], "Workspaces", []pair{{"Route protection", "", output.StateUnknown, true}},
		"protection scan failed: inspect network: not found (workspace protection UNKNOWN)")

	if blocks := proxyStatusReport(up, cfg, nil, nil); len(blocks) != 1 {
		t.Errorf("no workspace connected and no scan error: %d blocks, want the Proxy block alone", len(blocks))
	}
}

// TestReportBlocksAreSeparated pins writeReport's composition: one blank line
// between two blocks, none after the last, one write.
func TestReportBlocksAreSeparated(t *testing.T) {
	var b strings.Builder
	s := output.NewStreamAt(&b, 80, false, output.ColourNone, false)
	err := writeReport(&b, s, output.KV{Title: "A", Pairs: []output.Fact{{K: "k", V: "v"}}}, output.KV{Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "A\n  k  v\n\nB\n"; b.String() != want {
		t.Errorf("got %q; want one blank line between the blocks, %q", b.String(), want)
	}
}

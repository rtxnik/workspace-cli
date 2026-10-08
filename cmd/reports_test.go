package cmd

import (
	"errors"
	"flag"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/proxyengine"
)

// The reports of phase 5 — ws proxy status, check, doctor and test, ws vault
// doctor, status and predict-bulk-load, and the profile wizard's summary —
// built by the commands' own builders and composed as writeReport composes
// them, held to the width contract at every width (§4.1) and whole in a pipe
// (§4.2). The fixtures reuse the tables' 64-character name and 200-character
// token.

// sweepIPv6 is an IPv6 address, sweepImage an image with a registry, a path
// and a tag, sweepCJK a CJK detail, and sweepMultiline an error of two lines.
var (
	sweepIPv6      = "2001:db8:85a3::8a2e:370:7334"
	sweepImage     = "registry.example.com/platform/devpod-proxy:v1.26.4-tproxy"
	sweepCJK       = "错误：无法连接到代理服务器，正在重试"
	sweepMultiline = "inspect ws-proxy: exit status 1\nError response from daemon: " + sweepToken
)

// sweepProtection is ws proxy status's route scan: the 64-character name
// protected, a CJK name unprotected, and a workspace whose route could not be
// read, the token in its detail.
var sweepProtection = []docker.RouteProtection{
	{Name: sweepName, Verdict: docker.RouteProtected},
	{Name: "数据科学", Verdict: docker.RouteUnprotected, Detail: "default via 172.28.0.1 (not the proxy " + sweepIPv6 + ")"},
	{Name: "api", Verdict: docker.RouteUnknown, Detail: "exec ip route: " + sweepToken},
}

// sweptReport is one report, with what it must render whole in a pipe: every
// value and note its fixture was built from — a note of several paragraphs
// paragraph by paragraph — every state it draws, mark and word, in the UTF-8
// glyph mode, and the caption it closes with.
type sweptReport struct {
	name    string
	blocks  []reportBlock
	values  []string
	states  []string
	caption string
}

// render is the report as its command writes it, less the final newline.
func (r sweptReport) render(s *output.Stream) string {
	var b strings.Builder
	_ = writeReport(&b, s, r.blocks...) // a strings.Builder does not fail
	return strings.TrimSuffix(b.String(), "\n")
}

// kvBlocks are ws proxy status's blocks as a report's.
func kvBlocks(kvs []output.KV) []reportBlock {
	blocks := make([]reportBlock, 0, len(kvs))
	for _, k := range kvs {
		blocks = append(blocks, k)
	}
	return blocks
}

// sweepReports builds every report through its command's builder.
func sweepReports(t *testing.T) []sweptReport {
	t.Helper()
	var reports []sweptReport
	add := func(name string, values, states []string, caption string, blocks ...reportBlock) {
		reports = append(reports, sweptReport{name, blocks, values, states, caption})
	}

	cfg := config.Config{ProxyNetwork: "ws-proxy", ProxyIP: sweepIPv6}
	add("ws proxy status: running, three workspaces",
		[]string{"10h18m28s", sweepImage, "ws-proxy (" + sweepIPv6 + ")", sweepName, "数据科学", "api"},
		[]string{"✓ running", "✓ healthy", "✓ protected", "✗ unprotected: " + sweepProtection[1].Detail,
			"? unknown: " + sweepProtection[2].Detail},
		"1 of 3 workspace(s) UNPROTECTED — route not via proxy (run: ws proxy fix-routes)",
		kvBlocks(proxyStatusReport(docker.Status{Running: true, Health: "healthy", Uptime: "10h18m28s", Image: sweepImage},
			cfg, sweepProtection, nil))...)
	add("ws proxy status: the route scan failed",
		[]string{"Route protection"},
		[]string{"✓ running", "~ starting", "? unknown"},
		"protection scan failed: "+sweepMultiline+" (workspace protection UNKNOWN)",
		kvBlocks(proxyStatusReport(docker.Status{Running: true, Health: "starting"}, cfg, nil, errors.New(sweepMultiline)))...)
	add("ws proxy status: stopped",
		[]string{"ws-proxy (" + sweepIPv6 + ")"},
		[]string{"- stopped", "✗ unhealthy"},
		"",
		kvBlocks(proxyStatusReport(docker.Status{Health: "unhealthy"}, cfg, nil, nil))...)

	add("ws proxy check: no daemon",
		[]string{"Docker running", "Xray config exists", "Proxy image built", "Proxy container running"},
		[]string{"✗ failed", "✓ ok", "? unknown"},
		"1 of 4 checks passed, 1 failed, 2 unknown",
		proxyCheckReport([]docker.CheckResult{{Name: "Docker running"}, {Name: "Xray config exists", Passed: true},
			{Name: "Proxy image built", Skipped: true}, {Name: "Proxy container running", Skipped: true}}))

	checks := proxyDoctorChecks(cfg, nil)
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	fix := "Realign image and profile: ws proxy rebuild  (and, if the profile is stale, ws proxy upgrade-config)"
	stopped := `image datapath="tproxy" but active profile mode="redirect" (black-hole risk): ` + sweepCJK
	failed := Result{FailedAt: 3, Outcomes: []checkResult{
		{Name: names[0], CheckOutcome: CheckOutcome{OK: true}},
		{Name: names[1], CheckOutcome: CheckOutcome{OK: true, Detail: sweepImage}},
		{Name: names[2], CheckOutcome: CheckOutcome{OK: true, Detail: `profile "` + sweepName + `"`}},
		{Name: names[3], CheckOutcome: CheckOutcome{Detail: stopped, Fix: fix}},
	}}
	add("ws proxy doctor: failed at check 4",
		append(slices.Clone(names), sweepImage, `profile "`+sweepName+`"`, stopped, "Fix: "+fix),
		[]string{"✓ ok", "✗ failed", "? unknown"},
		"Failed at check 4 of 13: "+names[3],
		doctorReport(checks, failed))
	soft := Result{OK: true, FailedAt: -1}
	notes := map[int]CheckOutcome{
		8:  {OK: true, Soft: softDegraded, Detail: "TCP tunnelled; UDP/DNS inconclusive: " + sweepToken},
		10: {OK: true, Soft: softUnknown, Detail: "IPv6 posture UNKNOWN for " + sweepName + "\n" + sweepMultiline},
		11: {OK: true, Soft: softUnknown, Detail: "hy2 TCP-TLS probe inconclusive: " + sweepCJK},
		12: {OK: true, Soft: softDegraded, Detail: "ADVISORY: inbound sockopt.tproxy missing on " + sweepIPv6},
	}
	softValues := slices.Clone(names)
	for i, name := range names {
		o, ok := notes[i]
		if !ok {
			o = CheckOutcome{OK: true}
		}
		soft.Outcomes = append(soft.Outcomes, checkResult{Name: name, CheckOutcome: o})
		softValues = append(softValues, o.Detail)
	}
	add("ws proxy doctor: soft findings",
		softValues,
		[]string{"✓ ok", "⚠ degraded", "? unknown"},
		"9 of 13 checks passed, 2 degraded, 2 unknown",
		doctorReport(checks, soft))

	probe := proxyengine.ProbeResult{DirectIP: sweepIPv6, ProxiedIP: "198.51.100.9", Tunneled: true, Latency: 182 * time.Millisecond}
	tunnel := []string{sweepIPv6, "198.51.100.9", "182ms"}
	add("ws proxy test: tunnelled", tunnel,
		[]string{"✓ yes", "✓ tunnelled (exit 198.51.100.9)"},
		"Tunnel active — exit IPs differ",
		tunnelReport(probe, "198.51.100.9"))
	add("ws proxy test: DNS leak", tunnel,
		[]string{"✓ yes", "✗ leak (exit " + sweepIPv6 + " is the direct IP)"},
		"UDP/DNS LEAK -- resolver saw your real IP "+sweepIPv6+" (untunnelled)",
		tunnelReport(probe, sweepIPv6))
	add("ws proxy test: DNS inconclusive", tunnel,
		[]string{"✓ yes", "? inconclusive"},
		"Tunnel active — exit IPs differ",
		tunnelReport(probe, ""))
	add("ws proxy test: tunnel down",
		[]string{sweepIPv6, "2.345s"},
		[]string{"✗ no", "- not probed"},
		"Tunnel NOT active — direct and proxied exit IPs are the same",
		tunnelReport(proxyengine.ProbeResult{DirectIP: sweepIPv6, ProxiedIP: sweepIPv6, Latency: 2345 * time.Millisecond}, ""))

	doctor := []*doctorCheck{
		{Name: "orphan-mcp-subprocess", Band: bandGreen, Detail: "0 orphan MCP subprocesses"},
		{Name: "vault-ai-token", Band: bandRed, Detail: "VAULT_AI_TOKEN unset or empty",
			Remediation: "provision the token, then re-run: ws vault doctor"},
		{Name: "xrepo-contract-parity", Band: bandYellow, Detail: "check-xrepo-contract.sh not found under /home/" + sweepName,
			Remediation: "restore the contract script: " + sweepToken},
		{Name: "索引-一致性", Band: statusBand("purple"), Detail: sweepMultiline + "\n" + sweepCJK},
	}
	values := []string{}
	for _, c := range doctor {
		values = append(values, c.Name, c.Detail)
		if c.Remediation != "" {
			values = append(values, "Fix: "+c.Remediation)
		}
	}
	add("ws vault doctor: mixed bands", values,
		[]string{"✓ ok", "✗ failed", "⚠ degraded", "? unknown"},
		"Overall: red (exit 2)",
		vaultDoctorReport(doctor, bandRed, 2))

	status := &statusReport{OverallBand: bandRed, ExitCode: 2, Signals: []statusSignal{
		{Label: "mcp reachable", Band: bandGreen, Detail: "answered in 12ms"},
		{Label: "index freshness", Band: bandYellow, Detail: "skipped (MCP unreachable)"},
		{Label: "dedup backlog", Band: bandRed, Detail: sweepToken},
		{Label: "数据完整性", Band: statusBand("purple"), Detail: sweepCJK},
	}}
	values = nil
	for _, sig := range status.Signals {
		values = append(values, sig.Label, sig.Detail)
	}
	add("ws vault status: mixed bands", values,
		[]string{"✓ ok", "⚠ degraded", "✗ failed", "? unknown"},
		"Overall: red (exit 2)",
		vaultStatusReport(status))

	current, projection := predictReport(40, &predictResult{
		CurrentRowsPerStream: map[string]int{sweepName: 10, "数据流": 40, "mcp": 70},
		ProjectedNewRows:     200, EstimatedDedupSeconds: 3.5, ProjectedSegmentCount: 7,
	})
	add("ws vault predict-bulk-load 40",
		[]string{sweepName + "  10", "数据流", "mcp", "total", "120", "Projection for 40 notes", "Projected New Rows",
			"Estimated Dedup Time", "3.50s", "Projected Segments"},
		nil, "", current, projection)
	current, projection = predictReport(1, &predictResult{})
	add("ws vault predict-bulk-load 1: no rows",
		[]string{"total 0", "Projection for 1 note"},
		nil, "", current, projection)

	add("the profile wizard's summary",
		[]string{sweepName, sweepImage, "curl git " + sweepToken, "go, golangci-lint, 数据工具", "true"},
		nil, "",
		profileSummary(sweepName, sweepImage, "curl git "+sweepToken, []string{"go", "golangci-lint", "数据工具"}, true))
	add("the profile wizard's summary: no packages or tools",
		[]string{"go-custom", "debian:bookworm-slim", "false"},
		nil, "",
		profileSummary("go-custom", "debian:bookworm-slim", "", nil, false))
	return reports
}

// TestReportsFitEveryWidth is phase 5's deliverable: every report at every
// width from MinWidth to 200, in both glyph modes, and not one line wider
// than the width.
func TestReportsFitEveryWidth(t *testing.T) {
	sweepReportsIn(t, []bool{false, true})
}

// sweepReportsIn sweeps every report in each glyph mode of modes.
func sweepReportsIn(t *testing.T, modes []bool) {
	t.Helper()
	var renders []func(*output.Stream) string
	for _, r := range sweepReports(t) {
		renders = append(renders, r.render)
	}
	sweepRenders(t, renders, modes)
}

const reportsAmbiWideEnv = "WS_TEST_REPORTS_AMBIWIDE"

// TestReportsFitEveryWidthAmbiguousWide runs the sweep under
// RUNEWIDTH_EASTASIAN=1.
func TestReportsFitEveryWidthAmbiguousWide(t *testing.T) {
	ambiguousWide(t, "TestReportsFitEveryWidthAmbiguousWide", reportsAmbiWideEnv, func(t *testing.T) {
		sweepReportsIn(t, []bool{true})
	})
}

// TestReportsLoseNothingInAPipe: piped, with no width, every report holds
// every value and note its fixture was built from, each paragraph whole on
// one line — prose collapses a run of spaces, so the comparison does too —
// with no truncation marker; every state it draws renders its mark and its
// word; and the caption closes it.
func TestReportsLoseNothingInAPipe(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		s := output.NewStreamAt(io.Discard, output.WidthUnbounded, false, output.ColourNone, ascii)
		marker := "…"
		if ascii {
			marker = "..."
		}
		for _, r := range sweepReports(t) {
			got := r.render(s)
			var lines []string
			for _, line := range strings.Split(got, "\n") {
				lines = append(lines, strings.Join(strings.Fields(line), " "))
			}
			whole := func(what, v string) {
				for _, para := range strings.Split(v, "\n") {
					para = strings.Join(strings.Fields(para), " ")
					if para != "" && !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, para) }) {
						t.Errorf("%s (ascii %v): the pipe lost the %s %q:\n%s", r.name, ascii, what, para, got)
					}
				}
			}
			for _, v := range r.values {
				whole("value", v)
			}
			for _, st := range r.states {
				if ascii {
					st = strings.NewReplacer("✓ ", "+ ", "✗ ", "x ", "⚠ ", "! ").Replace(st)
				}
				whole("state", st)
			}
			if strings.Contains(got, marker) {
				t.Errorf("%s (ascii %v): the pipe cut something:\n%s", r.name, ascii, got)
			}
			if r.caption != "" && !strings.HasSuffix(got, "\n"+r.caption) {
				t.Errorf("%s (ascii %v): the report does not close with its caption %q:\n%s", r.name, ascii, r.caption, got)
			}
		}
	}
}

// TestReportsStackInOrder: where a block stacks, every key goes above its
// value and the block keeps its order — the projection below 36 columns, and
// ws proxy status's workspaces under a 64-character name — and a stacked
// state carries its role's colour on every line it wraps to.
func TestReportsStackInOrder(t *testing.T) {
	_, projection := predictReport(40, &predictResult{ProjectedNewRows: 200, EstimatedDedupSeconds: 3.5, ProjectedSegmentCount: 7})
	plain := func(w int) *output.Stream { return output.NewStreamAt(io.Discard, w, true, output.ColourNone, false) }
	want := "Projection for 40 notes\n" +
		"  Projected New Rows\n    200\n" +
		"  Estimated Dedup Time\n    3.50s\n" +
		"  Projected Segments\n    7"
	if got := projection.Render(plain(35)); got != want {
		t.Errorf("the projection at 35 columns:\n%s\nwant:\n%s", got, want)
	}
	if got := projection.Render(plain(36)); !strings.Contains(got, "\n  Projected New Rows    200\n") {
		t.Errorf("the projection at 36 columns stacks; want it aligned:\n%s", got)
	}

	// At 70 columns the 64-character key leaves the value column 2 cells,
	// under §4.4's 12: each workspace's key on a line of its own, its verdict
	// below it at an indent of 4, wrapped, in the scan's order.
	ws := proxyStatusReport(docker.Status{Running: true}, config.Config{ProxyNetwork: "ws-proxy", ProxyIP: sweepIPv6},
		sweepProtection, nil)[1]
	lines := strings.Split(ws.Render(plain(70)), "\n")
	painted := strings.Split(ws.Render(output.NewStreamAt(io.Discard, 70, true, output.ColourTrue, false)), "\n")
	if len(painted) != len(lines) || lines[0] != "Workspaces" {
		t.Fatalf("the Workspaces block at 70 columns:\n%s", strings.Join(lines, "\n"))
	}
	i := 1
	for _, p := range []struct{ key, value string }{
		{sweepName, "✓ protected"},
		{"数据科学", "✗ unprotected: " + sweepProtection[1].Detail},
		{"api", "? unknown: " + sweepProtection[2].Detail},
	} {
		if i >= len(lines) || lines[i] != "  "+p.key {
			t.Fatalf("line %d of the Workspaces block: want the key %q:\n%s", i, p.key, strings.Join(lines, "\n"))
		}
		i++
		first := i
		var value []string
		for i < len(lines) && strings.HasPrefix(lines[i], "    ") {
			value = append(value, strings.TrimSpace(lines[i]))
			i++
		}
		// The token is hard-broken, so the lines are compared without spaces.
		if strip := func(s string) string { return strings.Join(strings.Fields(s), "") }; strip(strings.Join(value, "")) != strip(p.value) {
			t.Errorf("the value under %q reads %q, want %q", p.key, strings.Join(value, " "), p.value)
		}
		sgr := func(line string) string {
			if !strings.HasPrefix(line, "\x1b[") {
				return ""
			}
			return line[:strings.IndexByte(line, 'm')+1]
		}
		for j := first; j < i; j++ {
			if sgr(painted[j]) == "" || sgr(painted[j]) != sgr(painted[first]) {
				t.Errorf("line %d, of the value under %q, is not painted as its first line is: %q", j, p.key, painted[j])
			}
		}
	}
	if caption := strings.Join(lines[i:], " "); caption != "1 of 3 workspace(s) UNPROTECTED — route not via proxy (run: ws proxy fix-routes)" {
		t.Errorf("the block does not close with the protection summary: %q", caption)
	}
}

// The reports baseline: each report command piped at COLUMNS=80 and
// COLUMNS=40, in the streams fixture, with a fake Docker Engine API where the
// command reads the daemon, with its exit code and both streams, in the
// format of testdata/error-protocol.golden. It is what a reviewer reads to see
// the reports of phase 5. Re-record by naming the cases the change rewrites or
// adds, comma-separated:
//
//	go test ./cmd -run '^TestReportsBaseline$' -update-reports-baseline=<case>,<case>
//
// Any other case whose rendering differs from its record fails the run and
// nothing is written.

var updateReportsBaseline = flag.String("update-reports-baseline", "",
	"comma-separated names of the cases testdata/reports.golden may rewrite or add")

const reportsBaselineFile = "testdata/reports.golden"

// reportsCases are the baseline's cases, each report at both widths.
func reportsCases() []streamsRow {
	var cases []streamsRow
	for _, c := range []streamsRow{
		{name: "ws proxy check: no daemon", args: []string{"proxy", "check"}},
		{name: "ws proxy check: all ok", args: []string{"proxy", "check"}, docker: &fakeHealthyProxy},
		{name: "ws proxy status: stopped and no network", args: []string{"proxy", "status"}, docker: &fakeDockerState{}},
		{name: "ws proxy status: one workspace unprotected", args: []string{"proxy", "status"}, docker: &fakeHealthyProxy},
		{name: "ws proxy test: tunnel and DNS tunnelled", args: []string{"proxy", "test"}, stub: "tunnel-up", docker: &fakeHealthyProxy},
		{name: "ws proxy test: DNS leak", args: []string{"proxy", "test"}, stub: "tunnel-dns-leak", docker: &fakeHealthyProxy},
		{name: "ws proxy test: tunnel down", args: []string{"proxy", "test"}, stub: "tunnel-down", docker: &fakeHealthyProxy},
		{name: "ws proxy doctor: no daemon", args: []string{"proxy", "doctor"}},
		{name: "ws proxy doctor: the image's datapath differs", args: []string{"proxy", "doctor"}, docker: &fakeHealthyProxy},
		{name: "ws vault doctor: mixed bands", args: []string{"vault", "doctor"}, stub: "vault-doctor-mixed"},
		{name: "ws vault status: mixed bands", args: []string{"vault", "status"}, stub: "vault-status-mixed"},
		{name: "ws vault predict-bulk-load 40: a projection", args: []string{"vault", "predict-bulk-load", "40"}, stub: "predict-projection"},
	} {
		for _, w := range []string{"80", "40"} {
			row := c
			row.name, row.env = c.name+" at COLUMNS="+w, []string{"COLUMNS=" + w}
			cases = append(cases, row)
		}
	}
	return cases
}

func TestReportsBaseline(t *testing.T) {
	var got strings.Builder
	var order []string
	for _, c := range reportsCases() {
		fx := newStreamsFixture(t)
		if c.setup != nil {
			c.setup(t, fx)
		}
		code, stdout, stderr := runStreamsChild(t, fx, c)
		got.WriteString(renderErrorCase(c.name, code, stdout, stderr))
		order = append(order, c.name)
	}

	if *updateReportsBaseline != "" {
		allowed := map[string]bool{}
		for _, name := range strings.Split(*updateReportsBaseline, ",") {
			allowed[strings.TrimSpace(name)] = true
		}
		// A missing file is an empty record: every case is then new.
		recorded, err := os.ReadFile(reportsBaselineFile)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("reading %s: %v", reportsBaselineFile, err)
		}
		if refused := refusedRewrites(splitErrorCases(string(recorded)), splitErrorCases(got.String()), order, allowed); len(refused) > 0 {
			t.Fatalf("refusing to rewrite %s; nothing was written:\n%s", reportsBaselineFile, strings.Join(refused, "\n"))
		}
		if err := os.WriteFile(reportsBaselineFile, []byte(got.String()), 0o644); err != nil {
			t.Fatalf("writing %s: %v", reportsBaselineFile, err)
		}
		t.Logf("rewrote %s (%d cases; named: %s)", reportsBaselineFile, len(order), *updateReportsBaseline)
		return
	}

	want, err := os.ReadFile(reportsBaselineFile)
	if err != nil {
		t.Fatalf("reading %s: %v (record it with -update-reports-baseline)", reportsBaselineFile, err)
	}
	if got.String() == string(want) {
		return
	}
	gotCases, wantCases := splitErrorCases(got.String()), splitErrorCases(string(want))
	for _, name := range order {
		if gotCases[name] != wantCases[name] {
			t.Errorf("case %s differs from the baseline.\n--- baseline:\n%s--- now:\n%s", name, wantCases[name], gotCases[name])
		}
	}
	if len(wantCases) != len(order) {
		t.Errorf("the baseline holds %d cases and the table %d; re-record deliberately, not by accident", len(wantCases), len(order))
	}
	if !t.Failed() {
		t.Errorf("the baseline differs from the rendering outside any case block; re-record deliberately, not by accident")
	}
}

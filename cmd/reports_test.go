package cmd

import (
	"flag"
	"os"
	"strings"
	"testing"
)

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

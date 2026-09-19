package output

// Two imports, and no more: x/ansi is here for fxExpandTabs alone, which
// measures the column a tab advances from. A helper landed ahead of its first
// caller is `func fxExpandTabs is unused` at the acceptance gate, which runs
// golangci-lint over the _test.go files too, so the helper and its callers
// land together.
import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The §6 acceptance corpus: the raw material, the eight table builders this
// file adds on top of acceptance_test.go's tableFixtures(), and fxCorpus().
//
// NOTHING IN THIS FILE RE-DECLARES A SHARED NAME. `fixture`, `fxState`,
// `fxRow`, `renderCase`, `newRenderCase`, `fxBudget` and `sweepStream` are
// alloc_policy_test.go's; `fxStateVocabulary`, `fxMark` and `fxBadge` are
// state_test.go's; `fxEscCause`, `fxEscSurvivors` and `tableFixtures` are
// acceptance_test.go's. Go has one package scope across every _test.go file in
// a package, so a second declaration is a compile error.

// ------------------------------------------------------------ raw material

// fxName64 is the ceiling ValidateName permits (§6.2).
const fxName64 = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffffgggggggghhhhhhhh"

// fxIPv6 is a bracketed IPv6 endpoint (§6.2). TruncHead columns exist for it:
// the port must survive.
const fxIPv6 = "[2001:db8:85a3::8a2e:370:7334]:443"

const fxBaseImage = "mcr.microsoft.com/devcontainers/base:ubuntu-24.04"

// fxToken200 is a 200-character run with no break opportunity (§6.2). §4.4
// hard-breaks it at the budget: the width contract wins over "wrapped, never
// truncated".
var fxToken200 = strings.Repeat("0123456789abcdefghij", 10)

// fxMultilineErr is a multi-line upstream error (§6.2) — the shape §4.8
// captures from a child process and places in Problem.Cause.
const fxMultilineErr = "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n" +
	"Is the docker daemon running?\n" +
	"error during connect: Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.47/containers/json\": " +
	"dial unix /var/run/docker.sock: connect: permission denied"

// fxEscCause — §6.7's ESC-laden upstream text — and fxEscSurvivors are
// declared in acceptance_test.go and consumed here. The escapes are zero-width
// to ansi.StringWidth, which is exactly why a width sweep over this text
// passes while the operator's terminal is being rewritten.

// fxEmojiCause is upstream text carrying emoji-presentation sequences.
//
// §6.2's list does not name this one and measurement says it must: ⚠️ ✔️ ℹ️ are
// each a base character followed by U+FE0F — two runes, per-rune widths 1 and
// 0, cluster width 2. A cut that steps by rune spends one cell of budget on a
// two-cell glyph. That is decision D-3's defect class, and it is a DIFFERENT
// class from fxCJKNote, where every rune is its own two-cell cluster and a
// rune-stepped cut is merely mismeasured rather than mis-segmented.
//
// It is permanent corpus rather than a regression case because the input is
// routine: §4.8 fills Problem.Cause from a child process's captured stderr,
// and Docker, devpod and CI tooling emit these glyphs in ordinary
// diagnostics. It is swept in BOTH glyph modes because §4.5 governs the
// glyphs the LAYER emits and has no effect on user data flowing through it.
const fxEmojiCause = "⚠️ buildkit: ✔️ 3 of 5 layers cached, ℹ️ 2 rebuilt; " +
	"⚠️ the base image digest drifted since the lockfile was written"

// fxEmojiRun is the degenerate form: nothing but emoji-presentation clusters,
// so every cut position the sweep visits lands on one.
var fxEmojiRun = strings.Repeat("⚠️", 20)

// fxCJKNote is CJK prose: every rune two cells under both Ambiguous
// conventions, which is what makes a rune-counting width function overflow.
const fxCJKNote = "拨号失败：连接超时（30 秒），代理配置文件 de-fra-01 无法访问，请检查路由表"

// fxEsc wraps text in control sequences: a line clear in front and an OSC 0
// title rewrite behind. Two ESC bytes, zero cells to ansi.StringWidth, and a
// payload — "pwned" — that must never reach the terminal as text.
//
// It exists because D-13 sanitises ELEVEN surfaces across blocks.go and, until
// this corpus, only one of them (Problem.Cause) had a fixture carrying an
// escape at all. A surface that silently stopped sanitising would have kept
// the whole suite green. The other ten are covered by the four fixtures at the
// end of fxCorpusBuild, and assertESCContainment asserts both halves over the
// corpus: zero ESC bytes out, and no "pwned" surviving as text.
//
// Each of the ten was proved, one deleted Sanitise at a time, every one
// restored — eight plants, because renderPairs covers Fact.K with Fact.V and
// renderRemedies covers Remedy.Label with Remedy.Cmd:
//
//	Problem.Title        12 violations, first problem/esc-surfaces @ 29
//	Fact.K + Fact.V      24, first problem/esc-surfaces @ 29
//	Remedy.Label + .Cmd  24, first problem/esc-surfaces @ 29
//	Empty.Subject        12, first empty/esc-surfaces @ 29
//	KV.Title             12, first kv/esc-surfaces @ 29
//	Checks.Title         12, first checks/esc-surfaces @ 29
//	Check.Name           12, first checks/esc-surfaces @ 29
//	Check.Note           12, first checks/esc-surfaces @ 29
func fxEsc(text string) string { return "\x1b[2K" + text + "\x1b]0;pwned\x07" }

// ------------------------------------------------------------ fixture type
//
// The fields this corpus fills in, and what reads each back:
//
//	name, kind, spec        what it is and which §6 clause puts it here
//	isTable, cols, rows,    the allocator's input and the two Table fields the
//	  caption, wideFlag     assertions read back, for a table fixture
//	states                  states that must appear in FULL in the render
//	                        (§6.5). Only blocks that never abbreviate a badge
//	                        declare them: Checks pads to the vocabulary's
//	                        widest mark and word, so the whole badge is always
//	                        there. Table state cells are held to the weaker,
//	                        truncation-aware rule inside assertStateStructure,
//	                        because the allocator may legitimately squeeze
//	                        them.
//	prefixState,            declared by the message helpers, whose shape is a
//	  hasPrefixState        mark followed by prose rather than a mark followed
//	                        by a state word (§4.7's vocabulary is `✓ workspace
//	                        started`, not `✓ ok`). The assertion is that the
//	                        mark survives at the head of the first line at
//	                        every width.
//	fidelity                the source strings this block WRAPS (§4.4): every
//	                        one must survive the render whole once line breaks
//	                        and the hanging indent are collapsed. Declared per
//	                        fixture rather than derived inside the assertion,
//	                        because a derivation would re-read the same fields
//	                        the renderer read and could not disagree with it.
//	                        Table fixtures declare none — inside the grid,
//	                        content is legitimately truncated.
//	render                  the closure the sweep calls.

// fxProblemSources, fxEmptySources, fxKVSources and fxChecksSources list the
// fields §4.4 says are wrapped.
//
// Problem.Title IS among them. Problem.Render wraps the whole title at
// budget-hang and renders the first line unindented at that same width, so no
// character is lost at any width and the fidelity assertion has something true
// to assert. If this line is ever removed, the reason must be a NEW
// measurement, not this comment's absence.
func fxProblemSources(p Problem) []string {
	out := []string{}
	if p.Title != "" {
		out = append(out, p.Title)
	}
	if p.Cause != "" {
		out = append(out, p.Cause)
	}
	for _, f := range p.Facts {
		out = append(out, f.K, f.V)
	}
	for _, st := range p.Steps {
		out = append(out, st.Label, st.Cmd)
	}
	return out
}

func fxEmptySources(e Empty) []string {
	out := []string{e.Subject}
	for _, st := range e.Steps {
		out = append(out, st.Label, st.Cmd)
	}
	return out
}

func fxKVSources(k KV) []string {
	out := []string{}
	if k.Title != "" {
		out = append(out, k.Title)
	}
	for _, f := range k.Pairs {
		out = append(out, f.K, f.V)
	}
	return out
}

func fxChecksSources(c Checks) []string {
	out := []string{}
	if c.Title != "" {
		out = append(out, c.Title)
	}
	for _, it := range c.Items {
		out = append(out, it.Name)
		if it.Note != "" {
			out = append(out, it.Note)
		}
	}
	return out
}

// fxExpandTabs is the harness's OWN copy of the tab rule (§4.4 / text.go's
// expandTabs), for the same reason fxStateVocabulary is the harness's own copy
// of §4.5: an expectation computed with the code under test cannot disagree
// with that code's mistakes. It short-circuits on tab-free text, so every
// fixture in the corpus that carries no tab is untouched by it.
//
// It is deliberately naive where the production rule is careful: it steps by
// RUNE and measures each rune on its own, where expandTabs steps by grapheme
// cluster. The two agree on every string in which a tab is preceded, on its
// own line, only by single-rune clusters — which is every string in this
// corpus, table/tab-in-cell being pure ASCII. They diverge on a tab preceded
// by an emoji-presentation sequence, where a rune-stepped column counter puts
// the next tab stop one cell early — measured, expandTabs("⚠️\tx") is
// "⚠️" + 6 spaces + "x" and fxExpandTabs of the same string is
// "⚠️" + 7 spaces + "x". A fixture that mixes tabs with multi-rune clusters
// must therefore land with this helper corrected; it is not a free addition.
func fxExpandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	out, col := "", 0
	for _, r := range s {
		switch r {
		case '\n':
			out, col = out+"\n", 0
		case '\t':
			n := 8 - col%8
			out, col = out+strings.Repeat(" ", n), col+n
		default:
			out, col = out+string(r), col+ansi.StringWidth(string(r))
		}
	}
	return out
}

// --------------------------------------------------------- table builders

// tableFixtureFrom is the whole of the *Table -> fixture conversion: the
// fixture type carries the allocator's input plus the two Table fields the
// assertions read back, with the Table value held in the render closure.
// Written once, used by every builder below.
func tableFixtureFrom(name, spec string, t *Table, states ...fxState) fixture {
	return fixture{
		name: name, kind: "table", spec: spec,
		isTable: true, cols: t.Cols, rows: t.Rows,
		caption: t.Caption, wideFlag: t.WideFlag,
		states: states,
		render: func(s *Stream) string { return t.Render(s) },
	}
}

// fxListTable is `ws list`: the table that carries state cells, so the
// allocator's ColState exemption (§4.3 step 5(b)) and §6.5 are both exercised.
func fxListTable(name64 bool) *Table {
	t := &Table{
		Cols: []Col{
			{Title: "NAME", Prio: 1, Min: 12, Trunc: TruncMid},
			{Title: "STATUS", Prio: 1, Min: 6, Kind: ColState},
			{Title: "PROFILE", Prio: 3, Min: 8, Trunc: TruncTail},
			{Title: "PROXY", Prio: 2, Min: 6, Atomic: true},
		},
		Caption:  "5 workspaces, 2 running, 2 via proxy",
		WideFlag: "--wide",
	}
	type row struct {
		name  string
		st    State
		label string
		prof  string
		proxy string
	}
	data := []row{
		{"api", StateOK, "running", "go", "via proxy"},
		{"web-frontend", StateOK, "running", "web", "direct"},
		{"ml-training", StateBusy, "starting", "python-datascience-cuda", "via proxy"},
		{"ops", StateIdle, "stopped", "devops", "direct"},
		{"legacy-billing", StateIdle, "not created", "default", "direct"},
	}
	if name64 {
		data = []row{
			{fxName64, StateOK, "running", "python-datascience-cuda", "via proxy"},
			{"api", StateIdle, "not created", "go", "direct"},
		}
		t.Caption = "2 workspaces, 1 running"
	}
	for _, d := range data {
		t.Rows = append(t.Rows, fxRow(Text(d.name), Mark(d.st, d.label), Text(d.prof), Text(d.proxy)))
	}
	return t
}

// fxProxyProfileTable carries the bracketed IPv6 endpoint in a TruncHead
// column. Its un-droppable columns are deliberately cheap enough to satisfy
// §4.3's construction-time rejection rule at MinWidth.
func fxProxyProfileTable() *Table {
	return &Table{
		Cols: []Col{
			// A state column's Min must hold mark + space + its shortest
			// word, or §4.3's step 5(b) exemption protects nothing: the
			// allocator would still be free to shrink it to its Min by step 3
			// and render `✓…` for every state alike. 5 cells holds `- off`.
			{Title: "ACTIVE", Prio: 1, Min: 5, Kind: ColState},
			{Title: "NAME", Prio: 1, Min: 8, Trunc: TruncMid},
			{Title: "PROTO", Prio: 3, Min: 5, Trunc: TruncTail},
			{Title: "ADDRESS:PORT", Prio: 2, Min: 12, Trunc: TruncHead},
			{Title: "SNI", Prio: 4, Min: 12, Trunc: TruncHead},
			{Title: "ID", Prio: 5, Min: 8, Atomic: true},
		},
		Rows: [][]Cell{
			fxRow(Mark(StateOK, "on"), Text("de-fra-01"), Text("vless/reality"),
				Text("de-fra-01.example-vpn.net:443"), Text("www.microsoft.com"), Text("b3f1a20c")),
			fxRow(Mark(StateIdle, "off"), Text("backup-nl"), Text("vless/ws-tls"),
				Text("nl-ams-02.example-vpn.net:8443"), Text("cdn.jsdelivr.net"), Text("7c2e04ab")),
			fxRow(Mark(StateIdle, "off"), Text("hy2-fallback"), Text("hysteria2"),
				Text(fxIPv6), Text("speed.cloudflare.com"), Text("-")),
		},
		Caption:  "3 profiles, active de-fra-01",
		WideFlag: "--wide",
	}
}

// fxCJKTable is the CJK-only row of §6.2.
func fxCJKTable() *Table {
	return &Table{
		Cols: []Col{
			{Title: "名称", Prio: 1, Min: 8, Trunc: TruncMid},
			{Title: "状态", Prio: 1, Min: 6, Kind: ColState},
			{Title: "工具链", Prio: 2, Min: 12, Trunc: TruncTail},
		},
		Rows: [][]Cell{
			fxRow(Text("中文工作区"), Mark(StateOK, "运行中"),
				Text("工具链, 编译器, 测试框架, 覆盖率, 静态分析, 格式化, 依赖管理")),
			fxRow(Text("日本語環境"), Mark(StateIdle, "停止"),
				Text("コンパイラ, テストフレームワーク, カバレッジ, 静的解析")),
		},
		Caption:  "2 个配置",
		WideFlag: "--wide",
	}
}

// fxEmojiTable carries emoji-presentation sequences in table cells and in the
// caption (§6.2), so the clusters travel the allocator, all three truncation
// modes and the caption's wrap budget.
func fxEmojiTable() *Table {
	return &Table{
		Cols: []Col{
			{Title: "NAME", Prio: 1, Min: 8, Trunc: TruncMid},
			{Title: "STATUS", Prio: 1, Min: 6, Kind: ColState},
			{Title: "LAST BUILD", Prio: 2, Min: 12, Trunc: TruncTail},
			{Title: "DIGEST", Prio: 3, Min: 8, Trunc: TruncHead},
		},
		Rows: [][]Cell{
			fxRow(Text("api"), Mark(StateOK, "running"), Text(fxEmojiCause), Text("sha256:9f3c1a2b")),
			fxRow(Text(fxEmojiRun), Mark(StateAdvisory, "degraded"),
				Text("⚠️ ✔️ ℹ️ mixed with ascii and 中文"), Text(fxEmojiRun)),
		},
		Caption:  "2 workspaces, ⚠️ 1 degraded, ✔️ 1 healthy",
		WideFlag: "--wide",
	}
}

func fxZeroRowTable() *Table {
	t := fxProxyProfileTable()
	t.Rows = nil
	t.Caption = "0 profiles"
	return t
}

func fxSingleColTable() *Table {
	return &Table{
		Cols:    []Col{{Title: "WORKSPACE", Prio: 1, Min: 8, Trunc: TruncMid}},
		Rows:    [][]Cell{fxRow(Text("api")), fxRow(Text(fxName64)), fxRow(Text("中文工作区"))},
		Caption: "3 workspaces",
	}
}

func fxLongCaptionTable() *Table {
	t := fxListTable(false)
	t.Caption = "5 workspaces, 2 running, 2 via proxy, 1 not created, 1 stopped; " +
		"the active proxy profile is de-fra-01 and the routing table was last " +
		"refreshed 41 minutes ago by ws proxy fix-routes"
	return t
}

func fxUnbreakableTable() *Table {
	return &Table{
		Cols: []Col{
			{Title: "NAME", Prio: 1, Min: 10, Trunc: TruncMid},
			{Title: "DIGEST", Prio: 2, Min: 12, Trunc: TruncHead},
		},
		Rows:    [][]Cell{fxRow(Text("api"), Text(fxToken200)), fxRow(Text(fxToken200), Text("sha256:deadbeef"))},
		Caption: "2 images",
	}
}

// fxTabTable is the TAB fixture. §4.4 mandates that Sanitise PRESERVE tab, and
// ansi.StringWidth measures U+0009 at 0 cells while a terminal advances to the
// next tab stop — so every width the layer computes over text containing a tab
// is wrong in the one direction the width contract forbids. Tabs are placed in
// a cell, in a column TITLE and in the CAPTION, because those are three
// different measurement paths.
func fxTabTable() fixture {
	cols := []Col{
		{Title: "NAME", Prio: 1, Min: 8, Trunc: TruncMid},
		{Title: "TOOLS\tSET", Prio: 2, Min: 12, Trunc: TruncTail},
	}
	rows := [][]Cell{
		fxRow(Text("go"), Text("go\tnode\tpython")),
		fxRow(Text("web"), Text("node\tbun\tdeno\tpnpm")),
	}
	tbl := Table{Cols: cols, Rows: rows, Caption: "2 profiles\tof 8", WideFlag: "--wide"}
	return fixture{
		name:    "table/tab-in-cell",
		kind:    "table",
		spec:    "§4.4 Sanitise preserves tab; ansi.StringWidth measures it at 0 cells",
		isTable: true, cols: cols, rows: rows,
		caption: "2 profiles\tof 8", wideFlag: "--wide",
		render: func(s *Stream) string { return tbl.Render(s) },
	}
}

// fxEscTable puts §6.7's control sequences where D-13 says they also arrive:
// in a CELL, in a column TITLE and in the CAPTION. Those are three different
// paths — Cell.display, Col.title() and captionText — and the escapes are
// zero-width to ansi.StringWidth, so the width sweep passes over all three
// while the operator's terminal is being rewritten.
//
// Before this fixture, assertESCContainment had never seen an escape anywhere
// but Problem.Cause, so these three surfaces would have shipped with no
// detector at all: one that silently stopped sanitising would have kept the
// whole suite green. Measured, one deletion at a time, each restored:
//
//	SanitiseInline out of Cell.display    esc_containment 6 violations, first
//	                                      "table/esc-in-cell @ 29 (mode 0):
//	                                      6 ESC bytes at ColourNone"
//	SanitiseInline out of Col.title()     6 violations, first the same fixture
//	                                      with 1 ESC byte
//	Sanitise out of captionText           6 violations, likewise
//
// Three deletions, three reds, every one naming this fixture — which is the
// evidence that no fourth hole is hiding behind the first.
func fxEscTable() fixture {
	cols := []Col{
		{Title: "NAME", Prio: 1, Min: 8, Trunc: TruncMid},
		{Title: "STATUS\x1b[2K", Prio: 2, Min: 12, Trunc: TruncTail},
	}
	rows := [][]Cell{
		fxRow(Text("api"), Text(fxEscCause)),
		fxRow(Text("web"), Text("\x1b]8;;https://example.invalid\x1b\\clickable\x1b]8;;\x1b\\")),
	}
	caption := "2 workspaces\x1b]0;pwned\x07"
	tbl := Table{Cols: cols, Rows: rows, Caption: caption, WideFlag: "--wide"}
	return fixture{
		name:    "table/esc-in-cell",
		kind:    "table",
		spec:    "§6.7 / D-13: escapes in a cell, a column title and a caption are contained",
		isTable: true, cols: cols, rows: rows,
		caption: caption, wideFlag: "--wide",
		render: func(s *Stream) string { return tbl.Render(s) },
	}
}

// ------------------------------------------------------------- the corpus

// fxCorpus IS MEMOISED, AND THE MEMOISATION IS LOAD-BEARING.
//
// runSweep calls fxCorpus() on every invocation, and a mutation harness calls
// runSweep once per mutant. tableFixtures() goes through NewTableBlock ->
// validateCols, whose chrome is chromeFor(n) = 3n+1-mutants.ChromeOff — so
// rebuilt inside the mutant window the fixture set is built against a MUTATED
// constant. On the chrome mutants the degenerate caption-only Col set stops
// being refused and tableFixtures()' own guard fires:
//
//	panic: table/degenerate-caption-only is declared degenerate but the
//	constructor accepts it
//
// A FIXTURE SET WHOSE VALIDITY DEPENDS ON A MUTATED CONSTANT IS NOT A FIXTURE
// SET. TestTableMutantsRedenTheBlockChecks answers the same defect by hoisting
// tableFixtures() above its loop; here that is not available, because runSweep
// rebuilds the corpus by design and is called from inside such a loop.
// Building it once, on first use, with the switches clean, is what makes the
// clean baseline and every mutant run compare the same inputs.
var fxCorpusOnce []fixture

func fxCorpus() []fixture {
	if fxCorpusOnce != nil {
		return fxCorpusOnce
	}
	fxCorpusOnce = fxCorpusBuild()
	return fxCorpusOnce
}

// fxCorpusBuild is the corpus itself. It STARTS FROM tableFixtures() and does
// not rebuild those eight rows: rebuilding them here would either duplicate a
// name or silently fork the corpus, so that TestCaptionDiscloses and
// TestAcceptanceSweep stopped testing the same tables.
func fxCorpusBuild() []fixture {
	out := tableFixtures()
	out = append(out, []fixture{
		tableFixtureFrom("table/list-name64", "§6.2 64-character name", fxListTable(true)),
		tableFixtureFrom("table/proxy-profiles-ipv6", "§6.2 bracketed IPv6 endpoint", fxProxyProfileTable()),
		tableFixtureFrom("table/cjk", "§6.2 CJK-only row", fxCJKTable()),
		tableFixtureFrom("table/zero-rows", "§6.2 zero-row table", fxZeroRowTable()),
		tableFixtureFrom("table/single-col", "§6.2 single-column table", fxSingleColTable()),
		tableFixtureFrom("table/long-caption", "§6.2 caption longer than the budget", fxLongCaptionTable()),
		tableFixtureFrom("table/unbreakable-200", "§6.2 200-character unbreakable token", fxUnbreakableTable()),
		fxTabTable(),
		fxEscTable(),
		tableFixtureFrom("table/emoji-presentation", "§6.2 emoji-presentation sequences (base + U+FE0F)", fxEmojiTable()),
	}...)

	problems := []struct {
		name, spec string
		p          Problem
		fid        []string // nil: derived from the block's wrapped fields
	}{
		{"problem/degraded", "§6.1 problem", Problem{
			Title: "Proxy is up, but 2 of 3 workspace routes are degraded",
			Cause: "iptables: no chain/target/match by that name (exit 1)",
			Facts: []Fact{{"stage", "post-start route fix"}, {"proxy", "devpod-proxy at 172.31.0.2"},
				{"failed", "ws-vault-ai, ws-lazyray"}},
			Steps: []Remedy{{"Retry the route fix", "ws proxy fix-routes"}, {"Diagnose the stack", "ws proxy doctor"}},
		}, nil},
		{"problem/multiline-cause", "§6.2 multi-line upstream error", Problem{
			Title: "Could not start workspace \"api\"",
			Cause: fxMultilineErr,
			Facts: []Fact{{"command", "devpod up api --ide none"}, {"exit", "1"}},
			Steps: []Remedy{{"Check the daemon", "systemctl --user status docker"}, {"Retry", "ws up api"}},
		}, nil},
		{"problem/name64", "§6.2 64-character name", Problem{
			Title: "Workspace \"" + fxName64 + "\" not found",
			Facts: []Fact{{"searched", "~/projects"}, {"endpoint", fxIPv6}},
			Steps: []Remedy{{"List workspaces", "ws list"}, {"Create it", "ws new " + fxName64}},
		}, nil},
		{"problem/unbreakable-200", "§6.2 200-character unbreakable token", Problem{
			Title: "Image pull failed for " + fxToken200,
			Cause: "manifest unknown: " + fxToken200,
			Facts: []Fact{{"digest", fxToken200}},
			Steps: []Remedy{{"Retry with the digest", "ws profile add x --image " + fxToken200}},
		}, nil},
		{"problem/cjk", "§6.2 CJK-only text", Problem{
			Title: "工作区启动失败：网络不可达",
			Cause: fxCJKNote,
			Facts: []Fact{{"阶段", "启动后路由修复"}, {"代理", "devpod-proxy 位于 172.31.0.2"}},
			Steps: []Remedy{{"重试", "ws proxy fix-routes"}},
		}, nil},
		{"problem/emoji-presentation", "§6.2 emoji-presentation sequences (base + U+FE0F)", Problem{
			Title: "⚠️ Could not rebuild profile \"go\"",
			Cause: fxEmojiCause + "\n" + fxEmojiRun,
			Facts: []Fact{{"stage", "buildkit export"}, {"markers", fxEmojiRun}},
			Steps: []Remedy{{"Retry the build", "ws profile rebuild go"}},
		}, nil},
		{"problem/esc-cause", "§6.7 Cause containing control sequences", Problem{
			Title: "Could not pull the base image",
			Cause: fxEscCause,
			Facts: []Fact{{"image", fxBaseImage}, {"registry", fxIPv6}},
			Steps: []Remedy{{"Retry", "ws profile rebuild default"}},
		}, append([]string{fxBaseImage, fxIPv6, "Retry", "ws profile rebuild default"}, fxEscSurvivors...)},
	}
	for _, pf := range problems {
		p, fid := pf.p, pf.fid
		if fid == nil {
			fid = fxProblemSources(p)
		}
		out = append(out, fixture{
			name: pf.name, kind: "problem", spec: pf.spec, fidelity: fid,
			render: func(s *Stream) string { return p.Render(s) },
		})
	}

	empties := []struct {
		name, spec string
		e          Empty
	}{
		{"empty/workspaces", "§6.1 empty", Empty{Subject: "workspaces", Steps: []Remedy{
			{"Create one", "ws new <name>"}, {"See profiles", "ws profiles"}}}},
		{"empty/long", "§6.2 subject and command longer than the budget", Empty{
			Subject: "proxy profiles for the currently selected endpoint " + fxName64,
			Steps: []Remedy{
				{"Add one from a share link", "ws proxy profile add --from-url 'vless://" + fxToken200 + "'"},
				{"Import", "ws proxy profile import ./" + fxName64 + ".json"}}}},
		{"empty/emoji-presentation", "§6.2 emoji-presentation sequences (base + U+FE0F)", Empty{
			Subject: "cached layers for " + fxEmojiRun,
			Steps:   []Remedy{{"⚠️ Rebuild", "ws profile rebuild go"}}}},
	}
	for _, ef := range empties {
		e := ef.e
		out = append(out, fixture{
			name: ef.name, kind: "empty", spec: ef.spec, fidelity: fxEmptySources(e),
			render: func(s *Stream) string { return e.Render(s) },
		})
	}

	kvs := []struct {
		name, spec string
		k          KV
	}{
		{"kv/proxy-status", "§6.1 KV", KV{Title: "Proxy", Pairs: []Fact{
			{"state", "running"}, {"profile", "de-fra-01"},
			{"endpoint", "de-fra-01.example-vpn.net:443"},
			{"uptime", "4h 12m"}, {"routes", "3 of 3 healthy"}}}},
		{"kv/key64", "§6.2 64-character key, IPv6 value, multi-line value", KV{
			Title: "Workspace " + fxName64, Pairs: []Fact{
				{fxName64, "present"}, {"endpoint", fxIPv6}, {"note", fxMultilineErr}}}},
		{"kv/unbreakable-200", "§6.2 200-character unbreakable token", KV{
			Title: "Image", Pairs: []Fact{{"digest", fxToken200}, {fxToken200, "value"}}}},
		{"kv/emoji-presentation", "§6.2 emoji-presentation sequences (base + U+FE0F)", KV{
			Title: "⚠️ Build report", Pairs: []Fact{
				{"buildkit", fxEmojiCause}, {"markers", fxEmojiRun}, {fxEmojiRun, "key side"}}}},
		{"kv/cjk", "§6.2 CJK-only text", KV{Title: "代理状态", Pairs: []Fact{
			{"状态", "运行中"}, {"配置文件", "de-fra-01"}, {"端点", fxIPv6},
			{"运行时间", "四小时十二分钟，自上次路由修复以来"}}}},
	}
	for _, kf := range kvs {
		k := kf.k
		out = append(out, fixture{
			name: kf.name, kind: "kv", spec: kf.spec, fidelity: fxKVSources(k),
			render: func(s *Stream) string { return k.Render(s) },
		})
	}

	// Checks never abbreviates a badge: it pads to the vocabulary's widest
	// mark and word (§4.4), so every state it renders must appear in full.
	checksAll := Checks{Title: "Proxy doctor", Items: []Check{
		{"xray binary present", StateOK, ""},
		{"xray config exists", StateOK, ""},
		{"route table current", StateAdvisory, "2 of 3 routes refreshed 41 minutes ago"},
		{"upstream reachable", StateFail, "dial tcp " + fxIPv6 + ": connect: connection refused"},
		{"tun device", StateBusy, ""},
		{"systemd unit", StateIdle, ""},
		{"dns leak probe", StateUnknown, "not evaluated: requires a running proxy"},
	}}
	allSix := []fxState{{StateOK, ""}, {StateAdvisory, ""}, {StateFail, ""},
		{StateBusy, ""}, {StateIdle, ""}, {StateUnknown, ""}}

	checks := []struct {
		name, spec string
		c          Checks
		states     []fxState
	}{
		{"checks/proxy-doctor", "§6.1 checks + §6.5 all six states", checksAll, allSix},
		{"checks/name64", "§6.2 64-character name, multi-line note", Checks{
			Title: "Vault doctor", Items: []Check{
				{fxName64, StateFail, fxMultilineErr}, {"short", StateOK, ""}}},
			[]fxState{{StateFail, ""}, {StateOK, ""}}},
		{"checks/unbreakable-200", "§6.2 200-character unbreakable token", Checks{
			Title: "Image checks", Items: []Check{{fxToken200, StateFail, fxToken200}}},
			[]fxState{{StateFail, ""}}},
		{"checks/emoji-presentation", "§6.2 emoji-presentation sequences (base + U+FE0F)", Checks{
			Title: "⚠️ Build doctor", Items: []Check{
				{"buildkit cache", StateAdvisory, fxEmojiCause},
				{fxEmojiRun, StateFail, fxEmojiRun}}},
			[]fxState{{StateAdvisory, ""}, {StateFail, ""}}},
		{"checks/cjk", "§6.2 CJK-only text", Checks{Title: "代理体检", Items: []Check{
			{"二进制文件存在", StateOK, ""},
			{"路由表是最新的", StateAdvisory, fxCJKNote},
			{"上游可达性", StateFail, "拨号失败：连接被拒绝"}}},
			[]fxState{{StateOK, ""}, {StateAdvisory, ""}, {StateFail, ""}}},
	}
	for _, cf := range checks {
		c := cf.c
		out = append(out, fixture{
			name: cf.name, kind: "checks", spec: cf.spec, states: cf.states,
			fidelity: fxChecksSources(c),
			render:   func(s *Stream) string { return c.Render(s) },
		})
	}

	// The five message helpers. renderMessage is the body of Info, Success,
	// Warn, Detail and Die with the write removed, which is what lets the
	// sweep cover them at every width (§6.1).
	messages := []struct {
		name, spec string
		shape      messageShape
		msg        string
		fid        []string // nil: the message text itself
	}{
		{"message/info-long", "§6.1 message helper (Info)", shapeInfo,
			"Reconciling 11 devcontainer profiles against " + fxBaseImage +
				" — this rebuilds any profile whose lockfile drifted since the last run", nil},
		{"message/success-cjk", "§6.1 message helper (Success) + §6.2 CJK", shapeSuccess,
			"工作区已启动：" + fxCJKNote, nil},
		{"message/warn-multiline", "§6.1 message helper (Warn) + §6.2 multi-line", shapeWarn,
			"Route fix reported a problem\n" + fxMultilineErr, nil},
		{"message/detail-unbreakable", "§6.1 message helper (Detail) + §6.2 unbreakable", shapeDetail,
			"digest " + fxToken200, nil},
		{"message/die-name64", "§6.1 message helper (Die) + §6.2 64-character name", shapeFail,
			"workspace \"" + fxName64 + "\" could not be created: " + fxMultilineErr, nil},
		{"message/warn-emoji-presentation", "§6.1 message helper (Warn) + §6.2 emoji presentation", shapeWarn,
			fxEmojiCause + " " + fxEmojiRun, nil},
		{"message/info-esc", "§6.7 message text containing control sequences", shapeInfo,
			fxEscCause, fxEscSurvivors},
	}
	// The D-13 surfaces OUTSIDE the table. table/esc-in-cell covers
	// Cell.display, Col.title() and captionText; problem/esc-cause covers
	// Problem.Cause. These four cover the remaining ten — Problem.Title,
	// Fact.K, Fact.V (renderPairs, shared by Problem.Facts and KV.Pairs),
	// Remedy.Label, Remedy.Cmd (renderRemedies, shared by Problem.Steps and
	// Empty.Steps), Empty.Subject, KV.Title, Checks.Title, Check.Name and
	// Check.Note.
	//
	// Each declares its SURVIVORS as its fidelity list rather than its raw
	// source: the source is deliberately altered in flight, so claiming it
	// whole would assert the opposite of what §6.7 requires.
	escProblem := Problem{
		Title: fxEsc("Could not reconcile the go profile"),
		Facts: []Fact{{fxEsc("stage"), fxEsc("buildkit export")}},
		Steps: []Remedy{{fxEsc("Retry"), fxEsc("ws profile rebuild go")}},
	}
	out = append(out, fixture{
		name: "problem/esc-surfaces", kind: "problem",
		spec:     "§6.7 / D-13: escapes in Title, a Fact key and value, and a Remedy label and command",
		fidelity: []string{"Could not reconcile the go profile", "stage", "buildkit export", "Retry", "ws profile rebuild go"},
		render:   func(s *Stream) string { return escProblem.Render(s) },
	})

	escEmpty := Empty{
		Subject: fxEsc("cached layers"),
		Steps:   []Remedy{{fxEsc("Rebuild"), fxEsc("ws profile rebuild go")}},
	}
	out = append(out, fixture{
		name: "empty/esc-surfaces", kind: "empty",
		spec:     "§6.7 / D-13: escapes in Subject and in a Remedy",
		fidelity: []string{"cached layers", "Rebuild", "ws profile rebuild go"},
		render:   func(s *Stream) string { return escEmpty.Render(s) },
	})

	escKV := KV{
		Title: fxEsc("Build report"),
		Pairs: []Fact{{fxEsc("buildkit"), fxEsc("3 of 5 layers cached")}},
	}
	out = append(out, fixture{
		name: "kv/esc-surfaces", kind: "kv",
		spec:     "§6.7 / D-13: escapes in Title and in a pair",
		fidelity: []string{"Build report", "buildkit", "3 of 5 layers cached"},
		render:   func(s *Stream) string { return escKV.Render(s) },
	})

	escChecks := Checks{
		Title: fxEsc("Build doctor"),
		Items: []Check{{fxEsc("buildkit cache"), StateAdvisory, fxEsc("2 of 5 layers reused")}},
	}
	out = append(out, fixture{
		name: "checks/esc-surfaces", kind: "checks",
		spec:     "§6.7 / D-13: escapes in Title, an item Name and an item Note",
		states:   []fxState{{StateAdvisory, ""}},
		fidelity: []string{"Build doctor", "buildkit cache", "2 of 5 layers reused"},
		render:   func(s *Stream) string { return escChecks.Render(s) },
	})

	for _, mf := range messages {
		shape, msg, fid := mf.shape, mf.msg, mf.fid
		if fid == nil {
			fid = []string{msg}
		}
		out = append(out, fixture{
			name: mf.name, kind: "message", spec: mf.spec, fidelity: fid,
			prefixState: shape.state, hasPrefixState: shape.hasState,
			render: func(s *Stream) string { return renderMessage(s, shape, msg) },
		})
	}

	return out
}

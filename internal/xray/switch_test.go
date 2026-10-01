package xray

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/output"
)

// TestValidationGate asserts ValidateProfile produces an error wrapping the
// profile name when the underlying ProxyExec call fails (e.g. dev-proxy is
// absent). Live docker integration belongs to Plan 22-06.
func TestValidationGate(t *testing.T) {
	cfg := config.Config{
		ProxyContainer:  "this-container-does-not-exist-xx22",
		XrayProfilesDir: t.TempDir(),
	}
	err := ValidateProfile(cfg, "primary")
	if err == nil {
		t.Skip("docker exec to non-existent container did not error (CI without docker?)")
	}
	if !strings.Contains(err.Error(), "xray -test failed for profile \"primary\"") {
		t.Errorf("error does not wrap profile name: %v", err)
	}
}

// TestManualRecoveryOnFailedSwitch is the TRIPWIRE test per D-10 and memory
// `feedback_no_auto_state_mutation`. It forces step-3 (Restart) to fail and
// asserts the post-failure symlink STILL points at the new (failed) target —
// i.e. NO auto-rollback occurred. If a future contributor "improves" the
// failure path by adding `os.Symlink(previous, cfg.XrayConfig)` or any other
// state-mutating recovery, THIS TEST fails in CI. They MUST revisit the
// discuss-phase decision (Q10 revised; CONTEXT.md D-10) before changing it.
func TestManualRecoveryOnFailedSwitch(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed two profile files.
	if err := os.WriteFile(filepath.Join(profilesDir, "primary.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "broken.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed broken: %v", err)
	}
	// Initial symlink: config.json -> profiles/primary.json.
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfgPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	cfg := config.Config{
		XrayConfig:      cfgPath,
		XrayProfilesDir: profilesDir,
		ProxyContainer:  "dev-proxy-test-xx22",
	}

	// Override test seams: validate passes, bind-mount check passes, wait
	// passes — but restart fails. This isolates the post-swap failure path.
	origValidate := validateProfileFn
	origRestart := restartProxyFn
	origWait := waitForHealthFn
	origBindCheck := bindMountIsWholeDirFn
	defer func() {
		validateProfileFn = origValidate
		restartProxyFn = origRestart
		waitForHealthFn = origWait
		bindMountIsWholeDirFn = origBindCheck
	}()
	validateProfileFn = func(_ config.Config, _ string) error { return nil }
	restartProxyFn = func(_ config.Config) error { return fmt.Errorf("simulated docker restart failure") }
	waitForHealthFn = func(_ config.Config, _ time.Duration) error { return nil }
	bindMountIsWholeDirFn = func(_ config.Config) (bool, error) { return true, nil }

	err := SwitchTo(cfg, "broken")
	if err == nil {
		t.Fatal("expected SwitchTo to return error after simulated restart failure")
	}
	if !strings.Contains(err.Error(), "primary") {
		t.Errorf("expected error to mention previous profile 'primary'; got: %v", err)
	}

	// CRITICAL TRIPWIRE ASSERTION: symlink must STILL point at the new
	// (broken) target. If a future contributor adds auto-rollback to
	// switch.go, this assertion fails with the message below — which
	// directs them at the discuss-phase decision they would need to
	// revisit before making the change.
	got, readErr := os.Readlink(cfgPath)
	if readErr != nil {
		t.Fatalf("readlink after failure: %v", readErr)
	}
	wantSuffix := filepath.Join("profiles", "broken.json")
	if got != wantSuffix {
		t.Fatalf("AUTO-ROLLBACK DETECTED: symlink points at %q, want %q (D-10 + feedback_no_auto_state_mutation tripwire). If you intentionally added auto-rollback, you must revisit the discuss-phase decision (CONTEXT.md D-10) first.", got, wantSuffix)
	}
}

// newSwitchFixture seeds a profiles dir holding primary.json and secondary.json,
// points config.json at primary, and stubs every SwitchTo seam to succeed. A
// test overrides the seam it drives; t.Cleanup restores all four.
func newSwitchFixture(t *testing.T) (config.Config, string) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	profilesDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"primary", "secondary"} {
		if err := os.WriteFile(filepath.Join(profilesDir, name+".json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), cfgPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	origValidate, origRestart, origWait, origBindCheck := validateProfileFn, restartProxyFn, waitForHealthFn, bindMountIsWholeDirFn
	t.Cleanup(func() {
		validateProfileFn, restartProxyFn, waitForHealthFn, bindMountIsWholeDirFn = origValidate, origRestart, origWait, origBindCheck
	})
	validateProfileFn = func(_ config.Config, _ string) error { return nil }
	restartProxyFn = func(_ config.Config) error { return nil }
	waitForHealthFn = func(_ config.Config, _ time.Duration) error { return nil }
	bindMountIsWholeDirFn = func(_ config.Config) (bool, error) { return true, nil }

	return config.Config{
		XrayConfig:      cfgPath,
		XrayProfilesDir: profilesDir,
		ProxyContainer:  "dev-proxy-test-xx22",
	}, cfgPath
}

// TestSwitchToWaitsOneFullProbeCycle pins the liveness budget to the proxy
// image's HEALTHCHECK schedule. The restart is a stop and a start, and every
// start resets Docker's health status to "starting". On Engine 27.0 or later
// the first probe runs about 5s after the start and, if it fails (a curl
// timeout included), the next one runs 30s after it ends; older Engines run the
// first probe only after the 30s interval. A budget shorter than that reports a
// switch whose container passes that second probe as timed out.
func TestSwitchToWaitsOneFullProbeCycle(t *testing.T) {
	// The proxy image declares HEALTHCHECK --interval=30s --timeout=10s
	// --start-period=5s --retries=3 and sets no --start-interval.
	const (
		startInterval = 5 * time.Second  // Docker's default --start-interval (Engine 27.0 or later)
		interval      = 30 * time.Second // --interval, counted from the end of the previous probe
		probe         = 11 * time.Second // upper bound: --timeout plus up to ~1s to start the exec (curl --max-time 5 usually ends it within ~6s)
		poll          = 1 * time.Second  // how often WaitForHealth inspects the container
	)
	// 58s. On older Engines the first probe alone needs interval+probe+poll = 42s.
	floor := startInterval + probe + interval + probe + poll

	cfg, _ := newSwitchFixture(t)
	var budget time.Duration
	waitForHealthFn = func(_ config.Config, d time.Duration) error {
		budget = d
		return nil
	}

	if err := SwitchTo(cfg, "secondary"); err != nil {
		t.Fatalf("SwitchTo: %v", err)
	}
	if budget < floor {
		t.Fatalf("SwitchTo waits %s for liveness; a healthy container can need %s (a failed first probe, then a passing second one)", budget, floor)
	}
}

// TestNoRollbackWhenLivenessTimesOut extends the no-auto-rollback tripwire
// above to the liveness step: a wait that gives up must leave the symlink at
// the new profile and name the previous one in the error.
func TestNoRollbackWhenLivenessTimesOut(t *testing.T) {
	cfg, cfgPath := newSwitchFixture(t)
	waitForHealthFn = func(_ config.Config, d time.Duration) error {
		return fmt.Errorf("proxy health check timed out after %s", d)
	}

	err := SwitchTo(cfg, "secondary")
	if err == nil {
		t.Fatal("expected SwitchTo to return the liveness failure")
	}
	if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "primary") {
		t.Errorf("expected the timeout and the previous profile 'primary' in the error; got: %v", err)
	}
	got, readErr := os.Readlink(cfgPath)
	if readErr != nil {
		t.Fatalf("readlink after failure: %v", readErr)
	}
	if want := filepath.Join("profiles", "secondary.json"); got != want {
		t.Fatalf("AUTO-ROLLBACK DETECTED: symlink points at %q after a liveness failure, want %q", got, want)
	}
}

// TestSwitchToReturnsPreSwapErrorOnLegacyBind guards the 2026-05-13 hotfix:
// when BindMountIsWholeDir reports false (legacy single-file bind), SwitchTo
// must return a non-nil error whose message mentions "legacy single-file bind
// mount" so the cmd layer can surface it to the operator. Previously the
// error was correctly returned by SwitchTo, but profileUseCmd swallowed it via
// os.Exit(1) with no output. This test pins the SwitchTo-layer contract;
// TestSwitchToLeavesTheErrorToTheRoot pins that it is a plain error, which
// the root prints as its ✗ line; cmd/proxy_profile_test.go pins that it
// propagates.
func TestSwitchToReturnsPreSwapErrorOnLegacyBind(t *testing.T) {
	origBindCheck := bindMountIsWholeDirFn
	defer func() { bindMountIsWholeDirFn = origBindCheck }()
	bindMountIsWholeDirFn = func(_ config.Config) (bool, error) { return false, nil }

	cfg := config.Config{
		ProxyContainer:  "dev-proxy-test-xx22-cibind",
		XrayConfig:      filepath.Join(t.TempDir(), "config.json"),
		XrayProfilesDir: t.TempDir(),
	}
	err := SwitchTo(cfg, "foo")
	if err == nil {
		t.Fatal("expected SwitchTo to return non-nil when bind is legacy single-file")
	}
	if !strings.Contains(err.Error(), "legacy single-file bind mount") {
		t.Fatalf("expected error to mention `legacy single-file bind mount`; got: %v", err)
	}
}

// captureStderr returns what fn writes to the process's stderr. The render
// layer resolves os.Stderr at write time, so pointing the variable at a pipe
// is enough.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stderr = saved }()
	fn()
	_ = w.Close()
	return <-done
}

// TestSwitchToLeavesTheErrorToTheRoot: a switch that fails after its
// pre-flight writes its step lines and nothing else — no box, no Problem —
// and returns the error that carries its Problem, whose facts and steps
// name the previous profile, the active one, and cfg.ProxyContainer. A
// pre-swap failure writes nothing and is a plain error. What the root does
// with either is cmd's TestRootProblemSelects.
func TestSwitchToLeavesTheErrorToTheRoot(t *testing.T) {
	cfg, _ := newSwitchFixture(t)
	restartProxyFn = func(_ config.Config) error { return fmt.Errorf("simulated docker restart failure") }

	var err error
	stderr := captureStderr(t, func() { err = SwitchTo(cfg, "secondary") })
	s := output.Err()
	ok, fail, idle, busy := s.StateMark(output.StateOK), s.StateMark(output.StateFail),
		s.StateMark(output.StateIdle), s.StateMark(output.StateBusy)
	want := busy + " Validate target profile (xray -test)\n" + ok + " Validate target profile (xray -test)  <t>\n" +
		busy + " Atomic symlink swap\n" + ok + " Atomic symlink swap  <t>\n" +
		busy + " Restart dev-proxy\n" + fail + " Restart dev-proxy  <t>\n" +
		idle + " Wait for liveness (<=1m0s)\n"
	if got := regexp.MustCompile(`(?m)  \d+\.\ds$`).ReplaceAllString(stderr, "  <t>"); got != want {
		t.Errorf("SwitchTo wrote\n%s\nwant exactly its step lines\n%s", got, want)
	}
	p, carried := output.ProblemOf(err)
	wantP := output.Problem{
		Title: `Switch to "secondary" failed`,
		Cause: "simulated docker restart failure",
		Facts: []output.Fact{{K: "Previous", V: "primary"}, {K: "Active", V: "secondary (not rolled back)"}},
		Steps: []output.Remedy{
			{Label: "Restore previous", Cmd: "ws proxy profile use primary"},
			{Label: "Inspect logs", Cmd: "docker logs dev-proxy-test-xx22 --tail 50"},
		},
	}
	if !carried || !reflect.DeepEqual(p, wantP) {
		t.Errorf("the root would print %+v; want %+v", p, wantP)
	}
	if want := `switch to "secondary" failed (previous="primary"): simulated docker restart failure`; err == nil || err.Error() != want {
		t.Errorf("the error reads %v; want %q", err, want)
	}

	restartProxyFn = func(_ config.Config) error { return nil }
	stderr = captureStderr(t, func() { err = SwitchTo(cfg, "primary") })
	if strings.Count(stderr, ok+" ") != 4 || strings.Contains(stderr, "Switched") || err != nil {
		t.Errorf("a switch that succeeds wrote\n%s\nand returned %v; want its four result lines and nothing else", stderr, err)
	}

	stderr = captureStderr(t, func() { err = SwitchTo(cfg, "bad/name") })
	if _, carried := output.ProblemOf(err); err == nil || carried || stderr != "" {
		t.Errorf("a pre-swap failure wrote %q and returned %v (a Problem: %t); want nothing written and a plain error", stderr, err, carried)
	}
}

// TestSwitchProblemFitsEveryWidth: the Problem of a failed switch, before
// and after the swap, at every width from MinWidth to 200 in both glyph
// modes, and not one line wider than the width, measured with the layer's W.
// The profiles' names are 64 characters and the cause carries a 20-line tail
// with a 200-character token, CJK and escapes. The control counts the lines
// exactly the width: the token is hard-broken into such lines at every width,
// or the sweep could not see an overflow of one cell.
func TestSwitchProblemFitsEveryWidth(t *testing.T) {
	sweepSwitchProblem(t, []bool{false, true})
}

func sweepSwitchProblem(t *testing.T, modes []bool) {
	t.Helper()
	name, previous := strings.Repeat("backup-", 9)+"x", strings.Repeat("primary", 9)+"x"
	tail := []string{strings.Repeat("9f86d081884c7d65", 13)[:200], "错误：无法连接到代理服务器，正在重试",
		"\x1b[31mred\x1b[0m \x1b]0;title\x07 done\r"}
	for i := len(tail); i < 20; i++ {
		tail = append(tail, fmt.Sprintf("[12:01:%02d] info restart dev-proxy: step %d of 20", i, i))
	}
	cfg := config.Config{ProxyContainer: "dev-proxy-" + name}
	err := &output.TaskError{Title: "Restart dev-proxy", Err: fmt.Errorf("restart %s: exit status 1", cfg.ProxyContainer), Tail: tail}
	problems := []output.Problem{switchProblem(cfg, name, previous, true, err), switchProblem(cfg, name, "", false, err)}
	renders, lines, over := 0, 0, 0
	for w := output.MinWidth; w <= 200; w++ {
		control := 0
		for _, ascii := range modes {
			s := output.NewStreamAt(io.Discard, w, true, output.ColourTrue, ascii)
			for _, p := range problems {
				renders++
				for _, line := range strings.Split(p.Render(s), "\n") {
					lines++
					switch n := output.W(line); {
					case n > w:
						if over++; over <= 10 {
							t.Errorf("at %d columns a line is %d cells wide: %q", w, n, line)
						}
					case n == w:
						control++
					}
				}
			}
		}
		if control == 0 {
			t.Errorf("control at %d columns: no line is wider than %d, so the sweep cannot see an overflow of one cell", w, w-1)
		}
		if w == output.MinWidth {
			t.Logf("control at %d columns: %d lines are wider than %d", w, control, w-1)
		}
	}
	if over > 0 {
		t.Errorf("%d lines over the width in all", over)
	}
	t.Logf("swept %d renders, %d lines", renders, lines)
}

const switchAmbiWideEnv = "WS_TEST_SWITCH_AMBIWIDE"

// TestSwitchProblemFitsEveryWidthAmbiguousWide runs the sweep under
// RUNEWIDTH_EASTASIAN=1 in a child, since x/ansi reads the variable in
// init(), in the glyph mode the layer selects there: ASCII, because the
// UTF-8 marks and borders are Ambiguous. The child checks both.
func TestSwitchProblemFitsEveryWidthAmbiguousWide(t *testing.T) {
	if os.Getenv(switchAmbiWideEnv) == "1" {
		if n := output.W("…"); n != 2 {
			t.Fatalf("U+2026 measures %d cells under RUNEWIDTH_EASTASIAN=1, want 2: the convention did not reach x/ansi", n)
		}
		if mode := output.Err().Mode(); mode != output.GlyphASCII {
			t.Fatalf("the layer selected glyph mode %v on an Ambiguous-wide terminal; want ASCII", mode)
		}
		sweepSwitchProblem(t, []bool{true})
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSwitchProblemFitsEveryWidthAmbiguousWide$", "-test.v")
	child.Env = append(os.Environ(), "RUNEWIDTH_EASTASIAN=1", switchAmbiWideEnv+"=1")
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("the Ambiguous-wide sweep failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "swept ") {
		t.Fatalf("the child ran no sweep:\n%s", out)
	}
}

// TestSwitchToBeforeTheSwapNamesNoActiveProfile: a switch that fails before
// the swap has not changed the active profile, and its Problem does not say
// it has.
func TestSwitchToBeforeTheSwapNamesNoActiveProfile(t *testing.T) {
	cfg, _ := newSwitchFixture(t)
	validateProfileFn = func(_ config.Config, _ string) error { return fmt.Errorf("xray -test failed") }
	var err error
	_ = captureStderr(t, func() { err = SwitchTo(cfg, "secondary") })
	p, _ := output.ProblemOf(err)
	if want := []output.Fact{{K: "Previous", V: "primary"}}; !reflect.DeepEqual(p.Facts, want) {
		t.Errorf("facts %+v; want %+v", p.Facts, want)
	}
}

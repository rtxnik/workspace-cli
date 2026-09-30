package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The stream matrix: which stream each command writes what to, in a pipe.
//
// Every row runs the real Execute in a re-exec of this test binary — the
// child half is TestExecuteChildProcess — over a fixture home of its own:
// five workspaces and a fake devpod on PATH that answers `list` and `status`,
// and prints a line to each stream for `up`, `stop` and `delete`, failing
// after a third when FAKE_DEVPOD=fail. Both streams are compared whole, once
// the step times are normalised, and no ESC byte may appear on either.

// fixtureWorkspaces are the fixture's workspaces: name, profile, and whether
// it is on the proxy network.
var fixtureWorkspaces = []struct {
	name, profile string
	proxy         bool
}{
	{"api", "go", true},
	{"web-frontend", "web", false},
	{"ml-training", "python", true},
	{"ops", "devops", false},
	{"legacy-billing", "default", false},
}

// fakeDevpod is the devpod the fixture puts on PATH. devpod knows four of the
// five workspaces; legacy-billing has never been created.
const fakeDevpod = `#!/bin/sh
case "$1" in
list)
	echo '[{"id":"api"},{"id":"web-frontend"},{"id":"ml-training"},{"id":"ops"}]'
	;;
status)
	case "$2" in
	api | web-frontend) echo "info Workspace '$2' is 'Running'" ;;
	ml-training) echo "info Workspace '$2' is 'Busy'" ;;
	ops) echo "info Workspace '$2' is 'Stopped'" ;;
	esac
	;;
up | stop | delete)
	echo "[12:01:30] info $1 $2: step one"
	echo "[12:01:31] info $1 $2: step two" >&2
	if [ "$FAKE_DEVPOD" = fail ]; then
		echo "[12:01:32] fatal $1 $2: denied: requested access to the resource is denied" >&2
		exit 1
	fi
	echo "[12:01:32] done $1 $2"
	;;
esac
`

// streamsFixture is one row's home and PATH.
type streamsFixture struct {
	home string
	bin  string
}

func newStreamsFixture(t *testing.T) streamsFixture {
	t.Helper()
	root := t.TempDir()
	fx := streamsFixture{home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin")}
	for _, ws := range fixtureWorkspaces {
		dc := filepath.Join(fx.home, "workspaces", ws.name, ".devcontainer")
		if err := os.MkdirAll(dc, 0o755); err != nil {
			t.Fatal(err)
		}
		runArgs := "[]"
		if ws.proxy {
			runArgs = `["--network=ws-proxy"]`
		}
		body := `{"containerEnv": {"WORKSPACE_PROFILE": "` + ws.profile + `"}, "runArgs": ` + runArgs + "}\n"
		if err := os.WriteFile(filepath.Join(dc, "devcontainer.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(fx.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.bin, "devpod"), []byte(fakeDevpod), 0o755); err != nil {
		t.Fatal(err)
	}
	return fx
}

// streamsRow is one invocation and everything it must write.
type streamsRow struct {
	name   string
	args   []string
	env    []string // added to the child's environment
	stub   string   // a stub set of installExecuteStub
	code   int
	stdout string
	stderr string                                // with every step time normalised to <t>
	after  func(t *testing.T, fx streamsFixture) // what the row must have left behind, when it matters
}

// runStreamsChild runs row in a child over fx.
func runStreamsChild(t *testing.T, fx streamsFixture, row streamsRow) (code int, stdout, stderr string) {
	t.Helper()
	argv, err := json.Marshal(row.args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecuteChildProcess$")
	cmd.WaitDelay = 5 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// No NO_COLOR: a pipe alone must keep every escape out.
	cmd.Env = append([]string{
		executeChildEnv + "=1",
		executeArgsEnv + "=" + string(argv),
		executeStubEnv + "=" + row.stub,
		"PATH=" + fx.bin,
		"HOME=" + fx.home,
		"WORKSPACES_DIR=" + filepath.Join(fx.home, "workspaces"),
		"DOCKER_HOST=unix://" + childHome + "/docker.sock",
		"COLUMNS=80",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"TMPDIR=" + os.TempDir(),
		"GORACE=atexit_sleep_ms=0",
	}, row.env...)
	if v, ok := os.LookupEnv("GOCOVERDIR"); ok {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+v)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		t.Fatalf("%s did not finish within 30s; stderr so far:\n%s", row.name, errBuf.String())
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running %s: %v\n%s", row.name, err, errBuf.String())
	}
	return code, normaliseStepTimes(out.String()), normaliseStepTimes(errBuf.String())
}

// stepTime is the time at the end of a step's result line.
var stepTime = regexp.MustCompile(`(?m)  (\d+\.\ds|\d+m\d\ds|\d+h\d\dm)$`)

// normaliseStepTimes replaces every step time with <t>: how long a step took
// is the one thing in its line that moves from run to run.
func normaliseStepTimes(s string) string { return stepTime.ReplaceAllString(s, "  <t>") }

func TestNormaliseStepTimes(t *testing.T) {
	in := "✓ Stopping workspace \"api\"  0.4s\n✗ Starting container  2m13s\n✓ Building  1h04m\n" +
		"x a  12.3s\n  12.3s later, not at the end\n"
	want := "✓ Stopping workspace \"api\"  <t>\n✗ Starting container  <t>\n✓ Building  <t>\n" +
		"x a  <t>\n  12.3s later, not at the end\n"
	if got := normaliseStepTimes(in); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// streamsRows is the matrix.
var streamsRows = []streamsRow{
	{name: "ws start: succeeds", args: []string{"start", "api"},
		stderr: "~ Checking workspace\n✓ Checking workspace  <t>\n~ Starting container\n✓ Starting container  <t>\n"},
	{name: "ws restart: succeeds, nothing from the child", args: []string{"restart", "api"},
		stderr: "~ Stopping workspace\n✓ Stopping workspace  <t>\n~ Starting container\n✓ Starting container  <t>\n"},
	{name: "ws stop: the child fails", args: []string{"stop", "api"}, env: []string{"FAKE_DEVPOD=fail"}, code: 1,
		stderr: "~ Stopping workspace \"api\"\n" +
			"✗ Stopping workspace \"api\"  <t>\n" +
			"✗ devpod stop: exit status 1\n" +
			"  [12:01:30] info stop api: step one\n" +
			"  [12:01:31] info stop api: step two\n" +
			"  [12:01:32] fatal stop api: denied: requested access to the resource is denied\n"},
	{name: "ws delete --force: missing", args: []string{"delete", "--force", "nosuch"}, code: 1, stderr: deleteNotFound},
	{name: "ws delete: missing, no confirmation", args: []string{"delete", "nosuch"}, code: 1, stderr: deleteNotFound},
	{name: "ws delete: devpod delete fails", args: []string{"delete", "--force", "ops"}, env: []string{"FAKE_DEVPOD=fail"},
		stderr: "~ Deleting workspace \"ops\"\n" +
			"✓ Deleting workspace \"ops\"  <t>\n" +
			"⚠ devpod delete: exit status 1\n" +
			"  [12:01:30] info delete ops: step one\n" +
			"  [12:01:31] info delete ops: step two\n" +
			"  [12:01:32] fatal delete ops: denied: requested access to the resource is\n" +
			"  denied\n",
		after: func(t *testing.T, fx streamsFixture) {
			if _, err := os.Stat(filepath.Join(fx.home, "workspaces", "ops")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the workspace directory is still there: %v", err)
			}
		}},
}

// deleteNotFound is what ws delete prints for a workspace that is not there.
const deleteNotFound = "✗ Workspace \"nosuch\" not found\n" +
	"\n" +
	"  1. List workspaces        ws list\n" +
	"  2. Remove it from devpod  devpod delete nosuch\n"

func TestStreams(t *testing.T) {
	for _, row := range streamsRows {
		t.Run(row.name, func(t *testing.T) {
			fx := newStreamsFixture(t)
			code, stdout, stderr := runStreamsChild(t, fx, row)
			if row.after != nil {
				row.after(t, fx)
			}
			if code != row.code || stdout != row.stdout || stderr != row.stderr {
				t.Errorf("exit %d\n--- stdout:\n%s--- stderr:\n%s\nwant exit %d\n--- stdout:\n%s--- stderr:\n%s",
					code, stdout, stderr, row.code, row.stdout, row.stderr)
			}
			if strings.ContainsRune(stdout+stderr, '\x1b') {
				t.Errorf("an ESC byte in a pipe: stdout %q, stderr %q", stdout, stderr)
			}
		})
	}
}

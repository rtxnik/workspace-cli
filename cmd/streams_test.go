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

	"github.com/rtxnik/workspace-cli/internal/output"
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

// fixtureProfiles are the fixture's workspace profiles: name, base image and
// tools, some of them longer than any terminal is wide.
var fixtureProfiles = []struct{ name, image, tools string }{
	{"default", "mcr.microsoft.com/devcontainers/base:ubuntu-24.04", ""},
	{"devops", "mcr.microsoft.com/devcontainers/base:ubuntu-24.04",
		"kubectl, helm, terraform, awscli, gcloud, azure-cli, ansible, packer, vault, jq, yq, k9s, stern"},
	{"go", "mcr.microsoft.com/devcontainers/go:1.26", "go, golangci-lint, gopls, delve"},
	{"java", "mcr.microsoft.com/devcontainers/java:21", "java, maven, gradle"},
	{"ml", "nvidia/cuda:12.4.1-cudnn-devel-ubuntu22.04", "python, uv, jupyter, ruff"},
	{"python", "mcr.microsoft.com/devcontainers/python:3.13", "python, uv, ruff, mypy, pre-commit"},
	{"rust", "mcr.microsoft.com/devcontainers/rust:1", "rust, cargo-nextest, cargo-deny"},
	{"terraform", "hashicorp/terraform:1.9", "terraform, tflint, terragrunt"},
	{"web", "mcr.microsoft.com/devcontainers/typescript-node:22", "node, pnpm, bun, deno"},
	{"zig", "debian:bookworm-slim", "zig, zls"},
}

// fixtureXrayProfiles are the fixture's proxy profiles; primary is active,
// and backup-nl's address is IPv6.
var fixtureXrayProfiles = []struct{ name, addr, port, network, sni, uuid string }{
	{"primary", "de-fra-01.example-vpn.net", "443", "tcp", "www.microsoft.com", "1f2e3d4c-5b6a-4798-8a9b-0c1d2e3f4a5b"},
	{"backup-nl", "2001:db8:85a3::8a2e:370:7334", "8443", "ws", "cdn.jsdelivr.net", "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"},
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
	for _, p := range fixtureProfiles {
		dir := filepath.Join(fx.home, ".config", "workspaces", "profiles", p.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+p.image+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mise := "[tools]\n"
		for _, tool := range strings.Split(p.tools, ", ") {
			if tool != "" {
				mise += tool + " = \"latest\"\n"
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "mise.toml"), []byte(mise), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	xrayProfiles := filepath.Join(fx.home, ".config", "xray", "profiles")
	if err := os.MkdirAll(xrayProfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range fixtureXrayProfiles {
		body := `{"outbounds":[{"tag":"proxy-1","protocol":"vless","settings":{"vnext":[{"address":"` + p.addr +
			`","port":` + p.port + `,"users":[{"id":"` + p.uuid + `","encryption":"none"}]}]},"streamSettings":{"network":"` +
			p.network + `","security":"reality","realitySettings":{"serverName":"` + p.sni + `","publicKey":"pk","shortId":"ab"}}}]}` + "\n"
		if err := os.WriteFile(filepath.Join(xrayProfiles, p.name+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("profiles", "primary.json"), filepath.Join(fx.home, ".config", "xray", "config.json")); err != nil {
		t.Fatal(err)
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
	setup  func(t *testing.T, fx streamsFixture) // what the row needs beyond the fixture
	// check replaces the byte comparison of stdout, for a row whose stdout
	// the tables baseline pins.
	check func(t *testing.T, stdout string)
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
	{name: "ws proxy up: no docker", args: []string{"proxy", "up"}, code: 1,
		stderr: "~ Starting proxy\n" +
			"✗ Starting proxy  <t>\n" +
			"- Waiting for health check\n" +
			"- Fixing workspace routes\n" +
			"✗ Failed to start proxy\n" +
			"  proxy image \"devpod-proxy\" not found, run 'ws proxy rebuild' first\n" +
			"\n" +
			"  1. Check config       ws proxy check\n" +
			"  2. Initialize config  ws proxy init <proxy-uri>\n" +
			"  3. Rebuild image      ws proxy rebuild\n"},
	{name: "ws proxy down: no docker", args: []string{"proxy", "down", "--force"}, code: 1,
		stderr: "~ Stopping proxy\n" +
			"✗ Stopping proxy  <t>\n" +
			"✗ stop proxy: Cannot connect to the Docker daemon at\n" +
			"  unix:///nonexistent/ws-error-baseline/docker.sock. Is the docker daemon\n" +
			"  running?\n"},
	{name: "ws proxy rebuild: no recipe", args: []string{"proxy", "rebuild", "--force"}, code: 1,
		stderr: "~ Building proxy image\n" +
			"✗ Building proxy image  <t>\n" +
			"- Recreating container\n" +
			"- Waiting for health check\n" +
			"- Cleaning old images\n" +
			"✗ proxy recipe drift: Dockerfile (missing), entrypoint.sh (missing). Run\n" +
			"  'chezmoi apply' to restore the canonical recipe, or rebuild intentionally with\n" +
			"  'ws proxy rebuild --allow-drift'\n"},
	{name: "ws proxy update: no recipe", args: []string{"proxy", "update", "v26.2.6", "--force"}, code: 1,
		stderr: "~ Building proxy image with xray-core v26.2.6\n" +
			"✗ Building proxy image with xray-core v26.2.6  <t>\n" +
			"✗ proxy recipe drift: Dockerfile (missing), entrypoint.sh (missing). Run\n" +
			"  'chezmoi apply' to restore the canonical recipe, or rebuild intentionally with\n" +
			"  'ws proxy rebuild --allow-drift'\n"},
	{name: "ws proxy rebuild: docker writes to the log", args: []string{"proxy", "rebuild", "--force", "--allow-drift"},
		setup: withFakeDockerBuild, code: 1,
		stderr: "~ Building proxy image\n" +
			"✓ Building proxy image  <t>\n" +
			"~ Recreating container\n" +
			"✓ Recreating container  <t>\n" +
			"~ Waiting for health check\n" +
			"✗ Waiting for health check  <t>\n" +
			"- Cleaning old images\n" +
			"✗ inspect proxy: Cannot connect to the Docker daemon at\n" +
			"  unix:///nonexistent/ws-error-baseline/docker.sock. Is the docker daemon\n" +
			"  running?\n"},
	{name: "ws list: five workspaces", args: []string{"list"},
		stdout: "╭────────────────┬───────────────┬─────────┬───────╮\n" +
			"│ NAME           │ STATUS        │ PROFILE │ PROXY │\n" +
			"├────────────────┼───────────────┼─────────┼───────┤\n" +
			"│ api            │ ✓ running     │ go      │ on    │\n" +
			"│ legacy-billing │ - not created │ default │ off   │\n" +
			"│ ml-training    │ ~ busy        │ python  │ on    │\n" +
			"│ ops            │ - stopped     │ devops  │ off   │\n" +
			"│ web-frontend   │ ✓ running     │ web     │ off   │\n" +
			"╰────────────────┴───────────────┴─────────┴───────╯\n" +
			"5 workspaces, 2 running\n"},
	{name: "ws list: none", args: []string{"list"}, setup: withoutWorkspaces,
		stdout: "No workspaces yet.\n  Create one    ws new <name>\n  See profiles  ws profiles\n"},
	{name: "ws list --json: none", args: []string{"list", "--json"}, setup: withoutWorkspaces, stdout: "[]\n"},
	// COLUMNS empty: a pipe with no width, the budget is unbounded.
	{name: "ws profiles: ten profiles, nothing cut", args: []string{"profiles"}, env: []string{"COLUMNS="},
		check: func(t *testing.T, stdout string) {
			for _, p := range fixtureProfiles {
				for _, cell := range []string{"│ " + p.name + " ", " " + p.image + " ", " " + p.tools + " "} {
					if !strings.Contains(stdout, cell) {
						t.Errorf("a pipe lost %q:\n%s", cell, stdout)
					}
				}
			}
			if strings.Contains(stdout, "…") || !strings.HasSuffix(stdout, "╯\n10 profiles\n") {
				t.Errorf("a pipe cut a cell, or the caption is not under the table:\n%s", stdout)
			}
		}},
	{name: "ws profiles: none", args: []string{"profiles"}, setup: withoutProfiles,
		stdout: "No profiles yet.\n  Create one  ws profile-create <name>\n"},
	{name: "ws profiles --json: none", args: []string{"profiles", "--json"}, setup: withoutProfiles, stdout: "[]\n"},
	{name: "ws proxy profile list: two profiles", args: []string{"proxy", "profile", "list"}, env: []string{"COLUMNS="},
		check: func(t *testing.T, stdout string) {
			for _, want := range []string{"│ ✓ yes  │ primary ", "│ - no   │ backup-nl ", " [2001:db8:85a3::8a2e:370:7334]:8443 ",
				" de-fra-01.example-vpn.net:443 ", "╯\n2 profiles, active primary\n"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
		}},
	{name: "ws proxy profile list --reveal: two profiles", args: []string{"proxy", "profile", "list", "--reveal"}, env: []string{"COLUMNS="},
		check: func(t *testing.T, stdout string) {
			want := []string{"│ ✓ yes  │ primary ", "│ - no   │ backup-nl ", " [2001:db8:85a3::8a2e:370:7334]:8443 ",
				"╯\n2 profiles, active primary\n"}
			for _, p := range fixtureXrayProfiles {
				want = append(want, " "+p.uuid+" ")
			}
			for _, w := range want {
				if !strings.Contains(stdout, w) {
					t.Errorf("stdout lacks %q:\n%s", w, stdout)
				}
			}
		}},
	{name: "ws proxy profile list: none", args: []string{"proxy", "profile", "list"}, setup: withoutXrayProfiles,
		stdout: proxyProfilesEmpty},
	{name: "ws proxy profile list --reveal: none", args: []string{"proxy", "profile", "list", "--reveal"}, setup: withoutXrayProfiles,
		stdout: proxyProfilesEmpty},
	{name: "ws proxy profile list --json: none", args: []string{"proxy", "profile", "list", "--json"}, setup: withoutXrayProfiles,
		stdout: "[]\n"},
	{name: "ws proxy profile list --reveal --json: none", args: []string{"proxy", "profile", "list", "--reveal", "--json"},
		setup: withoutXrayProfiles, stdout: "[]\n"},
}

// proxyProfilesEmpty is what ws proxy profile list answers with no profile.
const proxyProfilesEmpty = "No proxy profiles yet.\n" +
	"  Initialize  ws proxy init <proxy-uri>\n" +
	"  Add one     ws proxy profile add <name> <vless-uri>\n"

// withoutXrayProfiles removes the fixture's proxy profiles and its xray
// configuration.
func withoutXrayProfiles(t *testing.T, fx streamsFixture) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(fx.home, ".config", "xray")); err != nil {
		t.Fatal(err)
	}
}

// withoutProfiles empties the fixture's profiles directory.
func withoutProfiles(t *testing.T, fx streamsFixture) {
	t.Helper()
	dir := filepath.Join(fx.home, ".config", "workspaces", "profiles")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// withoutWorkspaces empties the fixture's workspaces directory.
func withoutWorkspaces(t *testing.T, fx streamsFixture) {
	t.Helper()
	dir := filepath.Join(fx.home, "workspaces")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// withFakeDockerBuild gives a row a proxy recipe that has drifted, for
// --allow-drift to build, and a docker on PATH whose build prints a line to
// each stream and succeeds.
func withFakeDockerBuild(t *testing.T, fx streamsFixture) {
	t.Helper()
	recipe := filepath.Join(fx.home, ".config", "workspaces", "profiles", "proxy")
	if err := os.MkdirAll(recipe, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"Dockerfile", "entrypoint.sh"} {
		if err := os.WriteFile(filepath.Join(recipe, f), []byte("drifted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake := "#!/bin/sh\necho '#1 [internal] load build definition'\necho '#2 WARN: FromAsCasing' >&2\n"
	if err := os.WriteFile(filepath.Join(fx.bin, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
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
			if row.setup != nil {
				row.setup(t, fx)
			}
			code, stdout, stderr := runStreamsChild(t, fx, row)
			if row.after != nil {
				row.after(t, fx)
			}
			if row.check != nil {
				row.check(t, stdout)
				stdout = row.stdout
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

func TestCountOf(t *testing.T) {
	for n, want := range map[int]string{0: "0 workspaces", 1: "1 workspace", 5: "5 workspaces"} {
		if got := countOf(n, "workspace", "workspaces"); got != want {
			t.Errorf("countOf(%d) = %q; want %q", n, got, want)
		}
	}
}

// TestWorkspaceState pins every word of ws list's state vocabulary,
// including the ones the fixture's devpod never reports.
func TestWorkspaceState(t *testing.T) {
	for _, c := range []struct {
		status string
		st     output.State
		word   string
	}{
		{"Running", output.StateOK, "running"},
		{"Stopped", output.StateIdle, "stopped"},
		{"NotCreated", output.StateIdle, "not created"},
		{"", output.StateIdle, "not created"},
		{"Busy", output.StateBusy, "busy"},
		{"Starting", output.StateBusy, "starting"},
		{"NotFound", output.StateIdle, "not found"},
		{"Rebuilding", output.StateUnknown, "rebuilding"},
	} {
		if st, word := workspaceState(c.status); st != c.st || word != c.word {
			t.Errorf("workspaceState(%q) = (%d, %q); want (%d, %q)", c.status, st, word, c.st, c.word)
		}
	}
}

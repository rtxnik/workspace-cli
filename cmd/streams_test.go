package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/rtxnik/workspace-cli/internal/workspace"
	"github.com/spf13/cobra"
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
	// docker, when set, is the state of a fake Docker Engine API the row's
	// child reaches through DOCKER_HOST, with the fake docker CLI on PATH for
	// route lookups. Unset, the child's DOCKER_HOST reaches nothing.
	docker *fakeDockerState
}

// reportCheck is the stdout check of a report row whose whole stdout the
// reports baseline pins: the report is there, on stdout, and holds each of
// lines.
func reportCheck(lines ...string) func(t *testing.T, stdout string) {
	return func(t *testing.T, stdout string) {
		t.Helper()
		for _, l := range lines {
			if !strings.Contains(stdout, l+"\n") {
				t.Errorf("stdout lacks the line %q:\n%s", l, stdout)
			}
		}
	}
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
	// On the timeout the child's whole session goes, the fake devpod it
	// started included, not the child alone.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
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
		// The git ws status runs reads no system or global configuration.
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}, row.env...)
	if row.docker != nil {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+startFakeDocker(t, *row.docker))
		writeFakeDockerCLI(t, fx.bin)
	}
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
	return code, normaliseUptime(normaliseStepTimes(out.String())), normaliseStepTimes(errBuf.String())
}

// stepTime is the time at the end of a step's result line.
var stepTime = regexp.MustCompile(`(?m)  (\d+\.\ds|\d+m\d\ds|\d+h\d\dm)$`)

// normaliseStepTimes replaces every step time with <t>: how long a step took
// is the one thing in its line that moves from run to run.
func normaliseStepTimes(s string) string { return stepTime.ReplaceAllString(s, "  <t>") }

// uptime is the value of ws proxy status's Uptime pair, which the fake
// daemon's fixed start time turns into a duration that grows from run to run.
var uptime = regexp.MustCompile(`(?m)^(  Uptime +)[0-9][0-9hms.]*$`)

// normaliseUptime replaces the proxy's uptime with <uptime>.
func normaliseUptime(s string) string { return uptime.ReplaceAllString(s, "${1}<uptime>") }

func TestNormaliseUptime(t *testing.T) {
	in := "Proxy\n  State    ✓ running\n  Uptime   24h31m5s\n  Image    devpod-proxy\n" +
		"  Uptime   not a duration\nUptime   1h\n"
	want := "Proxy\n  State    ✓ running\n  Uptime   <uptime>\n  Image    devpod-proxy\n" +
		"  Uptime   not a duration\nUptime   1h\n"
	if got := normaliseUptime(in); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

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
	{name: "ws start: missing", args: []string{"start", "nosuch"}, code: 1,
		stderr: "✗ Workspace \"nosuch\" not found\n\n  1. List workspaces  ws list\n  2. Create it        ws new nosuch\n"},
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
	{name: "ws proxy check: no daemon", args: []string{"proxy", "check"}, code: 1,
		stdout: "Proxy prerequisites\n" +
			"  ✗ failed    Docker running\n" +
			"  ✓ ok        Xray config exists\n" +
			"  ? unknown   Proxy image built\n" +
			"  ? unknown   Proxy container running\n" +
			"1 of 4 checks passed, 1 failed, 2 unknown\n"},
	{name: "ws proxy check: all ok", args: []string{"proxy", "check"}, docker: &fakeHealthyProxy,
		stdout: "Proxy prerequisites\n" +
			"  ✓ ok        Docker running\n" +
			"  ✓ ok        Xray config exists\n" +
			"  ✓ ok        Proxy image built\n" +
			"  ✓ ok        Proxy container running\n" +
			"4 of 4 checks passed\n"},
	{name: "ws proxy status: no daemon", args: []string{"proxy", "status"}, code: 1,
		stderr: "✗ inspect proxy: Cannot connect to the Docker daemon at\n" +
			"  unix:///nonexistent/ws-error-baseline/docker.sock. Is the docker daemon\n" +
			"  running?\n"},
	{name: "ws proxy status: stopped and no network", args: []string{"proxy", "status"}, docker: &fakeDockerState{},
		stdout: "Proxy\n" +
			"  State    - stopped\n" +
			"  Network  ws-proxy (172.28.0.2)\n" +
			"\n" +
			"Workspaces\n" +
			"  Route protection  ? unknown\n" +
			"protection scan failed: inspect network: Error response from daemon: No such\n" +
			"network (workspace protection UNKNOWN)\n"},
	{name: "ws proxy status: one workspace unprotected", args: []string{"proxy", "status"}, docker: &fakeHealthyProxy,
		stdout: "Proxy\n" +
			"  State    ✓ running\n" +
			"  Health   ✓ healthy\n" +
			"  Uptime   <uptime>\n" +
			"  Image    devpod-proxy\n" +
			"  Network  ws-proxy (172.28.0.2)\n" +
			"\n" +
			"Workspaces\n" +
			"  unprot-ml-training  ✗ unprotected: default via 172.28.0.1 (not the proxy\n" +
			"                      172.28.0.2)\n" +
			"  web-frontend        ✓ protected\n" +
			"1 of 2 workspace(s) UNPROTECTED — route not via proxy (run: ws proxy fix-routes)\n"},
	{name: "ws proxy doctor: no daemon", args: []string{"proxy", "doctor"}, code: 1,
		check: reportCheck("✗ failed    docker reachable", "? unknown   inbound sockopt.tproxy (advisory)",
			"Failed at check 1 of 13: docker reachable")},
	{name: "ws proxy doctor: the image's datapath differs", args: []string{"proxy", "doctor"}, code: 4,
		docker: &fakeHealthyProxy,
		check: reportCheck("✓ ok        active profile valid (xray -test)", "✗ failed    datapath contract (image ↔ profile)",
			"? unknown   proxy container running and healthy", "Failed at check 4 of 13: datapath contract (image ↔ profile)")},
	{name: "ws proxy doctor --json: no daemon", args: []string{"proxy", "doctor", "--json"}, code: 1,
		stdout: "{\n  \"ok\": false,\n  \"failedAt\": 0,\n  \"checks\": [\n    {\n      \"name\": \"docker reachable\",\n" +
			"      \"ok\": false,\n      \"fix\": \"Start Docker (Docker Desktop or the daemon) and retry.\"\n    }\n  ]\n}\n"},
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
	// The table and its caption go to stdout and nothing to stderr; the
	// tables baseline pins the table itself.
	{name: "ws status: a dirty repo", args: []string{"status"}, setup: withRepos, code: 1,
		check: func(t *testing.T, stdout string) {
			for _, want := range []string{"│ vault-ai ", " ~ dirty ", "╯\n1/3 repos healthy\n"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
		}},
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
			// No ESC byte in either stream, whatever compares the stdout.
			if strings.ContainsRune(stdout+stderr, '\x1b') {
				t.Errorf("an ESC byte in a pipe: stdout %q, stderr %q", stdout, stderr)
			}
			if row.check != nil {
				row.check(t, stdout)
				stdout = row.stdout
			}
			if code != row.code || stdout != row.stdout || stderr != row.stderr {
				t.Errorf("exit %d\n--- stdout:\n%s--- stderr:\n%s\nwant exit %d\n--- stdout:\n%s--- stderr:\n%s",
					code, stdout, stderr, row.code, row.stdout, row.stderr)
			}
		})
	}
}

// TestStatusIgnoresTheSystemGitConfig: the git ws status runs in a child
// reads no system configuration. One that hides untracked files would make
// the fixture's dirty repository — dirty through an untracked file — clean.
func TestStatusIgnoresTheSystemGitConfig(t *testing.T) {
	fx := newStreamsFixture(t)
	withRepos(t, fx)
	system := filepath.Join(fx.home, "system.gitconfig")
	if err := os.WriteFile(system, []byte("[status]\n\tshowUntrackedFiles = no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := streamsRow{name: "ws status under a system gitconfig", args: []string{"status"}, env: []string{"GIT_CONFIG_SYSTEM=" + system}}
	code, stdout, stderr := runStreamsChild(t, fx, row)
	if code != 1 || !strings.Contains(stdout, "~ dirty") || stderr != "" {
		t.Errorf("exit %d\n--- stdout:\n%s--- stderr:\n%s\nwant exit 1, vault-ai dirty, and nothing on stderr", code, stdout, stderr)
	}
}

// TestStartRefusesAnEntryThatDoesNotResolve: a workspace entry that is
// there but does not resolve — a dangling symlink — is refused by ws start
// and ws restart before devpod is asked, with the reason and the remedy that
// removes it; a name that is not there at all is refused by both as not
// found.
func TestStartRefusesAnEntryThatDoesNotResolve(t *testing.T) {
	fx := newStreamsFixture(t)
	entry := filepath.Join(fx.home, "workspaces", "broken")
	if err := os.Symlink(filepath.Join(fx.home, "nowhere"), entry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fx.bin)
	t.Setenv("HOME", fx.home)
	t.Setenv("WORKSPACES_DIR", filepath.Join(fx.home, "workspaces"))
	_, statErr := os.Stat(entry)
	for _, c := range []*cobra.Command{startCmd, restartCmd} {
		var err error
		stderr := captureStderr(t, func() { err = c.RunE(c, []string{"broken"}) })
		p, carried := output.ProblemOf(err)
		want := output.Problem{
			Title: `Workspace "broken" cannot be read`,
			Cause: statErr.Error(),
			Steps: []output.Remedy{{Label: "Delete it", Cmd: "ws delete broken"}},
		}
		if !carried || !reflect.DeepEqual(p, want) || !errors.Is(err, os.ErrNotExist) {
			t.Errorf("ws %s broken returned %v, a Problem %+v (%t); want %+v, an error that is os.ErrNotExist", c.Name(), err, p, carried, want)
		}
		if stderr != "" {
			t.Errorf("ws %s broken wrote %q; want nothing before the root prints the Problem", c.Name(), stderr)
		}
		stderr = captureStderr(t, func() { err = c.RunE(c, []string{"nosuch"}) })
		if p, _ := output.ProblemOf(err); p.Title != `Workspace "nosuch" not found` || stderr != "" {
			t.Errorf("ws %s nosuch wrote %q and returned a Problem %+v; want it refused as not found, nothing written", c.Name(), stderr, p)
		}
	}
}

// TestDeleteKeepsDevpodsLinesOutOfARemovalFailure: when devpod has deleted
// its workspace and the directory then cannot be removed, the Problem the
// root prints names the directory and has the removal's error as its cause —
// not devpod's output, which is all the task's log holds, and which a failed
// devpod delete has already printed under its warning.
func TestDeleteKeepsDevpodsLinesOutOfARemovalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes the entries of a read-only directory")
	}
	fx := newStreamsFixture(t)
	dir := filepath.Join(fx.home, "workspaces", "ops")
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	t.Setenv("PATH", fx.bin)
	t.Setenv("HOME", fx.home)
	t.Setenv("WORKSPACES_DIR", filepath.Join(fx.home, "workspaces"))
	if err := deleteCmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deleteCmd.Flags().Set("force", "false") })

	var err error
	_ = captureStderr(t, func() { err = deleteCmd.RunE(deleteCmd, []string{"ops"}) })
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("ws delete --force ops returned %v; want the removal's permission error", err)
	}
	p, carried := output.ProblemOf(err)
	want := output.Problem{
		Title: `The directory of workspace "ops" could not be removed`,
		Cause: err.Error(),
		Facts: []output.Fact{{K: "Directory", V: dir}},
	}
	if !carried || !reflect.DeepEqual(p, want) {
		t.Errorf("the root would print %+v; want %+v", p, want)
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

// TestWorkspaceRefusalsReturnTheirProblem runs each refusal in process: the
// command returns the error that carries its Problem, for the root to print.
// A command printing the Problem itself and returning an empty exit is byte
// for byte the same in a pipe, which is why the streams cannot tell.
func TestWorkspaceRefusalsReturnTheirProblem(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKSPACES_DIR", dir)
	if err := os.Mkdir(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cmd   *cobra.Command
		args  []string
		title string
		next  output.Remedy
	}{
		{newCmd, []string{"api"}, `Workspace "api" already exists`, output.Remedy{Label: "List workspaces", Cmd: "ws list"}},
		{startCmd, []string{"nosuch"}, `Workspace "nosuch" not found`, output.Remedy{Label: "Create it", Cmd: "ws new nosuch"}},
		{deleteCmd, []string{"nosuch"}, `Workspace "nosuch" not found`, output.Remedy{Label: "Remove it from devpod", Cmd: "devpod delete nosuch"}},
	} {
		err := c.cmd.RunE(c.cmd, c.args)
		p, ok := output.ProblemOf(err)
		if !ok || p.Title != c.title || len(p.Steps) != 2 || p.Steps[1] != c.next {
			t.Errorf("ws %s %s returned %v (Problem %+v); want the error carrying %q with the step %+v",
				c.cmd.Name(), c.args[0], err, p, c.title, c.next)
		}
	}
}

// TestWorkspaceOptions pins the selector's labels of ws ssh and ws code: the
// name, two spaces, and ws list's mark and word for the stream given.
func TestWorkspaceOptions(t *testing.T) {
	workspaces := []workspace.Info{{Name: "api", Status: "Running"}, {Name: "ops", Status: "Stopped"},
		{Name: "legacy-billing", Status: ""}, {Name: "ml-training", Status: "Busy"}, {Name: "web", Status: "Rebuilding"}}
	for _, c := range []struct {
		ascii bool
		want  []string
	}{
		{false, []string{"api  ✓ running", "ops  - stopped", "legacy-billing  - not created", "ml-training  ~ busy", "web  ? rebuilding"}},
		{true, []string{"api  + running", "ops  - stopped", "legacy-billing  - not created", "ml-training  ~ busy", "web  ? rebuilding"}},
	} {
		opts := workspaceOptions(output.NewStreamAt(io.Discard, 80, false, output.ColourNone, c.ascii), workspaces)
		if len(opts) != len(workspaces) {
			t.Fatalf("ascii=%v: %d options for %d workspaces", c.ascii, len(opts), len(workspaces))
		}
		for i, o := range opts {
			if o.Label != c.want[i] || o.Value != workspaces[i].Name {
				t.Errorf("ascii=%v: option %d = {%q, %q}; want {%q, %q}", c.ascii, i, o.Label, o.Value, c.want[i], workspaces[i].Name)
			}
		}
	}
}

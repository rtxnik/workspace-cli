package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/config"
	"github.com/rtxnik/workspace-cli/internal/docker"
)

// A fake Docker Engine API, for the stream matrix's proxy rows.
//
// internal/docker talks to the daemon through the SDK over DOCKER_HOST, so a
// fake on PATH cannot stand in for it the way the fake devpod does. This one
// is an HTTP server on a unix socket that answers the few calls the proxy
// commands make — the ping, and the container, image and network inspections
// — from a state the row chooses. A child gets it through DOCKER_HOST in the
// row's env, which comes after the harness's unreachable default and so wins.

// fakeDockerState is what the fake daemon answers.
type fakeDockerState struct {
	Down      bool              // every call, the ping included, answers 500
	Container *fakeContainer    // nil: no such container
	Labels    map[string]string // the proxy image's labels; nil: no such image
	Network   *fakeNetwork      // nil: no such network
}

type fakeContainer struct {
	Running   bool
	Health    string // "" for no healthcheck
	StartedAt string // RFC 3339
	Image     string
}

type fakeNetwork struct {
	Subnet     string
	Containers []string // endpoint names, in the order the daemon lists them
}

// apiPrefix is the version prefix the SDK puts on every call after it has
// negotiated one from the ping's API-Version header.
var apiPrefix = regexp.MustCompile(`^/v[0-9.]+`)

// handler answers the calls internal/docker makes, in the shapes the SDK
// decodes: the ping, and the container, image and network inspections. A
// missing object is a 404 with a message, which the SDK turns into the
// not-found error the callers test for.
func (st fakeDockerState) handler() http.Handler {
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Api-Version", "1.45")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func(w http.ResponseWriter, what string) {
		reply(w, http.StatusNotFound, map[string]string{"message": "No such " + what})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := apiPrefix.ReplaceAllString(r.URL.Path, "")
		if st.Down {
			reply(w, http.StatusInternalServerError, map[string]string{"message": "daemon down"})
			return
		}
		switch {
		case path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "OK")
		case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
			c := st.Container
			if c == nil {
				notFound(w, "container")
				return
			}
			var health any
			if c.Health != "" {
				health = map[string]string{"Status": c.Health}
			}
			reply(w, http.StatusOK, map[string]any{
				"Id":     strings.Repeat("f", 64),
				"State":  map[string]any{"Running": c.Running, "StartedAt": c.StartedAt, "Health": health},
				"Config": map[string]any{"Image": c.Image},
			})
		case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			if st.Labels == nil {
				notFound(w, "image")
				return
			}
			reply(w, http.StatusOK, map[string]any{
				"Id":     "sha256:" + strings.Repeat("a", 64),
				"Config": map[string]any{"Labels": st.Labels},
			})
		case strings.HasPrefix(path, "/networks/"):
			n := st.Network
			if n == nil {
				notFound(w, "network")
				return
			}
			endpoints := map[string]any{}
			for i, name := range n.Containers {
				endpoints[fmt.Sprintf("%064d", i)] = map[string]string{"Name": name}
			}
			reply(w, http.StatusOK, map[string]any{
				"Name":       strings.TrimPrefix(path, "/networks/"),
				"IPAM":       map[string]any{"Config": []map[string]string{{"Subnet": n.Subnet}}},
				"Containers": endpoints,
			})
		default:
			notFound(w, "endpoint "+path)
		}
	})
}

// writeFakeDockerCLI puts a fake docker on PATH for the one call the proxy
// commands make through the CLI rather than the SDK: `docker exec NAME ip route
// show default`, whose answer routes a container through the proxy (172.28.0.2)
// unless its name carries "unprot". Any other exec — the doctor's xray -test —
// gets the same line and succeeds. It does not replace withFakeDockerBuild,
// which answers a build: a row takes one or the other.
func writeFakeDockerCLI(t *testing.T, bin string) {
	t.Helper()
	const script = `#!/bin/sh
[ "$1" = exec ] || { echo "fake docker: unsupported: $*" >&2; exit 1; }
case "$2" in
*unprot*) echo "default via 172.28.0.1 dev eth0" ;;
*) echo "default via 172.28.0.2 dev eth0" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// startFakeDocker serves st on a unix socket for the rest of the test and
// returns the DOCKER_HOST that reaches it. The socket lives in a directory of
// its own under the system temporary directory, not under t.TempDir(): a unix
// socket's path is limited to 108 bytes, and a test's directory carries its
// name.
func startFakeDocker(t *testing.T, st fakeDockerState) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wsdock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(st.handler())
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return "unix://" + sock
}

// TestFakeDockerAnswersTheSDK holds the fake to the client that reads it:
// internal/docker's own calls, through the SDK, over the fake's socket, must
// see the state the fake was given. A response in a shape the SDK does not
// read — a health status under the wrong key, a network without its IPAM, a
// ping without the version header the client negotiates from — fails here
// rather than as a row of the matrix that reads a wrong report.
func TestFakeDockerAnswersTheSDK(t *testing.T) {
	cfg := config.Config{
		XrayConfig:     filepath.Join(t.TempDir(), "absent.json"),
		ProxyContainer: "dev-proxy",
		ProxyImage:     "devpod-proxy",
		ProxyNetwork:   "ws-proxy",
		ProxySubnet:    "172.28.0.0/16",
		ProxyIP:        "172.28.0.2",
	}

	t.Setenv("DOCKER_HOST", startFakeDocker(t, fakeDockerState{
		Container: &fakeContainer{Running: true, Health: "unhealthy", StartedAt: "2026-10-07T10:00:00Z", Image: "registry.example/devpod-proxy:v26"},
		Labels:    map[string]string{docker.LabelDatapath: "tproxy"},
		Network:   &fakeNetwork{Subnet: "172.28.0.0/16", Containers: []string{"web", "dev-proxy", "api"}},
	}))
	st, err := docker.ProxyStatus(cfg)
	if err != nil {
		t.Fatalf("ProxyStatus over the fake: %v", err)
	}
	if !st.Running || st.Health != "unhealthy" || st.Image != "registry.example/devpod-proxy:v26" || st.Uptime == "" {
		t.Errorf("ProxyStatus read %+v; want running, unhealthy, the fake's image and an uptime", st)
	}
	var passed []string
	for _, r := range docker.ProxyCheck(cfg) {
		if r.Passed {
			passed = append(passed, r.Name)
		}
	}
	if want := "Docker running,Proxy image built,Proxy container running"; strings.Join(passed, ",") != want {
		t.Errorf("ProxyCheck passed %v; want %s (the config is absent)", passed, want)
	}
	if labels, err := docker.ImageLabels(cfg); err != nil || labels[docker.LabelDatapath] != "tproxy" {
		t.Errorf("ImageLabels = %v, %v; want the datapath label tproxy", labels, err)
	}
	if subnet, err := docker.NetworkSubnet(cfg); err != nil || subnet != "172.28.0.0/16" {
		t.Errorf("NetworkSubnet = %q, %v; want 172.28.0.0/16", subnet, err)
	}
	if names, err := docker.ProxyConnectedContainers(cfg); err != nil || strings.Join(names, ",") != "api,web" {
		t.Errorf("ProxyConnectedContainers = %v, %v; want api,web (sorted, the proxy left out)", names, err)
	}

	t.Setenv("DOCKER_HOST", startFakeDocker(t, fakeDockerState{}))
	if st, err := docker.ProxyStatus(cfg); err != nil || st.Running {
		t.Errorf("with no container ProxyStatus = %+v, %v; want not running and no error", st, err)
	}
	if _, err := docker.NetworkSubnet(cfg); err == nil {
		t.Error("with no network NetworkSubnet returned no error")
	}

	t.Setenv("DOCKER_HOST", startFakeDocker(t, fakeDockerState{Down: true}))
	if _, err := docker.ProxyStatus(cfg); err == nil {
		t.Error("with the daemon down ProxyStatus returned no error")
	}
	if r := docker.ProxyCheck(cfg); len(r) == 0 || r[0].Passed {
		t.Errorf("with the daemon down ProxyCheck = %+v; want Docker running to fail", r)
	}
}

// TestFakeDockerCLIAnswersRouteLookups holds the fake docker on PATH to the
// route lookup that reads it: a workspace whose name carries "unprot" routes
// around the proxy, every other one through it.
func TestFakeDockerCLIAnswersRouteLookups(t *testing.T) {
	bin := t.TempDir()
	writeFakeDockerCLI(t, bin)
	t.Setenv("PATH", bin)
	for name, want := range map[string]string{"api": "172.28.0.2", "unprot-ml": "172.28.0.1"} {
		if via, err := docker.DefaultRouteOf(name); err != nil || via != want {
			t.Errorf("DefaultRouteOf(%q) = %q, %v; want %s", name, via, err, want)
		}
	}
}

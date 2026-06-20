// Package proxyengine is a thin, documented boundary between the CLI and the
// proxy backend that turns a share URI into a runnable config and validates it.
//
// Why a seam: xray-core is the only backend today, but a future one (sing-box,
// a native hysteria2 client) should be swappable without rewiring callers. The
// surface is deliberately minimal — one interface, one concrete XrayEngine, and
// a test fake. No registries, no plugin loaders; a new backend is a new type
// that satisfies Engine.
//
// Import direction (cycle-avoidance): proxyengine imports the protocol builders
// (vless, hysteria2), the neutral config types (xrayconf), the active backend
// (xray, for Validate), and config. The xray package does NOT import proxyengine
// — generateProfileConfig stays where it is — so the dependency is one-way and
// `go build ./...` stays cycle-free.
package proxyengine

import "github.com/rtxnik/workspace-cli/internal/config"

// Profile is the engine's neutral build input: a proxy share URI that the
// engine scheme-dispatches (vless://, hysteria2://, hy2://).
type Profile struct {
	URI string
}

// Engine turns a Profile into a backend config, validates a stored profile,
// and probes live tunnel connectivity.
type Engine interface {
	// BuildConfig scheme-dispatches p.URI and returns the marshalled backend
	// config (xray JSON today).
	BuildConfig(p Profile) ([]byte, error)
	// Validate checks an already-written profile by name against the backend
	// (xray run -test today), delegating to the active backend.
	Validate(cfg config.Config, profileName string) error
	// Probe performs a live tunnel-connectivity check by comparing the host's
	// direct egress IP with the proxied egress IP reported by the container.
	// Returns a ProbeResult with Tunneled=true only when both IPs are non-empty
	// and different, proving that traffic exits via the tunnel endpoint.
	Probe(cfg config.Config) (ProbeResult, error)
}

// Default returns the engine for the only backend wired today (xray-core).
// Callers depend on Engine, not the concrete type, so swapping the default is
// a one-line change once a second backend exists.
func Default() Engine { return &XrayEngine{} }

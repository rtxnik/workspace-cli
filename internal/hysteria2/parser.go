package hysteria2

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Config holds parsed Hysteria2 URI parameters.
type Config struct {
	Auth    string // userinfo — the hy2 password (NOT a UUID)
	Address string
	Port    int

	// TLS
	SNI           string
	ALPN          []string
	Fingerprint   string
	AllowInsecure bool
	PinSHA256     string // normalized lowercase hex of 32-byte SHA-256; empty = no pinning

	// Salamander obfuscation (optional)
	Obfs         string // "salamander" or ""
	ObfsPassword string

	// Remark from the URI fragment (cosmetic; the on-disk profile name is the
	// CLI <name> argument, not this).
	Remark string

	// Port-hopping (udphop). HopPorts is the full original port spec
	// (e.g. "443,5000-6000" or "20000-50000"); empty when no ranges were present.
	// PortHopping is true when HopPorts != "".
	HopPorts    string // e.g. "443,5000-6000"; "" = no hopping
	HopInterval int    // seconds between hops; default 30 when hopping
	PortHopping bool   // synonym for HopPorts != ""

	// Bandwidth & congestion (optional; emitted only when provided).
	Up         string // e.g. "50mbps"
	Down       string // e.g. "200mbps"
	Congestion string // "" | "reno" | "bbr" | "brutal" | "force-brutal"
}

// Parse parses a hysteria2:// (alias hy2://) URI into a Config.
func Parse(uri string) (Config, error) {
	switch {
	case strings.HasPrefix(uri, "hysteria2://"), strings.HasPrefix(uri, "hy2://"):
	default:
		return Config{}, fmt.Errorf("not a Hysteria2 URI: must start with hysteria2:// or hy2://")
	}

	// net/url rejects the comma port-hopping form (e.g. ":443,5000-6000").
	// Reduce it to the base port before url.Parse; capture the full spec.
	normalized, hopPorts, hopped := stripPortHopping(uri)

	u, err := url.Parse(normalized)
	if err != nil {
		return Config{}, fmt.Errorf("parse URI: %w", err)
	}
	if u.User == nil {
		return Config{}, fmt.Errorf("missing auth (password) in URI")
	}
	// A hysteria2 URI carries its whole userinfo as the auth secret, which may
	// contain ':'. net/url splits userinfo on the first colon, so rejoin the
	// username and password to recover the full value.
	auth := u.User.Username()
	if pw, ok := u.User.Password(); ok {
		auth += ":" + pw
	}
	if auth == "" {
		return Config{}, fmt.Errorf("missing auth (password) in URI")
	}
	host := u.Hostname()
	if host == "" {
		return Config{}, fmt.Errorf("missing host in URI")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return Config{}, fmt.Errorf("invalid port %q: %w", u.Port(), err)
	}
	if port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("invalid port %d: must be 1-65535", port)
	}

	q := u.Query()
	cfg := Config{
		Auth:          auth,
		Address:       host,
		Port:          port,
		SNI:           q.Get("sni"),
		Fingerprint:   q.Get("fp"),
		Obfs:          q.Get("obfs"),
		ObfsPassword:  q.Get("obfs-password"),
		AllowInsecure: q.Get("insecure") == "1" || q.Get("allowInsecure") == "1",
		Remark:        u.Fragment,
		HopPorts:      hopPorts,
		PortHopping:   hopped,
	}

	// Pin SHA-256: accept hysteria hex-colon, bare hex, or base64.
	if raw := q.Get("pinSHA256"); raw != "" || q.Get("pin-sha256") != "" {
		if raw == "" {
			raw = q.Get("pin-sha256")
		}
		pin, err := NormalizePinSHA256(raw)
		if err != nil {
			return Config{}, err
		}
		cfg.PinSHA256 = pin
	}

	// ALPN: comma-separated; default ["h3"].
	if raw := q.Get("alpn"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.ALPN = append(cfg.ALPN, p)
			}
		}
	}
	if len(cfg.ALPN) == 0 {
		cfg.ALPN = []string{"h3"}
	}

	// Port-hopping interval (only meaningful when HopPorts is set).
	if cfg.HopPorts != "" {
		cfg.HopInterval = 30
		if v := q.Get("hopInterval"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 5 {
				return Config{}, fmt.Errorf("invalid hopInterval %q: integer ≥5", v)
			}
			cfg.HopInterval = n
		}
	}

	// Bandwidth & congestion (optional).
	cfg.Up = q.Get("up")
	cfg.Down = q.Get("down")
	cfg.Congestion = q.Get("congestion")
	switch cfg.Congestion {
	case "", "reno", "bbr", "brutal", "force-brutal":
	default:
		return Config{}, fmt.Errorf("invalid congestion %q: reno|bbr|brutal|force-brutal", cfg.Congestion)
	}

	// Defaults.
	if cfg.SNI == "" {
		cfg.SNI = host
	}
	if cfg.Fingerprint == "" {
		cfg.Fingerprint = "chrome"
	}
	// "none"/empty obfs => no obfs (drop any stray password).
	if strings.EqualFold(cfg.Obfs, "none") {
		cfg.Obfs = ""
	}
	if cfg.Obfs == "" {
		cfg.ObfsPassword = ""
	}

	return cfg, nil
}

// stripPortHopping removes port-hopping detail (a comma list and/or a
// "lo-hi" range) from the authority's port, reducing it to a single base
// port and returning the full original spec; a malformed or single-port
// authority is returned unchanged. It only touches the authority segment
// (between "://" and the first '/', '?' or '#'), so query-string commas are
// untouched.
func stripPortHopping(uri string) (normalized string, hopPorts string, hopped bool) {
	const sep = "://"
	i := strings.Index(uri, sep)
	if i < 0 {
		return uri, "", false
	}
	head := uri[:i+len(sep)]
	rest := uri[i+len(sep):]

	authority := rest
	tail := ""
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		authority = rest[:end]
		tail = rest[end:]
	}

	userinfo := ""
	hostport := authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userinfo = authority[:at+1]
		hostport = authority[at+1:]
	}

	colon := strings.LastIndex(hostport, ":")
	if colon < 0 {
		return uri, "", false
	}
	portSpec := hostport[colon+1:]
	// Only a well-formed hop spec is normalized; a plain single port or a
	// malformed spec is left for url.Parse to handle exactly as before. Query
	// commas never reach here — only the authority segment is inspected.
	base, ok := hopBasePort(portSpec)
	if !ok {
		return uri, "", false
	}
	return head + userinfo + hostport[:colon+1] + base + tail, portSpec, true
}

// hopBasePort validates a Hysteria2 port-hopping spec — a comma list whose every
// item is a plain port or a "lo-hi" range, each endpoint numeric and in
// 1..65535 — and returns the base port url.Parse should see: the first listed
// port, or the low end of the first range. ok is false for a plain single port
// (not hopping) or any malformed spec, so malformed input is never widened into
// an accepted config.
func hopBasePort(spec string) (base string, ok bool) {
	if !strings.ContainsAny(spec, ",-") {
		return "", false // plain single port -> not hopping
	}
	var first string
	for i, item := range strings.Split(spec, ",") {
		loStr, hiStr, isRange := strings.Cut(item, "-")
		lo, okLo := portNum(loStr)
		if !okLo {
			return "", false
		}
		if isRange {
			hi, okHi := portNum(hiStr)
			if !okHi || lo > hi {
				return "", false
			}
		}
		if i == 0 {
			first = loStr
		}
	}
	return first, true
}

// portNum parses s as a decimal port and reports whether it is in 1..65535.
func portNum(s string) (n int, ok bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= 65535
}

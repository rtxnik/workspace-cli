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
	// (e.g. "443,5000-6000"); empty when no ranges were present.
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
	if u.User == nil || u.User.Username() == "" {
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
		Auth:          u.User.Username(),
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
		pin, err := normalizePinSHA256(raw)
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

// stripPortHopping removes a ",<ranges>" suffix from the port in the URI's
// authority (the Hysteria2 port-hopping form), returning the normalized URI,
// the full original port spec (base + ranges, e.g. "443,5000-6000"), and
// whether ranges were present. It only touches the authority segment (between
// "://" and the first '/', '?' or '#'), so query-string commas are untouched.
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
	comma := strings.IndexByte(hostport[colon+1:], ',')
	if comma < 0 {
		return uri, "", false
	}
	base := hostport[colon+1 : colon+1+comma]
	ranges := hostport[colon+1+comma+1:]
	fullSpec := base + "," + ranges
	return head + userinfo + hostport[:colon+1] + base + tail, fullSpec, true
}

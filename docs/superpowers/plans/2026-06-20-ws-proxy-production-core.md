# ws proxy production — Core (workspace-cli) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unblock and harden the `hysteria2` container-proxy path in `workspace-cli` — correct TLS trust (no `allowInsecure`, cert pinning), port-hopping/brutal, secret-at-rest perms, a pluggable engine seam, a neutral config core, and turnkey `doctor`/`test` diagnostics — all unit/integration-testable without Docker.

**Architecture:** Keep xray-core pinned `v26.2.6`. Promote the neutral xray-config types out of `internal/vless` into `internal/xrayconf`; add a thin `internal/proxyengine.Engine` seam; route profile generation/validation/probing through it. Diagnostics (`ws proxy doctor`, real `ws proxy test`) prove tunneling by exit-IP comparison.

**Tech Stack:** Go 1.24+, cobra, docker SDK, lipgloss/huh (existing `internal/output`).

## Global Constraints

- xray-core engine is pinned **exactly `v26.2.6`** — never advertise `>= v26.1.13` (incompatible obfs schema). Copy this rationale into any comment that mentions a version floor.
- **Never emit `allowInsecure`** in any generated config (xray v26.2.6 `TLSConfig.Build()` hard-errors on `allowInsecure:true` after 2026-06-01).
- Certificate pin field name is exactly **`pinnedPeerCertSha256`**; value is base64 of `sha256(DER(leaf cert))`.
- hysteria2 outbound JSON (v26.2.6, verbatim): `protocol:"hysteria"`, `settings.version=2`, `streamSettings.hysteriaSettings.{version:2,auth}`, salamander as `streamSettings.finalmask.udp=[{"type":"salamander","settings":{"password":...}}]`, port-hop as `hysteriaSettings.udphop.{port,interval}` (interval ≥5), bandwidth `hysteriaSettings.up/down` (≥65536 Bps), `hysteriaSettings.congestion` ∈ {reno,bbr,brutal,force-brutal}.
- Secrets on disk: profile files `0600`, xray config/profiles dirs `0700`.
- No AI attribution in commits/code/docs. Discussion English in code; conventional-commit messages.
- Gates (must stay green): `go vet ./...`, `go test -race ./...`, coverage ≥30%, `golangci-lint run` (v2.12.2).
- TDD: failing test first, then minimal implementation. Commit per task.
- Karpathy discipline: bounded surface per task, log kept/discarded attempts to `.planning/LEDGER.tsv`.

---

### Task 1: Promote neutral xray-config types into `internal/xrayconf`

Behavior-preserving refactor. The neutral config types currently live in `internal/vless` and `internal/hysteria2` imports `internal/vless` to reach them. Move the neutral types to a protocol-agnostic package; leave vless/hysteria2 with only their protocol-specific code.

**Files:**
- Create: `internal/xrayconf/config.go` (moved neutral types + `AssembleConfig`/`WriteConfig`/`writeConfig`)
- Create: `internal/xrayconf/config_test.go` (moved `AssembleConfig` assertions from `internal/vless/config_test.go`)
- Modify: `internal/vless/config.go` (keep only `VLESSConfig` construction: `GenerateConfig`, `WriteNewConfig`, `AddNode`, `buildOutbound`, `buildStreamSettings`; reference `xrayconf.*`)
- Modify: `internal/hysteria2/config.go` (`vless.*` → `xrayconf.*`)
- Modify: `internal/xray/profile.go`, `internal/xray/show.go`, `internal/xray/regenerate.go` (`vless.XrayConfig`/`vless.Outbound` → `xrayconf.*`)

**Interfaces:**
- Produces (package `xrayconf`): `XrayConfig`, `LogConfig`, `Inbound`, `InboundSetting`, `Sniffing`, `Outbound`, `RoutingConfig`, `Balancer`, `BalancerStrategy` structs (identical fields/json tags to today's `vless` versions); `func AssembleConfig(proxy Outbound) *XrayConfig`; `func WriteConfig(path string, xc *XrayConfig) error`.
- `internal/vless` keeps `VLESSConfig`, `Parse`, `GenerateConfig(cfg VLESSConfig, tag string) (*xrayconf.XrayConfig, error)`, `WriteNewConfig`, `AddNode`.

- [ ] **Step 1: Create `internal/xrayconf/config.go`** — copy the neutral types and `AssembleConfig`/`WriteConfig`/`writeConfig` verbatim from `internal/vless/config.go` (lines 10–118, 120–185), change `package vless` → `package xrayconf`. Keep json tags byte-identical.

- [ ] **Step 2: Move the `AssembleConfig` test** — cut the `AssembleConfig`-focused cases from `internal/vless/config_test.go` into `internal/xrayconf/config_test.go` (`package xrayconf`), adjusting references.

- [ ] **Step 3: Trim `internal/vless/config.go`** — delete the moved types/functions; add `import "github.com/rtxnik/workspace-cli/internal/xrayconf"`; change return types and internal references (`GenerateConfig` returns `*xrayconf.XrayConfig`; `buildOutbound` returns `xrayconf.Outbound`; `AddNode` unmarshals into `xrayconf.XrayConfig`; `WriteNewConfig` calls `xrayconf.WriteConfig`).

- [ ] **Step 4: Update `internal/hysteria2/config.go`** — replace every `vless.` with `xrayconf.` and the import path; `BuildOutbound` returns `xrayconf.Outbound`; `GenerateConfig` returns `*xrayconf.XrayConfig`; `WriteNewConfig` calls `xrayconf.WriteConfig`.

- [ ] **Step 5: Update `internal/xray` references** — in `profile.go`, `show.go`, `regenerate.go` replace `vless.XrayConfig`→`xrayconf.XrayConfig`, `vless.Outbound`→`xrayconf.Outbound`, and imports. (`generateProfileConfig` still calls `vless.GenerateConfig`/`hysteria2.GenerateConfig`, which now return `*xrayconf.XrayConfig`.)

- [ ] **Step 6: Build + full test suite**

Run: `go build ./... && go test ./...`
Expected: PASS (no behavior change). If a test referenced `vless.XrayConfig`/`vless.AssembleConfig`, update it to `xrayconf.*`.

- [ ] **Step 7: Byte-identical guard** — add `internal/xrayconf/config_test.go` golden assertion that `AssembleConfig` of a fixed vless outbound marshals to the same bytes as a checked-in golden (`testdata/assemble_vless.golden.json`). Generate the golden from current output once, commit it.

Run: `go test ./internal/xrayconf/ -run TestAssembleGolden -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/xrayconf internal/vless internal/hysteria2 internal/xray
git commit -m "refactor(proxy): extract neutral xray-config types into internal/xrayconf"
```

---

### Task 2: hysteria2 TLS trust model — drop `allowInsecure`, add `pinnedPeerCertSha256`

**Files:**
- Modify: `internal/hysteria2/parser.go` (Config field `PinSHA256`; parse + normalize `pinSHA256`/`pin-sha256`; keep parsing `insecure` but mark for warn-drop)
- Create: `internal/hysteria2/pin.go` (`normalizePinSHA256`)
- Create: `internal/hysteria2/pin_test.go`
- Modify: `internal/hysteria2/config.go` (`BuildOutbound` tlsSettings: emit `pinnedPeerCertSha256` when set, never `allowInsecure`)
- Modify: `internal/hysteria2/hysteria2_test.go` (assert no `allowInsecure`; assert pin emitted)
- Modify: `internal/xray/show.go` + `internal/xray/profile.go` (`DetailedProfile`/`ProfileSummary`: replace `AllowInsecure`/`Insecure` display with `PinSHA256` presence; `loadHysteria` reads `pinnedPeerCertSha256`)
- Modify: `cmd/proxy_profile.go` (show: print `Pinned: yes/no` instead of `Insecure`)

**Interfaces:**
- Consumes: `xrayconf` (Task 1).
- Produces: `hysteria2.Config.PinSHA256 string` (normalized base64); `func normalizePinSHA256(string) (string, error)`.

- [ ] **Step 1: Write failing test for pin normalization** (`internal/hysteria2/pin_test.go`)

```go
package hysteria2

import "testing"

func TestNormalizePinSHA256(t *testing.T) {
	// 32 zero bytes -> base64 "AAAA...=" ; hex-colon form must map to the same base64.
	hexColon := "00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00"
	wantB64 := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	got, err := normalizePinSHA256(hexColon)
	if err != nil || got != wantB64 {
		t.Fatalf("hex-colon: got %q err %v, want %q", got, err, wantB64)
	}
	if got, err := normalizePinSHA256(wantB64); err != nil || got != wantB64 {
		t.Fatalf("base64 passthrough: got %q err %v", got, err)
	}
	if _, err := normalizePinSHA256("not-a-pin"); err == nil {
		t.Fatalf("expected error for junk input")
	}
	if got, _ := normalizePinSHA256(""); got != "" {
		t.Fatalf("empty must stay empty, got %q", got)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`normalizePinSHA256` undefined)

Run: `go test ./internal/hysteria2/ -run TestNormalizePinSHA256`

- [ ] **Step 3: Implement `internal/hysteria2/pin.go`**

```go
package hysteria2

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// normalizePinSHA256 converts a certificate SHA-256 pin into xray-core's
// expected form: base64 of the raw 32 sha256 bytes (tlsSettings.pinnedPeerCertSha256).
// Accepts hysteria-style hex-with-colons ("AA:BB:.."), bare hex, or an
// already-base64 value. Returns "" for empty input; an error for anything
// that is not exactly 32 bytes once decoded.
func normalizePinSHA256(pin string) (string, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return "", nil
	}
	if strings.Contains(pin, ":") || isHex64(pin) {
		b, err := hex.DecodeString(strings.ReplaceAll(pin, ":", ""))
		if err != nil || len(b) != 32 {
			return "", fmt.Errorf("invalid pin sha256 %q: want 32-byte hex or base64", pin)
		}
		return base64.StdEncoding.EncodeToString(b), nil
	}
	b, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("invalid pin sha256 %q: want 32-byte hex or base64", pin)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/hysteria2/ -run TestNormalizePinSHA256`

- [ ] **Step 5: Wire pin into parser** — in `internal/hysteria2/parser.go`, add `PinSHA256 string` to `Config`; after building `cfg`, parse and normalize:

```go
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
```

Keep `AllowInsecure` parsing as-is (still read `insecure`/`allowInsecure`) — it is now only used to emit a warning, never a config field.

- [ ] **Step 6: Update `BuildOutbound` tlsSettings** (`internal/hysteria2/config.go`) — replace the `tlsSettings` map so it NEVER contains `allowInsecure` and contains `pinnedPeerCertSha256` only when set:

```go
	tls := map[string]any{
		"serverName":  cfg.SNI,
		"alpn":        cfg.ALPN,
		"fingerprint": cfg.Fingerprint,
	}
	if cfg.PinSHA256 != "" {
		tls["pinnedPeerCertSha256"] = cfg.PinSHA256
	}
	// allowInsecure is intentionally never emitted: xray-core v26.2.6 TLSConfig.Build()
	// hard-errors on allowInsecure:true after 2026-06-01. Use pinnedPeerCertSha256 for
	// self-signed endpoints.
	stream := map[string]any{
		"network":  "hysteria",
		"security": "tls",
		"tlsSettings": tls,
		"hysteriaSettings": map[string]any{
			"version": 2,
			"auth":    cfg.Auth,
		},
	}
```

- [ ] **Step 7: Update generation test** — in `hysteria2_test.go`, extend `TestGenerateConfigWithObfs` to assert `tlsSettings` has **no** `allowInsecure` key and (for a pinned URI) `pinnedPeerCertSha256` equals the normalized value. Add a `?pinSHA256=<hex-colon>` case.

```go
func TestGenerateConfigPinNoInsecure(t *testing.T) {
	pinHex := "00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00"
	cfg, err := Parse("hy2://pw@example.com:443?pinSHA256=" + pinHex)
	if err != nil { t.Fatal(err) }
	xc, _ := GenerateConfig(cfg, "proxy-1")
	var ss map[string]any
	_ = json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss)
	tls := ss["tlsSettings"].(map[string]any)
	if _, ok := tls["allowInsecure"]; ok {
		t.Errorf("allowInsecure must never be emitted")
	}
	if tls["pinnedPeerCertSha256"] != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Errorf("pin = %v", tls["pinnedPeerCertSha256"])
	}
}
```

- [ ] **Step 8: Warn-drop on `insecure`** — in `cmd/proxy.go` (`proxyInitCmd`) and `internal/xray/profile.go` (`generateProfileConfig` hy2 branch), after parsing, if `parsed.AllowInsecure && parsed.PinSHA256 == ""`, emit:

```go
output.Warn("hysteria2 'insecure' is unsupported on xray-core v26.2.6; ignoring. For a self-signed endpoint, pin the cert: add ?pinSHA256=<sha256> (run 'ws proxy doctor' to print it).")
```

- [ ] **Step 9: Update show/summary** — `DetailedProfile`: remove `AllowInsecure` field, add `PinSHA256 string json:"pinSHA256,omitempty"`. `loadHysteria` reads `tlsSettings.pinnedPeerCertSha256` into it. In `cmd/proxy_profile.go` show, replace the `Insecure:` line for hy2 with `Pinned:    yes|no` (mask the value; `--reveal` prints it). Update `MaskShort` usage for the pin.

- [ ] **Step 10: Full suite + vet + lint**

Run: `go test ./... && go vet ./... && golangci-lint run`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/hysteria2 internal/xray cmd
git commit -m "feat(hysteria2): replace allowInsecure with pinnedPeerCertSha256 cert pinning"
```

---

### Task 3: hysteria2 port-hopping (udphop) + bandwidth + congestion

**Files:**
- Modify: `internal/hysteria2/parser.go` (capture hop range string + `hopInterval`/`up`/`down`/`congestion`; stop dropping ranges)
- Modify: `internal/hysteria2/config.go` (`BuildOutbound`: emit `udphop`/`up`/`down`/`congestion`)
- Modify: `internal/hysteria2/hysteria2_test.go`
- Modify: `cmd/proxy.go` + `internal/xray/profile.go` (drop the "port-hopping ranges dropped" warning; ranges are now honored)

**Interfaces:**
- Produces: `Config.HopPorts string` (e.g. `"443,5000-6000"`), `Config.HopInterval int`, `Config.Up string`, `Config.Down string`, `Config.Congestion string`.

- [ ] **Step 1: Failing test** — port-hopping is now emitted, not dropped:

```go
func TestGenerateConfigPortHopping(t *testing.T) {
	cfg, err := Parse("hysteria2://pw@h.example:443,5000-6000?hopInterval=30&up=50mbps&down=200mbps&congestion=brutal&sni=h.example")
	if err != nil { t.Fatal(err) }
	if cfg.Port != 443 || cfg.HopPorts == "" { t.Fatalf("hop: port=%d hopPorts=%q", cfg.Port, cfg.HopPorts) }
	xc, _ := GenerateConfig(cfg, "proxy-1")
	var ss map[string]any
	_ = json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss)
	hy := ss["hysteriaSettings"].(map[string]any)
	udphop := hy["udphop"].(map[string]any)
	if udphop["port"] != "443,5000-6000" { t.Errorf("udphop.port = %v", udphop["port"]) }
	if udphop["interval"].(float64) != 30 { t.Errorf("interval = %v", udphop["interval"]) }
	if hy["up"] != "50mbps" || hy["down"] != "200mbps" || hy["congestion"] != "brutal" {
		t.Errorf("up/down/congestion = %v/%v/%v", hy["up"], hy["down"], hy["congestion"])
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

Run: `go test ./internal/hysteria2/ -run TestGenerateConfigPortHopping`

- [ ] **Step 3: Parser** — replace `stripPortHopping`'s drop with capture. Keep base port in `Port`; set `HopPorts` to the full original port spec (base + ranges, comma-joined) when ranges were present; parse `hopInterval` (default 30 when port-hopping and unset; reject <5), `up`, `down`, `congestion`:

```go
	cfg.HopPorts = hopPorts // e.g. "443,5000-6000"; "" when no ranges
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
	cfg.Up = q.Get("up")
	cfg.Down = q.Get("down")
	cfg.Congestion = q.Get("congestion")
	switch cfg.Congestion {
	case "", "reno", "bbr", "brutal", "force-brutal":
	default:
		return Config{}, fmt.Errorf("invalid congestion %q: reno|bbr|brutal|force-brutal", cfg.Congestion)
	}
```

Update `stripPortHopping` to also return the full port spec (base + ranges) so `HopPorts` can be set; keep `PortHopping` as a synonym for `HopPorts != ""`.

- [ ] **Step 4: Config emit** (`BuildOutbound`) — extend `hysteriaSettings`:

```go
	hy := map[string]any{"version": 2, "auth": cfg.Auth}
	if cfg.HopPorts != "" {
		udphop := map[string]any{"port": cfg.HopPorts}
		if cfg.HopInterval > 0 {
			udphop["interval"] = cfg.HopInterval
		}
		hy["udphop"] = udphop
	}
	if cfg.Up != "" { hy["up"] = cfg.Up }
	if cfg.Down != "" { hy["down"] = cfg.Down }
	if cfg.Congestion != "" { hy["congestion"] = cfg.Congestion }
	stream["hysteriaSettings"] = hy
```

- [ ] **Step 5: Remove the drop warning** — delete the `if parsed.PortHopping { output.Warn("...dropped...") }` blocks in `cmd/proxy.go` (`proxyInitCmd`) and `internal/xray/profile.go` (`generateProfileConfig`). Update `TestParsePortHopping` to assert `HopPorts == "443,5000-6000"` instead of "dropped".

- [ ] **Step 6: Run suite — expect PASS.**

Run: `go test ./internal/hysteria2/ ./cmd/ ./internal/xray/`

- [ ] **Step 7: Commit**

```bash
git add internal/hysteria2 cmd internal/xray
git commit -m "feat(hysteria2): honor port-hopping (udphop) + bandwidth + congestion"
```

---

### Task 4: Secret-at-rest permissions (0600 files / 0700 dirs)

**Files:**
- Modify: `internal/xrayconf/config.go` (`writeConfig`: dir `0700`, file `0600`)
- Modify: `internal/xray/profile.go` (`AddProfile`: `MkdirAll 0700`, `WriteFile 0600`)
- Modify: `internal/xray/regenerate.go` (`WriteFile 0600`)
- Modify: `internal/xray/migrate.go` (`MkdirAll 0700`)
- Modify: `cmd/proxy_helpers.go` (`setXrayLogLevel`: `WriteFile 0600`)
- Test: `internal/xray/profile_test.go` (assert perms)

**Interfaces:** none new.

- [ ] **Step 1: Failing test** — added profile is `0600`, profiles dir `0700`:

```go
func TestAddProfilePerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XRAY_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("XRAY_PROFILES_DIR", filepath.Join(dir, "profiles"))
	cfg := config.Load()
	if err := AddProfile(cfg, "p1", "hy2://pw@h.example:443", false); err != nil { t.Fatal(err) }
	fi, _ := os.Stat(filepath.Join(cfg.XrayProfilesDir, "p1.json"))
	if fi.Mode().Perm() != 0o600 { t.Errorf("profile perm = %o, want 600", fi.Mode().Perm()) }
	di, _ := os.Stat(cfg.XrayProfilesDir)
	if di.Mode().Perm() != 0o700 { t.Errorf("dir perm = %o, want 700", di.Mode().Perm()) }
}
```

- [ ] **Step 2: Run — expect FAIL** (currently 0644/0755).

Run: `go test ./internal/xray/ -run TestAddProfilePerms`

- [ ] **Step 3: Change every xray-tree write** — `os.WriteFile(..., 0o644)` → `0o600` and `os.MkdirAll(..., 0o755)` → `0o700` in the five files listed. (Do NOT change non-xray writes, e.g. workspace-profile templates in `internal/profile`.)

- [ ] **Step 4: Run — expect PASS** (and full `go test ./...`).

- [ ] **Step 5: Commit**

```bash
git add internal/xrayconf internal/xray cmd
git commit -m "fix(proxy): write xray profiles 0600 / dirs 0700 (secret-at-rest)"
```

---

### Task 5: Golden generation tests + emitted-key guard

Protect against the silent-unknown-field failure mode (xray ignores misnamed keys): pin our emitter with golden files and an explicit key assertion.

**Files:**
- Create: `internal/hysteria2/testdata/{base,obfs,pin,udphop}.golden.json`
- Create: `internal/hysteria2/golden_test.go`
- Modify: `internal/hysteria2/config.go` (add `expectedStreamKeys` assertion helper used by the test only — or keep the check in the test)

**Interfaces:** none new (test-only).

- [ ] **Step 1: Write golden test** that generates configs for 4 URIs (base, salamander obfs, pinned, port-hopping) with placeholder secrets and compares to checked-in goldens; plus asserts the hy2 `streamSettings` top-level keys are exactly the allowed set `{network,security,tlsSettings,hysteriaSettings,finalmask}` (no stray/typo'd key):

```go
func TestHysteriaStreamKeysAllowed(t *testing.T) {
	allowed := map[string]bool{"network": true, "security": true, "tlsSettings": true, "hysteriaSettings": true, "finalmask": true}
	cfg, _ := Parse("hysteria2://pw@h.example:443?obfs=salamander&obfs-password=OBFS&pinSHA256=" + zeroPinHex)
	xc, _ := GenerateConfig(cfg, "proxy-1")
	var ss map[string]json.RawMessage
	_ = json.Unmarshal(xc.Outbounds[0].StreamSettings, &ss)
	for k := range ss {
		if !allowed[k] { t.Errorf("unexpected streamSettings key %q (typo would be silently dropped by xray)", k) }
	}
}
```

- [ ] **Step 2: Generate goldens** — run a one-off to write the 4 golden files from current output, eyeball them against the §Global-Constraints schema, commit.

- [ ] **Step 3: Run — expect PASS.**

Run: `go test ./internal/hysteria2/ -run 'Golden|StreamKeys'`

- [ ] **Step 4: Commit**

```bash
git add internal/hysteria2
git commit -m "test(hysteria2): golden configs + emitted-key guard against silent field drop"
```

---

### Task 6: Engine seam — `internal/proxyengine`

A thin, documented boundary so a future backend is swappable. Wraps existing generation + validation; `Probe` is added in Task 7.

**Files:**
- Create: `internal/proxyengine/engine.go` (`Engine` interface, `Profile` input type)
- Create: `internal/proxyengine/xray.go` (`XrayEngine` implementing `BuildConfig`, `Validate`)
- Create: `internal/proxyengine/engine_test.go` (table test + a `fakeEngine`)
- Modify: `internal/xray/profile.go` (`generateProfileConfig` delegates to `proxyengine` dispatch) — or keep `generateProfileConfig` and have `XrayEngine.BuildConfig` call it (avoid a churn cycle; pick the lower-risk wiring and note it).

**Interfaces:**
- Produces:

```go
package proxyengine

type Profile struct{ URI string } // scheme-dispatched

type Engine interface {
	BuildConfig(p Profile) ([]byte, error)
	Validate(cfg config.Config, profileName string) error
}

func Default() Engine // returns *XrayEngine
```

- [ ] **Step 1: Failing test** — `Default().BuildConfig` produces valid hy2/vless JSON; `Validate` delegates to `xray.ValidateProfile`:

```go
func TestXrayEngineBuildConfig(t *testing.T) {
	out, err := proxyengine.Default().BuildConfig(proxyengine.Profile{URI: "hy2://pw@h.example:443"})
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(out), `"protocol": "hysteria"`) { t.Errorf("bad config: %s", out) }
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement `engine.go` + `xray.go`** — `XrayEngine.BuildConfig` dispatches on URI scheme (reuse `vless`/`hysteria2` `GenerateConfig` + `xrayconf` marshal); `Validate` calls the existing `xray.ValidateProfile`. To avoid an import cycle (`xray` ↔ `proxyengine`), put the scheme-dispatch in `proxyengine` and have `internal/xray.generateProfileConfig` call `proxyengine.Default().BuildConfig` (then unmarshal) — or, if simpler, leave `xray.generateProfileConfig` as-is and have `proxyengine` call into `vless`/`hysteria2` directly. Choose the wiring with no cycle; document it in a comment.

- [ ] **Step 4: Run — expect PASS** + `go test ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/proxyengine internal/xray
git commit -m "feat(proxy): add thin proxyengine.Engine seam (xray backend)"
```

---

### Task 7: `Engine.Probe` + real `ws proxy test` (exit-IP proof)

**Files:**
- Modify: `internal/proxyengine/engine.go` (`Probe` method + `ProbeResult`)
- Modify: `internal/proxyengine/xray.go` (`Probe` implementation)
- Create: `internal/proxyengine/probe.go` (`compareExitIP` helper + direct-baseline fetch)
- Create: `internal/proxyengine/probe_test.go` (httptest-backed: fake ip-echo, assert proxied≠direct logic)
- Modify: `cmd/proxy.go` (`proxyTestCmd`: real tunnel proof, `--json`)

**Interfaces:**
- Produces:

```go
type ProbeResult struct {
	DirectIP  string        `json:"directIP"`
	ProxiedIP string        `json:"proxiedIP"`
	Tunneled  bool          `json:"tunneled"`
	Latency   time.Duration `json:"latencyMs"`
}
func (e *XrayEngine) Probe(cfg config.Config) (ProbeResult, error)
```

- [ ] **Step 1: Failing unit test** for the pure compare logic (no docker): two `httptest` servers act as direct vs proxied ip-echo; assert `Tunneled = (proxied != direct)`:

```go
func TestExitIPCompare(t *testing.T) {
	direct := "203.0.113.7"
	if !tunneled(direct, "198.51.100.9") { t.Errorf("different IPs => tunneled") }
	if tunneled(direct, direct) { t.Errorf("same IP => not tunneled") }
}
```

- [ ] **Step 2: Run — expect FAIL** (`tunneled` undefined).

- [ ] **Step 3: Implement `probe.go`** — `func tunneled(direct, proxied string) bool { return direct != "" && proxied != "" && direct != proxied }`; `fetchDirectIP(ctx)` does a plain `http.Get("https://ifconfig.me")` from the host (baseline); `fetchProxiedIP(cfg)` runs `docker.ProxyExec(cfg, "curl","-s","--max-time","5","https://ifconfig.me")` (proxy's egress = tunnel). `Probe` assembles `ProbeResult`. Trim/validate IP strings.

- [ ] **Step 4: Implement `proxyTestCmd`** — load engine, require container running, run `Probe`, render: print DirectIP, ProxiedIP, Tunneled (✓/✗), latency; exit non-zero when `!Tunneled`; honor `--json`.

- [ ] **Step 5: Run unit tests — expect PASS** + `go test ./...`.

- [ ] **Step 6: Commit**

```bash
git add internal/proxyengine cmd
git commit -m "feat(proxy): real 'ws proxy test' proves tunneling via exit-IP comparison"
```

---

### Task 8: `ws proxy doctor`

Ordered, fail-fast diagnostic with per-step ✓/✗, remediation, non-zero exit on first hard failure, `--json`.

**Files:**
- Create: `cmd/proxy_doctor.go` (`proxyDoctorCmd` + check runner)
- Create: `internal/docker/diagnose.go` (helpers: `DefaultRouteOf(container)`, `NetworkSubnet`, reuse `ProxyStatus`/`ProxyCheck`/`ProxyConnectedContainers`)
- Create: `cmd/proxy_doctor_test.go` (table test of the check-runner with injected fakes — no real docker)
- Modify: `cmd/proxy.go` (register `proxyDoctorCmd`)
- Modify: `internal/proxyengine` (expose cert-pin helper `LeafCertSHA256(host, port)` used by the protocol-sanity check to print the observed pin — TCP-TLS dial; document QUIC caveat)

**Interfaces:**
- Produces: `type Check struct { Name string; Run func() CheckOutcome }`; `type CheckOutcome struct { OK bool; Detail string; Fix string }`. The runner stops at the first `!OK` hard check and returns its index for the exit code.

- [ ] **Step 1: Failing test** — the runner stops at the first failing hard check and reports it:

```go
func TestDoctorStopsAtFirstFailure(t *testing.T) {
	checks := []Check{
		{Name: "a", Run: func() CheckOutcome { return CheckOutcome{OK: true} }},
		{Name: "b", Run: func() CheckOutcome { return CheckOutcome{OK: false, Fix: "do x"} }},
		{Name: "c", Run: func() CheckOutcome { t.Fatal("must not run after b"); return CheckOutcome{} }},
	}
	res := runChecks(checks)
	if res.FailedAt != 1 || res.OK { t.Fatalf("got %+v", res) }
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement `runChecks`** + the ordered check list (§spec 5.1.F): docker reachable → image present → `xray -test` active profile → container running+healthy → network+subnet → dev-container default route → real egress (TCP exit-IP via `Engine.Probe`, UDP best-effort) → protocol sanity (hy2: print observed leaf sha256 via `LeafCertSHA256`, note pin match; vless: serverName/shortId present). Each check returns `CheckOutcome` with a `Fix` hint. Render ✓/✗ lines; `--json` emits the full list; exit `FailedAt+1` (or 0).

- [ ] **Step 4: Implement docker helpers** — `DefaultRouteOf` runs `docker exec <ws> ip route show default` and parses the `via` IP; `NetworkSubnet` reads `NetworkInspect(...).IPAM.Config[0].Subnet`.

- [ ] **Step 5: Run tests — expect PASS** + `go test ./...`.

- [ ] **Step 6: Commit**

```bash
git add cmd internal/docker internal/proxyengine
git commit -m "feat(proxy): add 'ws proxy doctor' ordered diagnostic"
```

---

### Task 9: Docs + version-pin corrections

**Files:**
- Modify: `README.md` (proxy section: trust model, pinning, port-hopping, `doctor`/`test`)
- Modify: `docs/proxy-profiles.md` (TLS trust, `pinSHA256`, port-hopping, `doctor`/`test`; remove the "Insecure" wording)
- Modify: any code comment citing `>= v26.1.13` → exact-pin rationale (grep `v26.1.13`)

**Interfaces:** none.

- [ ] **Step 1: Grep + fix version claims**

Run: `grep -rn "v26.1.13" --include='*.go' --include='*.md'`
Replace each with the exact-`v26.2.6`-pin rationale (Global Constraints).

- [ ] **Step 2: Rewrite the proxy docs** — document: no `allowInsecure`; `?pinSHA256=<hex|base64>` for self-signed; port-hopping `host:base,RANGE` + `?hopInterval=&up=&down=&congestion=`; `ws proxy doctor`; `ws proxy test` (exit-IP proof). Keep secrets masked in examples.

- [ ] **Step 3: Build docs sanity** — `go test ./...` (doc-embedded examples if any) + manual read.

- [ ] **Step 4: Commit**

```bash
git add README.md docs cmd internal
git commit -m "docs(proxy): document cert pinning, port-hopping, doctor/test; fix version-pin claims"
```

---

## Self-review notes

- Spec coverage: trust model (T2), port-hopping/brutal (T3), perms (T4), unknown-field guard (T5 golden+keys), engine seam (T6), real test (T7), doctor (T8), neutral core (T1), docs/version (T9). TPROXY/Dockerfile/inbound-sockopt/e2e/runbook are **Plan 2** (deliberately deferred to the container plan).
- The cert-pin auto-extraction over QUIC is deferred (spec §9); Task 8 prints the observed leaf sha256 via a TCP-TLS dial best-effort and documents the QUIC caveat + `openssl` one-liner.
- Type consistency: `Config.PinSHA256`, `Config.HopPorts/HopInterval/Up/Down/Congestion`, `proxyengine.{Engine,Profile,ProbeResult,XrayEngine,Default}`, `xrayconf.*` used consistently across tasks.

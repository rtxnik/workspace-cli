# ws proxy — production maturation design

Date: 2026-06-20
Status: approved (owner decisions locked, see §9)
Repos touched: `workspace-cli` (primary), `dotfiles` (proxy container assets)

## 1. Context & problem

`ws proxy` runs a single shared `dev-proxy` container (Ubuntu + xray-core, pinned
`v26.2.6`) on a Docker bridge network `ws-proxy` (172.28.0.0/16, proxy at
172.28.0.2). Dev containers join that network and default-route through the proxy;
inside the proxy, iptables redirects egress into an xray `dokodemo-door` transparent
inbound, which tunnels it to a remote server via a `vless` or `hysteria2` outbound.

A `hysteria2` profile type was added previously but **never verified against a live
container**. Source-level research against xray-core `v26.2.6` (not docs/memory)
surfaced that the feature is not merely unverified — it is **broken for the common
self-hosted case** and the surrounding proxy has real production gaps in correctness,
security, and verifiability.

A hard process constraint shapes this design: **the engineer cannot run Docker**
(it lives in a different environment); the **human operator runs the live network
test**. Therefore the deliverable must make live verification *turnkey* — a
diagnostic command with a clear pass/fail, a tunnel-proving test, an integration
suite that runs only where Docker exists, and an operator runbook.

## 2. Goals / non-goals

**Goals**

- Make the `hysteria2` path **provably working** and **turnkey-verifiable** by the operator.
- Treat security as first-class: no plaintext-on-disk secrets, no `allowInsecure`, verified supply chain, fail-closed transport.
- Correct transparent proxying for **UDP/QUIC**, not only TCP.
- Lay a foundation for growth: a thin pluggable **engine boundary** and a protocol-agnostic config core.
- Tidy the proxy subsystem while we are in it (the `internal/vless`-owns-shared-types smell).

**Non-goals**

- Migrating off xray-core (to sing-box or a native hysteria2 client) — explicitly rejected, see §4.
- Building a second engine implementation now (the seam is reserved, not filled).
- Multi-node / load-balanced hysteria2 (`init --add` for hy2 stays unsupported).

## 3. Verified facts (xray-core v26.2.6 source, 2026-06-20)

These are settled against tag-pinned source (`infra/conf/transport_internet.go`,
`infra/conf/hysteria.go`, `infra/conf/serial/loader.go`), corroborated by an
adversarial second pass. They are load-bearing for every decision below.

1. **The current hysteria2 JSON shape is correct for v26.2.6.** Outbound
   `protocol:"hysteria"`; `settings.version=2` **and** `hysteriaSettings.version=2`
   are both required (`Build()` errors otherwise); `auth` lives in
   `streamSettings.hysteriaSettings.auth`; salamander obfs is
   `streamSettings.finalmask.udp = [{"type":"salamander","settings":{"password":...}}]`.
   Salamander must be under `.udp` (`Mask.Build()` errors under `.tcp`).
2. **`allowInsecure:true` is a hard error since 2026-06-01.** `TLSConfig.Build()`
   returns `PrintRemovedFeatureError("allowInsecure","pinnedPeerCertSha256")` when
   `time.Now()` is after 2026-06-01. Today is after that date. `Build()` runs during
   `xray run -test`, so any profile that sets `allowInsecure:true` fails `-test` and
   runtime. A self-signed hysteria2 endpoint (the typical self-hosted case) is
   therefore **impossible to use today** without certificate pinning. Emitting
   `allowInsecure:false` is harmless (the error branch is gated on the value being true).
3. **Certificate pinning field is `tlsSettings.pinnedPeerCertSha256`** (not
   `pinnedPeerCertificateChainSha256`). `verifyPeerCertByName` / `verifyPeerCertInNames`
   also exist. Encoding: base64 of `sha256(DER(leaf cert))`, comma-separated for a list.
4. **Port-hopping and brutal are supported.** `hysteriaSettings.udphop = {"port":"<range
   string e.g. 20000-50000 or comma list>","interval":<int ≥5>}`; `hysteriaSettings.congestion`
   accepts `reno|bbr|brutal|force-brutal`; `hysteriaSettings.up`/`down` are unit-suffixed
   bandwidth strings (≥ 65536 Bps when set). The code's "udphop not supported" parse-then-drop
   is a self-imposed limitation, not an engine limitation.
5. **Pin must be exactly `v26.2.6`.** The feature exists from `v26.1.13`, but `v26.1.13`
   uses an **incompatible** obfs schema (`streamSettings.udpmasks`, a flat array) that was
   renamed to `finalmask` by `v26.2.6`. Advertising `>= v26.1.13` spans two incompatible
   schemas. (Tags `v26.1.12`/`v26.1.22` do not exist.)
6. **The xray JSON loader does not `DisallowUnknownFields`.** A misnamed `streamSettings`
   key is silently ignored — e.g. a typo'd `finalmask` yields a tunnel with **no
   obfuscation** that still passes `xray run -test` and the `curl ifconfig.me` healthcheck
   against any non-obfs-enforcing server. `-test` is necessary but **not sufficient**.
7. **Transparent UDP requires TPROXY, not REDIRECT.** `nat REDIRECT` preserves the
   original destination for TCP (`SO_ORIGINAL_DST`) but not for UDP. Correct transparent
   UDP needs `mangle` TPROXY (destination preserved, recovered via `IP_RECVORIGDSTADDR`
   on an `IP_TRANSPARENT` socket) + policy routing (`ip rule add fwmark 1 lookup 100`;
   `ip route add local default dev lo table 100`) + dokodemo-door `streamSettings.sockopt.tproxy="tproxy"`.
   The current UDP/QUIC path drops/leaks.
8. **Exit-IP comparison proves tunneling.** Proxied exit IP ≠ direct-baseline exit IP ⇒
   traffic is tunneled; equal to the host ISP IP ⇒ not carried / leaking. This is the
   canonical reliable proof and the basis of the new `ws proxy test`.

## 4. Architecture

### 4.1 Engine: keep xray-core, pin `v26.2.6`, add a thin seam

xray-core is the only single binary that serves both `vless`/REALITY and `hysteria2`
on the existing dokodemo-door pipeline; the pinned line already provides udphop and
finalmask-salamander (closing the only real pro-migration argument); sing-box churns
its config format and a native hysteria2 client is single-protocol (would shatter the
single-shared-proxy topology). **Decision: stay on xray-core.**

Introduce a minimal, documented seam so a future backend is swappable without rewiring
the CLI:

```go
// internal/proxyengine
type Engine interface {
    // BuildConfig renders the engine-specific config for a parsed profile.
    BuildConfig(p Profile) ([]byte, error)
    // Validate runs the engine's own config check inside the container (xray run -test).
    Validate(cfg config.Config, profileName string) error
    // Probe performs a real egress request through the running proxy and returns the
    // observed exit IP + latency (proves tunneling).
    Probe(cfg config.Config) (ProbeResult, error)
}
```

Keep it thin: one interface, one `xrayEngine` implementation, plus a test fake. Do not
build a second engine this iteration. The interface mostly formalizes seams that already
exist (`generateProfileConfig` → BuildConfig, `ValidateProfile` → Validate) and adds
`Probe` (new).

### 4.2 Neutral config core (refactor / tidy-up)

The neutral xray-core config types (`XrayConfig`, `Inbound`, `Outbound`,
`RoutingConfig`, `AssembleConfig`, `WriteConfig`) currently live in `internal/vless`,
and `internal/hysteria2` imports `internal/vless` to reach them — a naming smell that
makes the proxy core look vless-specific.

**Move the neutral types into a protocol-agnostic package `internal/xrayconf`.**
`internal/vless` keeps only vless/REALITY parsing + outbound construction;
`internal/hysteria2` keeps only hysteria2 parsing + outbound construction; both produce
into `internal/xrayconf`. `internal/xray` (profile management) imports `internal/xrayconf`.
No import cycles (`xrayconf` imports nothing project-internal).

This is a behavior-preserving, bounded rename/move (acceptance gate: `go build` + full
existing test suite green, byte-identical generated configs for unchanged inputs).

### 4.3 Two-repo split

- **`workspace-cli`** (Go): trust model, port-hopping/brutal, engine seam, neutral-core
  refactor, `0600`/`0700` perms, our-side strict validation, `ws proxy doctor`, real
  `ws proxy test`, e2e harness, exact version pin, docs. One self-contained PR.
- **`dotfiles`** (`dot_config/workspaces/profiles/proxy/`): `entrypoint.sh` TPROXY
  migration, `Dockerfile` checksum verification. A separate coordinated PR.

The two are coupled at exactly one point: the dokodemo-door inbound's
`sockopt.tproxy="tproxy"` is emitted by `workspace-cli` (`xrayconf.AssembleConfig`) and
only works once `entrypoint.sh` sets up TPROXY. They must ship together. (The
`config.json.example` in dotfiles also carries the inbound and is updated in lockstep.)

## 5. Component changes

### 5.1 workspace-cli

**A. TLS trust model (security, unblock).** Only the hysteria2 builder currently emits
`allowInsecure` (vless `tls`/`reality` do not). Replace that unconditional
`allowInsecure` with the owner-chosen model: standard CA verification by default;
`pinnedPeerCertSha256` for self-signed / private-CA. Pin support is added to the shared
`tlsSettings` path so vless `tls` can opt in too, but vless behavior is otherwise unchanged.
- Parser accepts a pin from the URI (`pinSHA256`/`pin-sha256` query param) and stores it.
- Config emits `tlsSettings.pinnedPeerCertSha256` when a pin is present. **Never emits
  `allowInsecure`.** A URI carrying `insecure=1`/`allowInsecure=1` is accepted but the
  flag is dropped with a warning that points the operator at pinning + `ws proxy doctor`.
- Format care: hysteria2 URIs conventionally express `pinSHA256` as hex-with-colons,
  while xray expects base64(sha256(DER)). Normalize on input and unit-test both forms.
- `ws proxy doctor` prints the **observed leaf cert sha256 in xray's base64 form** so
  pinning a self-signed endpoint is turnkey; docs also give the `openssl` one-liner.

**B. Port-hopping + brutal + bandwidth (functionality, owner-requested).**
- Parser keeps port-hopping ranges (stop dropping them) and parses optional
  `up`/`down`/`congestion`/`hopInterval` params.
- Config emits `hysteriaSettings.udphop {port,interval}` when ranges are present, and
  `up`/`down`/`congestion` when set, with the validity bounds from §3.4 enforced before
  write (so a bad value is a clear CLI error, not an opaque `xray -test` failure).

**C. Engine seam + neutral-core refactor.** §4.1, §4.2.

**D. Secret-at-rest.** Profile files `0600`, the xray config/profiles dirs `0700`.
Audit every `os.WriteFile(..., 0o644)` / `MkdirAll(..., 0o755)` on the xray tree
(`AddProfile`, `xrayconf.WriteConfig`, `RegenerateProfile`, `MigrateLegacy`,
`setXrayLogLevel`) and tighten. The container bind stays `:ro`.

**E. Our-side strict validation (defense for §3.6).** Because xray silently drops
unknown fields, add a generation-time guard: re-decode each generated config with
`json.Decoder.DisallowUnknownFields()` against the known struct set, so a typo in *our*
emitter is caught by `go test` (golden + strict round-trip), not silently in production.

**F. `ws proxy doctor` (verifiability — keystone for the operator).** New command:
ordered, fail-fast, each step a clear ✓/✗ with remediation; non-zero exit on first hard
failure; `--json` for automation. Steps:
1. Docker daemon reachable.
2. Proxy image present.
3. Active profile config valid (`xray run -test`).
4. Container running **and** `.State.Health.Status == healthy` (not merely running).
5. `ws-proxy` network + `172.28.0.0/16` subnet present.
6. Dev-container default route → `172.28.0.2` (inspect a connected workspace container).
7. **Real egress**: a TCP probe and a UDP/QUIC probe through the proxy; report exit IP +
   latency.
8. Protocol sanity: hysteria2 → TLS reachable, salamander target reached, pin/port-hop
   parsed and printed; vless/reality → serverName/shortId present.

**G. Real `ws proxy test` (verifiability).** Replace the health-only no-op with a tunnel
proof: request the same ip-echo endpoint **directly** (baseline) and **through the proxy**,
assert `proxied_ip != direct_ip`, print both + latency. Must hit a salamander-enforcing
target so a silently-obfs-disabled tunnel (§3.6) fails loudly. `--json` supported.

**H. e2e harness + runbook.** A `//go:build docker_e2e` suite (env-guarded) that builds
the image, ups the proxy, and runs doctor/test — so `go test ./...` stays green on the
engineer's box and the full suite runs only where Docker exists. Plus a short operator
runbook (`docs/proxy-runbook.md`): build → `ws proxy up` → `ws proxy doctor` →
`ws proxy test` (expect distinct exit IP + obfs target reached) → teardown.

**I. Version pin + docs.** Keep the image `ARG XRAY_VERSION=v26.2.6`; correct every
`>= v26.1.13` claim in code comments/docs to the exact-pin rationale (§3.5). Update
`README.md` + `docs/proxy-profiles.md` for the trust model, port-hopping, doctor/test,
and the runbook.

### 5.2 dotfiles (`dot_config/workspaces/profiles/proxy/`)

**J. `entrypoint.sh` → TPROXY.** Move the `XRAY` chain to `-t mangle`; keep the
private-net `RETURN`s; `-p tcp -j TPROXY --on-port 12345 --tproxy-mark 1` and
`-p udp -j TPROXY --on-port 12345 --tproxy-mark 1`; add `ip rule add fwmark 1 lookup 100`
and `ip route add local default dev lo table 100`; rework loop prevention from
`OUTPUT` uid-owner exclusion to a mark-based bypass of xray's own tunneled packets.
**Fail-closed**: unmatched / tunnel-down traffic is DROPPED, not RETURNed; the `freedom`
`direct` outbound stays scoped to private nets only. Keep idempotent re-apply on restart.

**K. `Dockerfile` supply chain.** After downloading `Xray-linux-${ARCH}.zip`, fetch the
release `...dgst` (sha256) for the **pinned** `v26.2.6` and verify before `unzip`; abort
on mismatch. (Container caps are already minimal — only `NET_ADMIN` is added by
`workspace-cli`'s `ContainerCreate` — so no cap change is needed.)

**L. `config.json.example` / `AssembleConfig` inbound.** Add `streamSettings.sockopt.tproxy="tproxy"`
to the dokodemo-door inbound, in lockstep with J.

## 6. Security model (first-class)

| Concern | Decision |
|---|---|
| Secrets at rest | Profile files `0600`, dirs `0700`; container bind `:ro`. |
| TLS trust | Standard CA verification; `pinnedPeerCertSha256` for self-signed. `allowInsecure` never emitted. |
| Silent misconfig | Generation-time `DisallowUnknownFields` round-trip guard (our side). |
| Supply chain | Dockerfile verifies xray-core zip sha256 against the pinned version. |
| Transport leaks | TPROXY + fail-closed DROP for unmatched/tunnel-down; `direct` scoped to private nets. |
| Least privilege | Container keeps only `NET_ADMIN` (already minimal). |
| Secret display | Existing masking (`show`/`list`) preserved; `doctor`/`test`/`--json` never print secrets. |

## 7. Verification design

`xray run -test` validates `Build()` (catches `version!=2`, bad `udphop`, now
`allowInsecure`) but never opens the tunnel and cannot see an unknown-field-dropped obfs
or a salamander interop timeout. Live proof is mandatory and is delivered as:

- `ws proxy doctor` — ordered turnkey diagnostic ending in a real TCP + UDP/QUIC egress
  check with exit-IP/latency (§5.1.F).
- `ws proxy test` — exit-IP comparison tunnel proof against a salamander-enforcing target
  (§5.1.G).
- `docker_e2e` build-tagged suite + operator runbook (§5.1.H).

The operator's `ws proxy doctor` + `ws proxy test` run on a TPROXY-enabled host **is the
definition of done** for the live-correctness claims — not `go test`, which cannot reach
Docker or the network from the engineer's environment.

## 8. Testing strategy

- TDD throughout (repo norm): failing test first, then implement.
- Unit: parser (pin formats, port-hop ranges, bandwidth/congestion, insecure→warn-drop),
  config emit (golden files for hy2 with/without obfs/pin/udphop; vless unchanged),
  strict round-trip guard, perms (`0600`/`0700`), trust-model matrix.
- Integration (existing `//go:build integration`): add hy2 `xray -test` against a real
  container where Docker exists.
- e2e (`//go:build docker_e2e`): build → up → doctor → test.
- Gates unchanged: `go vet`, `go test -race`, coverage ≥30%, golangci-lint v2.12.2.

## 9. Decisions locked (owner) + deferred

**Locked (2026-06-20):**
- Scope: **full production push across both repos** (workspace-cli + dotfiles).
- TLS trust: **standard verification + `pinnedPeerCertSha256` for self-signed**; no `allowInsecure`.
- hysteria2 features: **include port-hopping (udphop) + brutal congestion (+ bandwidth)** this iteration.
- Engine: keep xray-core pinned `v26.2.6`; add a thin engine seam (no second engine now).
- TPROXY: applies to **both TCP and UDP** (uniform mangle path); documented REDIRECT-TCP +
  UDP-fail-closed fallback if the operator's kernel rejects all-TPROXY.
- Spec/plan live in `workspace-cli/docs/superpowers/` for a self-contained deliverable
  (deviates from the workspace-meta convention deliberately, for clean single-repo merge).

**Deferred (with trade-off):**
- `dns-out` tunneled DNS (DNS-leak window via Docker `127.0.0.11` remains; private-scoped
  short-term; do it after TPROXY is proven to avoid coupling two risky changes).
- Automatic QUIC-based pin extraction in the CLI (avoids a `quic-go` dependency; the
  `openssl` one-liner + doctor guidance is turnkey enough for v1).
- A second engine backend (seam reserved; pure cost until a concrete need appears).

## 10. Risks & honest uncertainties

- **TPROXY-in-container is host/kernel-dependent** (`IP_TRANSPARENT`, `lo`-table local
  route inside the netns). Only the operator's live `doctor` run can confirm it. Primary
  iteration risk; "done" gates on that run, with the REDIRECT-TCP + UDP-fail-closed fallback.
- **Salamander server interop** has documented timeout reports (xray issue #5712). The
  schema is correct; interop with *the operator's specific* endpoint must be proven by
  `ws proxy test`.
- **Pin format** (hysteria hex-with-colons vs xray base64(DER-sha256)) must be normalized
  and unit-tested, or pinning silently fails.

## 11. Acceptance criteria

1. A default hysteria2 profile (public-CA endpoint) and a pinned self-signed profile both
   pass `xray run -test` on v26.2.6; no config ever emits `allowInsecure`.
2. Port-hopping and brutal are emitted correctly (golden tests) when requested.
3. Profile files are `0600`, dirs `0700`.
4. `ws proxy doctor` and `ws proxy test` exist, exit non-zero on failure, support `--json`,
   and `ws proxy test` proves tunneling by exit-IP comparison.
5. `go test ./...` is green on a Docker-less host; the `docker_e2e` suite and runbook exist.
6. `entrypoint.sh` uses TPROXY for TCP+UDP with fail-closed DROP; `Dockerfile` verifies the
   xray-core zip checksum; the inbound carries `sockopt.tproxy`.
7. The neutral config core lives in `internal/xrayconf`; `internal/vless` no longer owns
   shared types; generated configs for unchanged inputs are byte-identical.
8. README + proxy-profiles + runbook document the new model.
9. **Operator sign-off**: on a real host, `ws proxy doctor` is all-green and `ws proxy test`
   shows a distinct exit IP through a salamander-enforcing hysteria2 endpoint.

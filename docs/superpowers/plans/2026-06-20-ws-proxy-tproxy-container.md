# ws proxy production — Container / TPROXY Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make transparent UDP/QUIC egress from dev containers actually work (mangle TPROXY), verify the proxy image's supply chain, couple the dokodemo-door `sockopt.tproxy` change with a config migration, and ship the operator-verified e2e harness + runbook.

**Architecture:** Hybrid iptables: dev-container (forwarded) traffic via `mangle` PREROUTING **TPROXY** for both TCP and UDP; the proxy container's own egress (incl. healthcheck) stays on `nat` OUTPUT **REDIRECT** (TCP), so the healthcheck still proves the tunnel. dokodemo-door gains `sockopt.tproxy="tproxy"`. Fail-closed: TPROXY to a non-listening socket drops; `direct` outbound stays private-only.

**Tech Stack:** bash + iptables (dotfiles proxy image), Go (workspace-cli inbound + migration + e2e), Docker.

**Repos:** `workspace-cli` (Tasks 1, 4) and `dotfiles` (Tasks 2, 3). **Run the dotfiles tasks in a separate isolated worktree/branch of the `dotfiles` repo** (set up with `superpowers:using-git-worktrees` at execution time) so other agents' dotfiles work is undisturbed.

## Global Constraints

- Inherits Plan-1 Global Constraints (exact `v26.2.6` pin, no `allowInsecure`, gates, TDD, no AI attribution).
- TPROXY mark `1`, route table `100`, dokodemo-door port `12345`.
- This plan changes `xrayconf.AssembleConfig` output (adds inbound `streamSettings.sockopt.tproxy`), so **Plan-1 golden files must be regenerated here** (note in Task 1).
- **Definition of done is the operator's live run** (`ws proxy doctor` all-green + `ws proxy test` distinct exit IP on a salamander-enforcing endpoint), NOT `go test` — the engineer cannot run Docker. Document the REDIRECT-TCP + UDP-fail-closed fallback if the operator's kernel rejects TPROXY.

---

### Task 1: dokodemo-door `sockopt.tproxy` + profile config migration (workspace-cli)

**Files:**
- Modify: `internal/xrayconf/config.go` (`Inbound` gains `StreamSettings`; new `InboundStream`/`Sockopt`; `AssembleConfig` sets `sockopt.tproxy="tproxy"`)
- Regenerate: `internal/xrayconf/testdata/assemble_vless.golden.json`, `internal/hysteria2/testdata/*.golden.json` (Plan-1 goldens — inbound now carries streamSettings)
- Create: `internal/xray/upgrade.go` (`UpgradeProfileInbounds(cfg)` — rewrite each profile's inbound to the canonical one, preserve outbounds+routing)
- Create: `internal/xray/upgrade_test.go`
- Create: `cmd/proxy_upgrade.go` (`proxy upgrade-config` command)
- Modify: `cmd/proxy.go` (register), `cmd/proxy_doctor.go` (a doctor check: active profile inbound has `sockopt.tproxy`; Fix → `ws proxy upgrade-config`)

**Interfaces:**
- Produces: `xrayconf.InboundStream{Sockopt *Sockopt}`, `xrayconf.Sockopt{Tproxy string json:"tproxy,omitempty"}`; `func xray.UpgradeProfileInbounds(cfg config.Config) (changed int, err error)`.

- [ ] **Step 1: Failing test** — `AssembleConfig` inbound carries `sockopt.tproxy`:

```go
func TestAssembleInboundTproxy(t *testing.T) {
	xc := AssembleConfig(Outbound{Tag: "proxy-1", Protocol: "vless", Settings: []byte("{}")})
	if xc.Inbounds[0].StreamSettings == nil || xc.Inbounds[0].StreamSettings.Sockopt == nil ||
		xc.Inbounds[0].StreamSettings.Sockopt.Tproxy != "tproxy" {
		t.Fatalf("inbound missing sockopt.tproxy: %+v", xc.Inbounds[0].StreamSettings)
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement** — add the structs + set them in `AssembleConfig`'s inbound:

```go
type InboundStream struct {
	Sockopt *Sockopt `json:"sockopt,omitempty"`
}
type Sockopt struct {
	Tproxy string `json:"tproxy,omitempty"`
}
// in Inbound: StreamSettings *InboundStream `json:"streamSettings,omitempty"`
// in AssembleConfig inbound: StreamSettings: &InboundStream{Sockopt: &Sockopt{Tproxy: "tproxy"}},
```

- [ ] **Step 4: Regenerate Plan-1 goldens** — re-emit and eyeball the 5 golden files (they now contain `"streamSettings":{"sockopt":{"tproxy":"tproxy"}}` on the inbound); commit the updated goldens.

- [ ] **Step 5: Migration** — `UpgradeProfileInbounds` loads each `*.json` in `cfg.XrayProfilesDir`, replaces its `Inbounds` with `AssembleConfig(<its first outbound>).Inbounds` (preserve `Outbounds`+`Routing`), writes back `0600`. Test: an old profile (no sockopt) gains it; outbounds/routing untouched.

```go
func TestUpgradeInbounds(t *testing.T) {
	// write a profile with a legacy inbound (no streamSettings), run upgrade, assert sockopt.tproxy present and outbound preserved.
}
```

- [ ] **Step 6: Command** — `proxy upgrade-config`: runs `UpgradeProfileInbounds`, prints count, suggests `ws proxy recreate`. Add the doctor check.

- [ ] **Step 7: Run `go test ./... && go vet ./... && golangci-lint run` — PASS.**

- [ ] **Step 8: Commit**

```bash
git add internal/xrayconf internal/hysteria2 internal/xray cmd
git commit -m "feat(proxy): add inbound sockopt.tproxy + 'upgrade-config' migration"
```

---

### Task 2: `entrypoint.sh` → hybrid TPROXY (dotfiles)

**Repo:** `dotfiles`. **File:** `dot_config/workspaces/profiles/proxy/entrypoint.sh` (full rewrite).

> No unit test (shell + kernel). Verified by `ws proxy doctor`/`test` on the operator's host (Task 4). Validate locally with `bash -n entrypoint.sh` (syntax) and `shellcheck` if available.

- [ ] **Step 1: Rewrite `entrypoint.sh`** to the hybrid recipe:

```bash
#!/usr/bin/env bash
# =============================================================================
# Transparent proxy entrypoint (hybrid TPROXY)
#  - dev-container (forwarded) traffic : mangle PREROUTING TPROXY (tcp + udp)
#  - proxy's own egress (healthcheck)  : nat OUTPUT REDIRECT (tcp), uid xray bypassed
# Fail-closed: TPROXY to a non-listening xray drops; 'direct' outbound is private-only.
# =============================================================================
set -euo pipefail

MARK=1
TABLE=100
PORT=12345
PRIVATE=(10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8 169.254.0.0/16 224.0.0.0/4)

# --- idempotent cleanup (Docker reuses the netns across restarts) ---
while iptables -t mangle -D PREROUTING -j XRAY 2>/dev/null; do :; done
while iptables -t nat    -D OUTPUT    -j XRAY_OUT 2>/dev/null; do :; done
iptables -t mangle -F XRAY 2>/dev/null || true; iptables -t mangle -X XRAY 2>/dev/null || true
iptables -t nat    -F XRAY_OUT 2>/dev/null || true; iptables -t nat -X XRAY_OUT 2>/dev/null || true
ip rule del fwmark $MARK lookup $TABLE 2>/dev/null || true
ip route flush table $TABLE 2>/dev/null || true

# --- policy routing: deliver marked, foreign-destined packets to the local TPROXY socket ---
ip rule add fwmark $MARK lookup $TABLE
ip route add local default dev lo table $TABLE

# --- mangle PREROUTING: TPROXY forwarded dev-container traffic ---
iptables -t mangle -N XRAY
for net in "${PRIVATE[@]}"; do iptables -t mangle -A XRAY -d "$net" -j RETURN; done
iptables -t mangle -A XRAY -p tcp -j TPROXY --on-port $PORT --tproxy-mark $MARK
iptables -t mangle -A XRAY -p udp -j TPROXY --on-port $PORT --tproxy-mark $MARK
iptables -t mangle -A PREROUTING -j XRAY

# --- nat OUTPUT: REDIRECT the proxy's OWN tcp egress (healthcheck), skip xray's tunnel ---
iptables -t nat -N XRAY_OUT
iptables -t nat -A XRAY_OUT -m owner --uid-owner xray -j RETURN
for net in "${PRIVATE[@]}"; do iptables -t nat -A XRAY_OUT -d "$net" -j RETURN; done
iptables -t nat -A XRAY_OUT -p tcp -j REDIRECT --to-ports $PORT
iptables -t nat -A OUTPUT -p tcp -j XRAY_OUT

# --- verify ---
iptables -t mangle -L XRAY -n >/dev/null 2>&1 && iptables -t nat -L XRAY_OUT -n >/dev/null 2>&1 \
  && echo "iptables applied (mangle PREROUTING TPROXY + nat OUTPUT REDIRECT)" \
  || { echo "ERROR: iptables rules failed to apply" >&2; exit 1; }

# --- validate + start xray ---
if ! su -s /bin/sh xray -c "xray run -test -c /etc/xray/config.json" >/dev/null 2>&1; then
    echo "ERROR: xray config validation failed" >&2
    su -s /bin/sh xray -c "xray run -test -c /etc/xray/config.json" >&2
    exit 1
fi
echo "xray config validated"
exec su -s /bin/sh xray -c "xray run -c /etc/xray/config.json"
```

- [ ] **Step 2: Syntax check**

Run: `bash -n dot_config/workspaces/profiles/proxy/entrypoint.sh` (and `shellcheck` if present)
Expected: no errors.

- [ ] **Step 3: Commit (dotfiles worktree)**

```bash
git add dot_config/workspaces/profiles/proxy/entrypoint.sh
git commit -m "feat(proxy): hybrid TPROXY entrypoint for transparent UDP/QUIC"
```

---

### Task 3: `Dockerfile` supply-chain verification (dotfiles)

**Repo:** `dotfiles`. **File:** `dot_config/workspaces/profiles/proxy/Dockerfile`.

- [ ] **Step 1: Add checksum verification** of the xray-core zip against the pinned release's published `dgst` (XTLS publishes `Xray-linux-<arch>.zip.dgst` containing the SHA256). Replace the download block:

```dockerfile
    && curl -fsSL \
       "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VERSION}/Xray-linux-${XRAY_ARCH}.zip" \
       -o /tmp/xray.zip \
    && curl -fsSL \
       "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VERSION}/Xray-linux-${XRAY_ARCH}.zip.dgst" \
       -o /tmp/xray.zip.dgst \
    && EXPECT=$(grep -i 'sha256' /tmp/xray.zip.dgst | head -1 | awk '{print $NF}') \
    && ACTUAL=$(sha256sum /tmp/xray.zip | awk '{print $1}') \
    && [ -n "$EXPECT" ] && [ "$EXPECT" = "$ACTUAL" ] \
       || { echo "xray-core checksum mismatch: expected=$EXPECT actual=$ACTUAL" >&2; exit 1; } \
    && unzip /tmp/xray.zip -d /tmp/xray \
```

(Confirm the `.dgst` line format for `v26.2.6` during execution; the `sha256` line's last field is the hex digest. If the artifact name differs, adapt — fail closed on mismatch either way.)

- [ ] **Step 2: Build sanity** (operator/CI, where docker exists): `docker build` the proxy image succeeds; a tampered zip would abort. Note in the runbook.

- [ ] **Step 3: Commit (dotfiles worktree)**

```bash
git add dot_config/workspaces/profiles/proxy/Dockerfile
git commit -m "fix(proxy): verify xray-core release checksum in proxy image build"
```

---

### Task 4: e2e harness (`//go:build docker_e2e`) + operator runbook (workspace-cli)

**Files:**
- Create: `cmd/proxy_e2e_test.go` (`//go:build docker_e2e`)
- Create: `docs/proxy-runbook.md`
- Modify: `Makefile` (add `test-e2e` target), `README.md` (link the runbook)

- [ ] **Step 1: e2e test** — guarded by build tag + a running daemon; builds image, ups proxy, runs `doctor` + `test`, asserts distinct exit IP:

```go
//go:build docker_e2e

package cmd
// TestProxyE2E: requires docker + an initialized hysteria2 'primary' profile.
// Skips (not fails) when docker or the profile is absent. Builds the image,
// ups the proxy, runs `ws proxy doctor` (expect all-green) and `ws proxy test`
// (expect Tunneled==true). This is the operator's live gate.
```

- [ ] **Step 2: `Makefile`**

```make
test-e2e:
	go test -tags docker_e2e ./cmd/ -run TestProxyE2E -v
```

- [ ] **Step 3: Runbook** (`docs/proxy-runbook.md`) — the 6-step operator procedure:

```
1. ws proxy rebuild --force        # build image (checksum-verified) + recreate with new entrypoint
2. ws proxy upgrade-config         # add sockopt.tproxy to existing profiles
3. ws proxy recreate               # apply the upgraded active profile
4. ws proxy doctor                 # expect ALL green incl. real TCP+UDP egress + exit IP
5. ws proxy test                   # expect Tunneled=yes, ProxiedIP != DirectIP, on a salamander endpoint
6. (rollback) if TPROXY misbehaves on this kernel: revert entrypoint to REDIRECT-TCP + leave UDP fail-closed
```
Include the `openssl` one-liner to compute a self-signed cert pin:
`openssl x509 -in cert.pem -outform DER | openssl dgst -sha256 -binary | base64`.

- [ ] **Step 4: `go test ./...` stays green** (e2e is excluded without the tag).

Run: `go test ./... && go build -tags docker_e2e ./...`
Expected: PASS (e2e compiles under the tag, does not run untagged).

- [ ] **Step 5: Commit**

```bash
git add cmd Makefile docs README.md
git commit -m "test(proxy): docker_e2e harness + operator runbook"
```

---

## Self-review notes

- Spec coverage: TPROXY (T2), inbound sockopt + migration (T1), Dockerfile checksum (T3), e2e+runbook (T4). DNS-out + QUIC auto-pin remain deferred per spec §9.
- Cross-repo: T1/T4 in `workspace-cli`; T2/T3 in a separate `dotfiles` worktree → a second PR. They ship together (the inbound sockopt is inert until the TPROXY entrypoint is live; document the rebuild order in the runbook).
- Type consistency: `xrayconf.{InboundStream,Sockopt}`, `xray.UpgradeProfileInbounds` used consistently; goldens regenerated to match the new inbound.
- Honest risk: TPROXY-in-container is host/kernel-dependent; the runbook carries the REDIRECT-TCP + UDP-fail-closed fallback. Operator live run is the gate.
```

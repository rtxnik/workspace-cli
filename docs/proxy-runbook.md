# Proxy Operator Runbook

Six-step procedure for rebuilding, upgrading, and validating the `dev-proxy` container after a code or config change. Run every step in order; stop and roll back on any failure.

---

## Prerequisites

- Docker running and reachable (`docker info` succeeds).
- A hysteria2 `primary` profile initialised (`ws proxy profile list` shows `primary` as active).
- The `ws` binary on `$PATH` (or `./ws` from the repo root after `make build`).

---

## 6-Step Procedure

### Step 1 — Rebuild the image

```bash
ws proxy rebuild --force
```

Performs a checksum-verified build of the proxy Docker image and recreates the container immediately. `--force` skips the "connected workspaces" confirmation prompt. Wait for the health-check step to complete before continuing.

### Step 2 — Upgrade on-disk profiles to TPROXY inbound

```bash
ws proxy upgrade-config
```

Rewrites the `inbounds` section of every `*.json` profile in `~/.config/xray/profiles/` to add `streamSettings.sockopt.tproxy: "tproxy"` (required for TPROXY-mode transparent proxying). Profiles that already carry the field are skipped — the command is idempotent.

### Step 3 — Recreate the container (apply the upgraded active profile)

```bash
ws proxy recreate
```

Tears down and re-creates the `dev-proxy` container so it picks up the upgraded inbound config written in Step 2. Faster than a full rebuild; preserves the existing image.

### Step 4 — Ordered diagnostic (expect all green)

```bash
ws proxy doctor
```

Runs the ordered, fail-fast diagnostic chain:

1. docker reachable
2. proxy image present
3. active profile valid (xray -test)
4. proxy container running and healthy
5. ws-proxy network + subnet
6. dev-container default route via proxy
7. self-egress (proxy tunnel exit-IP) — TCP
8. protocol sanity
9. inbound sockopt.tproxy (advisory)

All nine checks must be green before proceeding. If any hard check fails, the command exits non-zero and prints a remediation hint. Fix the issue and re-run.

For machine-readable output (CI or automated gates): `ws proxy doctor --json`.

### Step 5 — Tunnel proof (expect Tunneled=yes)

```bash
ws proxy test
```

Compares the direct exit IP (plain HTTP) against the proxied exit IP (via `docker exec curl` inside `dev-proxy`) and prints:

```
Direct IP   <your-host-ip>
Proxied IP  <tunnel-exit-ip>
Tunneled    ✓
```

`ProxiedIP` must differ from `DirectIP`. If they are identical the tunnel is not carrying traffic — check the profile and container logs (`ws proxy logs`).

For machine-readable output: `ws proxy test --json` → `{"directIP":"…","proxiedIP":"…","tunneled":true,"latency":"…"}`.

### Step 6 — Rollback note

If TPROXY misbehaves on the operator's kernel (e.g. the container lacks `CAP_NET_ADMIN` or the host kernel does not support TPROXY in Docker), fall back to REDIRECT-TCP mode:

- Revert the entrypoint to use iptables REDIRECT instead of TPROXY (edit `dotfiles`; rebuild with `ws proxy rebuild --force`).
- Leave UDP fail-closed (no UDP forwarding rule) until TPROXY is confirmed working.
- The `ws proxy doctor` advisory check (step 9, "inbound sockopt.tproxy") will report the missing field; that is expected in REDIRECT mode.

---

## Computing a Self-Signed Cert Pin

When the upstream endpoint uses a self-signed certificate, compute the leaf cert SHA-256 pin and pass it as `?pinSHA256=<value>` in the hysteria2 URI:

```bash
openssl x509 -in cert.pem -outform DER | openssl dgst -sha256 -binary | base64
```

Alternatively, `ws proxy doctor` prints the observed leaf SHA-256 when it can dial the endpoint over TCP-TLS (hysteria2 is QUIC/UDP, so a TCP refusal is expected on QUIC-only endpoints — the observed value from the doctor output is the one to pin).

To inspect the currently configured pin:

```bash
ws proxy profile show primary
```

---

## Quick Reference

| Command | Purpose |
|---------|---------|
| `ws proxy rebuild --force` | Build image + recreate container |
| `ws proxy upgrade-config` | Add TPROXY sockopt to existing profiles |
| `ws proxy recreate` | Recreate container (no image rebuild) |
| `ws proxy doctor` | Ordered fail-fast diagnostic |
| `ws proxy doctor --json` | Machine-readable diagnostic |
| `ws proxy test` | Tunnel proof (exit-IP comparison) |
| `ws proxy test --json` | Machine-readable tunnel proof |
| `ws proxy logs` | Container log tail |
| `ws proxy status` | Container state, health, uptime |
| `ws proxy profile list` | List profiles (active marked) |
| `ws proxy profile show <name>` | Show profile (masked) |
| `make test-e2e` | Docker e2e harness (CI gate) |

---

## CI datapath coverage (SP-4)

The `datapath` CI job builds the proxy image (`ws-proxy:gate`) from the pinned
dotfiles recipe and runs:

- **Red-line preconditions (T2–T5) + dead-socket detection (T13)** — `scripts/ci/datapath-gate.sh`.
- **H6 golden validity** — `xray -test` over every committed golden config and a
  generated VLESS URI matrix, using the image's own pinned xray-core
  (version-correct by construction): `make test-golden-xray`.
- **H7 profile-lifecycle integration** — `make test-integration-proxy`.

**Flow-only boundary.** Without the `WS_TEST_ENDPOINT` repository secret the job
runs in *flow-only* mode: preconditions, the forwarding-leg structure, T13, and
H6 semantic validity are all enforced, but the **exit-IP value comparison**
(`TestProxyE2E`, `Tunneled == true`) is skipped — it needs a real upstream.
Setting `WS_TEST_ENDPOINT` promotes the job to *strict* mode and runs the full
tunnel assertion. The gate prints a loud banner when running flow-only.

(The `WS_TEST_ENDPOINT` repository secret is exposed to the job as the
`WS_TEST_URI` environment variable — `WS_TEST_URI: ${{ secrets.WS_TEST_ENDPOINT }}`
in `ci.yml` — which is what `datapath-gate.sh` and the strict-mode step read.)

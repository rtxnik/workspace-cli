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

`ws proxy upgrade-config` also repairs already-stored profiles written by an
older `ws`: a cert pin in base64/uppercase/colon form is re-encoded to lowercase
hex, and an `h2` transport is migrated to XHTTP stream-one — the two shapes a
current xray-core rejects. Run it once after upgrading `ws` if an existing
profile fails `ws proxy doctor`'s `xray -test` check.

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
openssl x509 -in cert.pem -outform DER | openssl dgst -sha256 -binary | xxd -p -c 256
```

This prints the pin as lowercase hex — the same form `ws` writes into the config (`tlsSettings.pinnedPeerCertSha256`, which xray v26 hex-decodes) and `ws proxy doctor` prints. `?pinSHA256=` also accepts colon-separated hex (`AA:BB:…`), uppercase hex, or base64; all normalize to lowercase hex internally.

Alternatively, `ws proxy doctor` prints the observed leaf SHA-256 (lowercase hex) when it can dial the endpoint over TCP-TLS (hysteria2 is QUIC/UDP, so a TCP refusal is expected on QUIC-only endpoints — the observed value from the doctor output is the one to pin).

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
- **H6 golden validity** — `xray -test` over all five committed goldens
  (hysteria2 `base`/`obfs`/`pin`/`udphop` and `assemble_vless`), a generated
  7-transport VLESS URI matrix (tcp-reality, tcp-http-header, ws-tls, grpc,
  httpupgrade, xhttp, and `h2`), and 2 repaired-legacy configs (a pre-fix
  base64 cert pin repaired to lowercase hex, and a pre-fix `h2` transport
  repaired to XHTTP stream-one) — 14 configs total; valid REALITY key — using
  the image's own pinned xray-core (version-correct by construction):
  `make test-golden-xray`. ws emits the cert pin as lowercase hex (xray v26
  hex-decodes `tlsSettings.pinnedPeerCertSha256`), a parsed `type=h2` URI is
  migrated to an XHTTP stream-one config at generation, and `upgrade-config`
  repairs legacy stored profiles to the same shapes — all three are semantically
  validated here.
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

---

## Bump proxy base image digest (D6-06)

The proxy base image is pinned by **manifest-list (index) digest** in
`dotfiles: dot_config/workspaces/profiles/proxy/Dockerfile`
(`FROM ubuntu:24.04@sha256:…`). Pinning gives reproducible, tamper-evident builds
but does not auto-follow upstream Ubuntu security patches — refresh it manually.

The datapath gate builds the staged dotfiles recipe **directly** (`docker build`),
so bumping the digest is only exercised end-to-end when `DOTFILES_REF` points at
the dotfiles commit that carries the new digest. Keep two refs equal to that
commit: `.github/workflows/ci.yml` `DOTFILES_REF` **and** the ws-embedded pin
`internal/proxyrecipe/recipe.lock -> dotfiles_ref`.

To refresh:

1. Resolve the current `ubuntu:24.04` index digest (no docker needed):

   ```sh
   TOKEN=$(curl -s "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/ubuntu:pull" | jq -r .token)
   curl -sI -H "Authorization: Bearer $TOKEN" \
     -H "Accept: application/vnd.oci.image.index.v1+json" \
     -H "Accept: application/vnd.docker.distribution.manifest.list.v2+json" \
     "https://registry-1.docker.io/v2/library/ubuntu/manifests/24.04" \
     | grep -i docker-content-digest
   ```

2. Update the digest in dotfiles `proxy/Dockerfile`; open and merge that PR — note
   the merged dotfiles commit `X`.
3. In workspace-cli, against a checkout of dotfiles at `X`:

   ```sh
   make pin-recipe RECIPE_DIR=<dotfiles@X>/dot_config/workspaces/profiles/proxy DOTFILES_REF=X
   ```

   then set `DOTFILES_REF: "X"` in `.github/workflows/ci.yml`; open and merge.

`make pin-recipe` re-hashes the **whole** recipe (Dockerfile *and* entrypoint.sh),
so the regenerated `recipe.lock` reflects every recipe change since the last pin,
not just the digest. The two-PR order (dotfiles first, then workspace-cli) applies
to every bump: the pin and the gate ref must point at a **merged** dotfiles commit.

---

## Transactional recreate (`ws proxy recreate` / `ws proxy rebuild` / `ws proxy update`)

`ws proxy recreate` is a Level-B transaction. The previous container is preserved
as `<ProxyContainer>-backup` (e.g. `ws-proxy-backup`) until the new container is
verified healthy, then the backup is dropped (COMMIT) or restored (ROLLBACK). A
failed recreate -- missing image, broken on-disk xray config, IP conflict, or an
unhealthy new container -- never leaves a dead proxy.

Because the proxy uses a fixed static IP, only one container can hold it at a
time, so there is a brief connectivity gap during the IP swap (disconnect backup
-> create new on the same IP). This is expected and lasts only the create/start
window.

Read the final line to know which container is serving the proxy IP:

- `recreate committed -- NEW proxy now serving <IP>` : success, new container live.
- `... -- rolled back ...` : the previous (OLD) container was restored and is serving.
- `... the restored proxy is also unhealthy ... run 'ws proxy doctor'` : the on-disk
  xray config is likely broken; both old and new fail health. Fix the config, then
  retry.
- `CRITICAL: ... the proxy is DOWN.` : automatic rollback also failed. Run the
  printed `Manual recovery` commands in order (only the still-required steps are
  printed), for example:

      docker rm -f ws-proxy
      docker network connect --ip <IP> ws-proxy ws-proxy-backup
      docker start ws-proxy-backup
      docker rename ws-proxy-backup ws-proxy

If a recreate was interrupted (e.g. the machine rebooted mid-swap), the next
`ws proxy recreate` self-heals: a leftover backup with no primary is restored
("recovered an interrupted recreate: restored OLD proxy from the backup"); a
leftover backup alongside a present primary is treated as garbage and removed.

### CI-only datapath acceptance (NOT verified by the unit suite)

The IP-reservation precondition cannot be exercised without a real Docker daemon
and is therefore verified only by the privileged CI `datapath` gate plus an owner
host check, never by the unit tests:

- `NetworkDisconnect` actually frees the static `cfg.ProxyIP`, and a subsequent
  `NetworkConnect` with the same `IPAMConfig.IPv4Address` re-reserves it.
- A deliberately-broken-config recreate leaves the previous proxy still serving
  `cfg.ProxyIP` (rollback proven end-to-end on real containers).
- The daemon does not auto-resurrect a force-removed `UnlessStopped` container
  mid-rollback.

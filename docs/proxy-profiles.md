# Proxy profiles

Manage multiple xray VLESS and Hysteria2 configurations as named profiles and switch between them with one command. Each profile lives in `~/.config/xray/profiles/<name>.json`; the active one is selected via a symlink at `~/.config/xray/config.json`.

## TL;DR

```bash
ws proxy profile add primary    'vless://<uri>'   # store a profile
ws proxy profile add secondary  'vless://<uri>'   # add another
ws proxy profile list                              # see them all
ws proxy profile use secondary                     # switch (auto-reloads proxy)
ws proxy profile current                           # which one is active right now?
```

That is the entire daily flow. Everything below is reference for setup, edge cases, and recovery.

## How it fits together

```
~/.config/xray/
├── config.json      → profiles/primary.json    # symlink (active profile)
└── profiles/
    ├── primary.json
    └── secondary.json
```

The xray container (`dev-proxy`) mounts the whole `~/.config/xray/` directory read-only at `/etc/xray/`. Profile files are visible inside the container at `/etc/xray/profiles/<name>.json`, so `xray run -test` can validate any profile before you switch to it.

## First-time setup

```bash
ws proxy check                              # verify Docker + image + config
ws proxy init 'vless://<your-first-uri>'    # generates the first profile (VLESS)
ws proxy init 'hysteria2://<auth>@host:443' # or a Hysteria2 URI
ws proxy up                                 # start the container
ws proxy profile current                    # should print: primary
```

`ws proxy init` accepts a `vless://`, `hysteria2://`, or `hy2://` URI, creates `~/.config/xray/profiles/primary.json`, and points the symlink at it. From there everything goes through `ws proxy profile`.

If you already had the legacy single-file layout (`~/.config/xray/config.json` as a regular file), the first `ws proxy profile` invocation migrates it transparently — your old file becomes `profiles/primary.json` and a symlink replaces the original. Use `--no-migrate` on any subcommand to opt out for one invocation.

## Adding profiles

```bash
ws proxy profile add secondary 'vless://uuid@host:443?type=tcp&security=reality&...'
ws proxy profile add hy2-exit  'hysteria2://<auth>@host:443?obfs=salamander&obfs-password=<pw>&sni=host&alpn=h3'
```

The `hy2://` scheme is accepted as an alias for `hysteria2://`.

Hysteria2 profiles carry an `auth` password and optional Salamander obfuscation (`obfs`/`obfs-password`). The proxy engine (xray-core, pinned to exactly v26.2.6) runs Hysteria2 natively — no rebuild needed. The image is pinned to v26.2.6 rather than a `>= v26.1.13` floor because v26.1.13 used an incompatible obfuscation schema (`udpmasks` flat array) that was renamed to `finalmask` (object) in v26.2.6.

What happens:
- URI is parsed into a full xray config (inbounds + outbound + routing rules).
- File written to `~/.config/xray/profiles/<name>.json`.
- Active profile is **not** changed — only added.

Profile name must match `^[a-z0-9_-]{1,32}$`. The names `config` and `tmp` are reserved and rejected.

## Switching profiles

```bash
ws proxy profile use secondary
```

This is one atomic operation:

1. Pre-flight checks the proxy container is running and the bind mount is healthy.
2. `xray run -test` validates `secondary.json` inside the container. If it fails — abort, symlink untouched.
3. Atomic symlink swap (`os.Symlink` + `os.Rename`, no `ln -sfn`).
4. Container restart so xray re-reads the new config.
5. Health check waits up to 15s for the container to come back healthy.

Total downtime: ~1–2 seconds. SSH/TCP keepalives ride it out. If anything between steps 3–5 fails, the symlink is left at the new profile and you get a structured error explaining what to do next. **There is no automatic rollback** — the operator decides recovery.

### Advanced: skip the reload

```bash
ws proxy profile use secondary --no-reload
```

Swaps the symlink only, does not touch the container. xray keeps running the previous profile in memory until you run `ws proxy restart` yourself. Useful for scripted batch operations, staged switches, or tests.

## Inspecting profiles

```bash
ws proxy profile list                # table view (active marked with *)
ws proxy profile list --json         # machine-readable
ws proxy profile show secondary      # masked (UUID, REALITY private key hidden)
ws proxy profile show secondary --reveal   # unmasked
ws proxy profile show hy2-exit       # masked (Auth, ObfsPass hidden)
ws proxy profile show hy2-exit --reveal    # unmasked
ws proxy profile current             # just the name of the active one
```

For VLESS profiles, `show` prints `UUID` (and `PublicKey`/`ShortID`/`SpiderX` for REALITY). For Hysteria2 profiles, `show` prints `Protocol`, `Auth`, `Obfs`, `ObfsPass`, and `Pinned` (yes/no + masked pin value) instead; secrets are masked to `****` unless `--reveal` is set. There is no `Insecure` field — xray-core v26.2.6 removed `allowInsecure`; use `?pinSHA256=` for self-signed endpoints (see [TLS trust and cert pinning](#tls-trust-and-cert-pinning) below). The `list` command's `TRANSPORT` column shows `hysteria` for hy2 profiles (and omits the `UUID` column for those rows in `--json` output).

## TLS trust and cert pinning

xray-core v26.2.6 does not support `allowInsecure` — using `?insecure=1` in a URI is silently ignored (a warning is printed instead). All Hysteria2 connections use standard TLS verification.

For endpoints with a **self-signed or private CA certificate**, pass the leaf certificate's SHA-256 fingerprint as `?pinSHA256=`:

```bash
# hex-colon form (openssl / most tools)
ws proxy profile add hy2-exit 'hysteria2://<auth>@host:443?sni=host&pinSHA256=AA:BB:CC:...'
# bare hex or base64 are also accepted
ws proxy profile add hy2-exit 'hysteria2://<auth>@host:443?sni=host&pinSHA256=<base64>'
```

`ws proxy doctor` prints the observed leaf SHA-256 of the endpoint as lowercase hex without colons (via a best-effort TCP-TLS probe) — the same form `ws` stores in the config. Because Hysteria2 is QUIC/UDP, a TCP refusal is normal — in that case the doctor prints a caveat and marks the check as inconclusive rather than failed. To get the fingerprint from outside the tool, you can use (the colon-hex it prints is accepted verbatim by `?pinSHA256=`):

```bash
openssl s_client -connect host:443 2>/dev/null </dev/null \
  | openssl x509 -fingerprint -sha256 -noout
```

## Port-hopping

Hysteria2 supports UDP port-hopping to evade per-port blocking. Append a comma-separated port spec to the base port in the URI:

```bash
# base port 443, also hop across 5000-6000
ws proxy profile add hy2-hop 'hysteria2://<auth>@host:443,5000-6000?sni=host&hopInterval=30&up=50mbps&down=200mbps&congestion=brutal'
```

| Query parameter | Default | Description |
|-----------------|---------|-------------|
| `hopInterval` | `30` | Seconds between port hops (≥5) |
| `up` | — | Upstream bandwidth hint (e.g. `50mbps`) |
| `down` | — | Downstream bandwidth hint (e.g. `200mbps`) |
| `congestion` | — | Congestion algorithm: `reno`, `bbr`, `brutal`, `force-brutal` |

## Diagnostics: `ws proxy doctor`

`ws proxy doctor` runs an ordered, fail-fast diagnostic of the full proxy stack:

1. Docker reachable
2. Proxy image present
3. Active profile valid (`xray -test`)
4. Datapath contract (image ↔ profile)
5. Proxy container running and healthy
6. TPROXY preconditions
7. ws-proxy network + subnet
8. Dev-container default route via proxy
9. Self-egress (proxy tunnel exit-IP)
10. Forwarding datapath (dev-container exit-IP)
11. Protocol sanity (hy2: leaf cert sha256 vs pin; VLESS: inbound socket)
12. Inbound `sockopt.tproxy` (advisory)

It stops at the first hard failure and prints a remediation hint plus exits non-zero. Use `--json` for a machine-readable report.

```bash
ws proxy doctor            # human-readable, fail-fast
ws proxy doctor --json     # full JSON result list
```

## Verifying the tunnel: `ws proxy test`

`ws proxy test` proves the tunnel is active by comparing the direct exit IP to the proxied exit IP:

```bash
ws proxy test              # human-readable (✓/✗ + latency)
ws proxy test --json       # JSON: {"directIP", "proxiedIP", "tunneled", "latencyMs"}
```

Exits 0 when `tunneled=true` (the two IPs differ), exits 1 otherwise.

## Editing routing rules

Routing rules live inside each profile file. If you edit them in the active profile and want every other profile to inherit the same routing block:

```bash
ws proxy profile regenerate secondary
```

This copies the `routing.rules` from the active profile into `secondary.json`. Outbounds and inbounds stay untouched.

## Removing profiles

```bash
ws proxy profile rm old-backup
```

Refuses to remove the active profile. Asks for confirmation; use `--force` to skip.

## Container lifecycle

| Command | What it does | When to use |
|---------|--------------|-------------|
| `ws proxy up` | Start (or resume) the container | Daily — first thing after host boot |
| `ws proxy down` | Stop the container | Free up resources, or before a host reboot |
| `ws proxy restart` | Stop + start same container | After manual config edits, or recovery |
| `ws proxy recreate` | Remove + create new container | After image / env / network changes |
| `ws proxy rebuild` | Rebuild image + recreate | After bumping xray-core version |
| `ws proxy status` | Show running state, health, uptime, image |
| `ws proxy logs` | Tail container logs |
| `ws proxy test` | End-to-end connectivity test through proxy |
| `ws proxy debug on\|off` | Toggle verbose xray logging |

## Recovery

### "Switch failed, what now?"

`ws proxy profile use <name>` failed after the symlink was already swapped. The error message will say so explicitly and suggest:

```bash
ws proxy profile use <previous>     # back out
ws proxy restart                    # retry the reload
docker logs dev-proxy --tail 50     # see what xray actually complained about
```

There is **no auto-rollback** — both because rolling back the symlink without checking *why* the new config failed risks masking real config errors, and because the operator may want to keep the new config visible on disk while investigating.

### "Switch exits 1 with no output"

If you see this, your `ws` binary is from before `f649399` (2026-05-13). Rebuild from `main`:

```bash
cd <wherever workspace-cli lives>
git pull && go install .
```

### "ws proxy profile use says 'legacy single-file bind mount'"

The container was created before the bind mount was widened to whole-directory. Recreate it:

```bash
ws proxy rebuild --force
```

This is a one-time migration per host.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `XRAY_CONFIG` | `~/.config/xray/config.json` | Active-profile symlink target |
| `XRAY_PROFILES_DIR` | `~/.config/xray/profiles` | Profile storage directory |
| `WS_PROXY_CONTAINER` | `dev-proxy` | Container name |
| `WS_PROXY_IMAGE` | `devpod-proxy` | Image name |

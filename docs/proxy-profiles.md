# Proxy profiles

Manage multiple xray VLESS configurations as named profiles and switch between them with one command. Each profile lives in `~/.config/xray/profiles/<name>.json`; the active one is selected via a symlink at `~/.config/xray/config.json`.

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
ws proxy init 'vless://<your-first-uri>'    # generates the first profile
ws proxy up                                 # start the container
ws proxy profile current                    # should print: primary
```

`ws proxy init` creates `~/.config/xray/profiles/primary.json` and points the symlink at it. From there everything goes through `ws proxy profile`.

If you already had the legacy single-file layout (`~/.config/xray/config.json` as a regular file), the first `ws proxy profile` invocation migrates it transparently — your old file becomes `profiles/primary.json` and a symlink replaces the original. Use `--no-migrate` on any subcommand to opt out for one invocation.

## Adding profiles

```bash
ws proxy profile add secondary 'vless://uuid@host:443?type=tcp&security=reality&...'
```

What happens:
- VLESS URI is parsed into a full xray config (inbounds + outbound + routing rules copied from the currently-active profile, so kill-switch rules etc. stay consistent).
- File written to `~/.config/xray/profiles/secondary.json`.
- Active profile is **not** changed — only added.

Profile name must match `^[a-z0-9_-]{1,32}$`. A handful of reserved names (`config`, `default`, etc.) are rejected.

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
ws proxy profile current             # just the name of the active one
```

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

## Deprecated

```bash
ws proxy init 'vless://...' --add        # deprecated since Phase 22
```

The `--add` flag still works but prints a stderr warning. Use `ws proxy profile add <name> 'vless://...'` instead. Removal scheduled for the next minor release.

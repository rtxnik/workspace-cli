# workspace-cli

Workspace manager CLI (`ws` binary) for [DevPod](https://devpod.sh/) environments with transparent proxy support.

## Install

### Verified install (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/rtxnik/workspace-cli/main/scripts/install.sh | sh
```

The installer always verifies the SHA-256 of the downloaded archive against the release `checksums.txt`. If `minisign` is installed, it also verifies the minisign signature over `checksums.txt` against the release signing key embedded in the script. To make signature verification mandatory:

```bash
curl -fsSL https://raw.githubusercontent.com/rtxnik/workspace-cli/main/scripts/install.sh -o install.sh
sh install.sh --require-signature
```

Overrides: `WS_VERSION` installs a specific tag; `PREFIX` sets the install root (default `/usr/local`).

### Manual verification

Every release ships `checksums.txt`, its minisign signature `checksums.txt.minisig`, and a Syft SBOM (`*.sbom.json`) per artifact:

```bash
minisign -Vm checksums.txt -P RWS9SKDBxXVQRL27p1aOVmdoSffl83dqJqKtnwDO6IqEMpdoRf+AMDGL
sha256sum -c --ignore-missing checksums.txt
```

The signing key and reporting policy are published in [SECURITY.md](SECURITY.md).

### From source

```bash
go install github.com/rtxnik/workspace-cli@latest
```

## Usage

### Workspaces

```bash
ws new myproject go          # Create workspace with Go profile
ws new myproject --proxy     # Create with proxy networking
ws list                      # List all workspaces
ws start myproject           # Start workspace
ws ssh myproject             # SSH into workspace
ws code myproject            # Open in VS Code
ws stop myproject            # Stop workspace
ws delete myproject          # Delete workspace
ws detect .                  # Detect profile for current directory
ws status                    # Show workspace health across all repositories
```

### Profiles

```bash
ws profiles                              # List available profiles
ws profile-create myprofile --image ...  # Create custom profile
ws profile-delete myprofile              # Delete custom profile
```

### Proxy

VLESS and Hysteria2 proxy via xray-core (pinned to v26.2.6) in a Docker container. Daily flow is profile-based — see [docs/proxy-profiles.md](docs/proxy-profiles.md) for the full guide. For rebuild, upgrade, and validation after a code change, follow the [operator runbook](docs/proxy-runbook.md).

```bash
# Setup
ws proxy init <uri>               # First-time: generate primary profile + symlink layout
ws proxy check                    # Verify Docker + image + config prerequisites

# Container lifecycle
ws proxy up                       # Start container
ws proxy down                     # Stop container
ws proxy restart                  # Stop + start (re-reads config on disk)
ws proxy recreate                 # Remove + create new (after image/env/network changes)
ws proxy rebuild                  # Rebuild image + recreate
ws proxy status                   # Show running state, health, uptime; per-workspace route protection (--json: workspaceProtection)
ws proxy logs                     # Tail container logs
ws proxy test                     # Prove tunnel is active (compares direct vs proxied exit IP)
ws proxy test --json              # Machine-readable {"directIP","proxiedIP","tunneled","latencyMs","dns","dnsExitIP"} — exits 1 on a DNS leak
ws proxy doctor                   # Ordered fail-fast diagnostic of the full proxy stack
ws proxy doctor --json            # Machine-readable diagnostic report
ws proxy debug on|off             # Toggle verbose xray logging
ws proxy update [version]         # Update xray-core version
ws proxy fix-routes               # Restore default routes in workspace containers (e.g. after reboot)
ws proxy rebuild --allow-drift    # Rebuild even if the recipe differs from the pinned known-good recipe

# Profiles (VLESS and Hysteria2 configurations)
ws proxy profile add <name> <uri>     # Store a new profile from a VLESS or Hysteria2 URI
ws proxy profile list                 # List all profiles (active marked)
ws proxy profile use <name>           # Switch active profile + reload proxy (atomic)
ws proxy profile current              # Print currently active profile name
ws proxy profile show <name>          # Show profile (masked; --reveal to unmask)
ws proxy profile regenerate <name>    # Copy routing rules from active into <name>
ws proxy profile rm <name>            # Remove a profile (refuses active)
```

#### Hysteria2 URI parameters

```
hysteria2://<auth>@<host>:<port>[,<hop-ranges>]?sni=<host>&pinSHA256=<sha256>&obfs=salamander&obfs-password=<pw>&hopInterval=30&up=50mbps&down=200mbps&congestion=brutal
```

Key parameters:

| Parameter | Notes |
|-----------|-------|
| `pinSHA256` | Leaf cert SHA-256 (hex-colon, bare hex, or base64) for self-signed endpoints; stored and compared as lowercase hex. `ws proxy doctor` prints the observed value (lowercase hex). `allowInsecure`/`insecure` is not supported on xray-core v26.2.6. |
| `<port>,<ranges>` | Port-hopping: e.g. `443,5000-6000`. Requires `hopInterval` (default 30 s, min 5). |
| `congestion` | `reno` \| `bbr` \| `brutal` \| `force-brutal` |

### Vault

`ws vault` provides CLI access to a private knowledge-base backend. The command reference lives in [docs/vault-commands.md](docs/vault-commands.md); these commands are not usable without access to that backend.

## Configuration

Environment variables (with defaults):

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKSPACES_DIR` | `~/workspaces` | Workspace root directory |
| `PROFILES_DIR` | `~/.config/workspaces/profiles` | Profile definitions |
| `SHARED_DIR` | `~/.config/workspaces/shared` | Shared scripts |
| `XRAY_CONFIG` | `~/.config/xray/config.json` | Active-profile symlink target |
| `XRAY_PROFILES_DIR` | `~/.config/xray/profiles` | Profile storage directory |
| `WS_PROXY_CONTAINER` | `dev-proxy` | Proxy container name |
| `WS_PROXY_IMAGE` | `devpod-proxy` | Proxy Docker image name |

## Build

```bash
make build                   # Build binary
make test                    # Run tests
make vet                     # Static analysis
make lint                    # golangci-lint
make install                 # Install to GOPATH/bin
make test-e2e                # Docker e2e harness (requires Docker + a live primary profile)
make test-golden-xray        # Golden xray-config validation suite
make test-integration-proxy  # Profile-lifecycle integration suite
make pin-recipe              # Re-pin the known-good proxy recipe
```

## License

MIT

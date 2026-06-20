# workspace-cli

Workspace manager CLI (`ws` binary) for [DevPod](https://devpod.sh/) environments with transparent proxy support.

## Install

```bash
# From source
go install github.com/rtxnik/workspace-cli@latest

# Or download from releases
# https://github.com/rtxnik/workspace-cli/releases
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
ws proxy status                   # Show running state, health, uptime
ws proxy logs                     # Tail container logs
ws proxy test                     # Prove tunnel is active (compares direct vs proxied exit IP)
ws proxy test --json              # Machine-readable {DirectIP, ProxiedIP, Tunneled, Latency}
ws proxy doctor                   # Ordered fail-fast diagnostic of the full proxy stack
ws proxy doctor --json            # Machine-readable diagnostic report
ws proxy debug on|off             # Toggle verbose xray logging
ws proxy update [version]         # Update xray-core version

# Profiles (VLESS and Hysteria2 configurations)
ws proxy profile add <name> <uri>     # Store a new profile from a VLESS or Hysteria2 URI
ws proxy profile list                 # List all profiles (active marked)
ws proxy profile use <name>           # Switch active profile + reload proxy (atomic)
ws proxy profile current              # Print currently active profile name
ws proxy profile show <name>          # Show profile (masked; --reveal to unmask)
ws proxy profile regenerate <name>    # Copy routing rules from active into <name>
ws proxy profile rm <name>            # Remove a profile (refuses active)
```

`ws proxy init --add` is deprecated; use `ws proxy profile add` instead.

#### Hysteria2 URI parameters

```
hysteria2://<auth>@<host>:<port>[,<hop-ranges>]?sni=<host>&pinSHA256=<sha256>&obfs=salamander&obfs-password=<pw>&hopInterval=30&up=50mbps&down=200mbps&congestion=brutal
```

Key parameters:

| Parameter | Notes |
|-----------|-------|
| `pinSHA256` | Leaf cert SHA-256 (hex-colon, bare hex, or base64) for self-signed endpoints. `ws proxy doctor` prints the observed value. `allowInsecure`/`insecure` is not supported on xray-core v26.2.6. |
| `<port>,<ranges>` | Port-hopping: e.g. `443,5000-6000`. Requires `hopInterval` (default 30 s, min 5). |
| `congestion` | `reno` \| `bbr` \| `brutal` \| `force-brutal` |

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
make build      # Build binary
make test       # Run tests
make vet        # Static analysis
make lint       # golangci-lint
make install    # Install to GOPATH/bin
make test-e2e   # Docker e2e harness (requires Docker + a live primary profile)
```

## License

MIT

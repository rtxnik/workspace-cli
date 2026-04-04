# ws

Workspace manager CLI for [DevPod](https://devpod.sh/) environments with transparent proxy support.

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

```bash
ws proxy init <vless-uri>         # Generate xray config from VLESS URI
ws proxy init <uri> --add         # Add node to existing config
ws proxy check                    # Verify prerequisites
ws proxy up                       # Start proxy container
ws proxy down                     # Stop proxy container
ws proxy status                   # Show container status
ws proxy logs                     # Show container logs
ws proxy test                     # Test connectivity
ws proxy debug on|off             # Toggle debug logging
ws proxy rebuild                  # Rebuild proxy image
ws proxy update [version]         # Update xray-core version
```

## Configuration

Environment variables (with defaults):

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKSPACES_DIR` | `~/workspaces` | Workspace root directory |
| `PROFILES_DIR` | `~/.config/workspaces/profiles` | Profile definitions |
| `SHARED_DIR` | `~/.config/workspaces/shared` | Shared scripts |
| `XRAY_CONFIG` | `~/.config/xray/config.json` | Proxy config path |
| `WS_PROXY_CONTAINER` | `dev-proxy` | Proxy container name |
| `WS_PROXY_IMAGE` | `devpod-proxy` | Proxy Docker image name |

## Build

```bash
make build    # Build binary
make test     # Run tests
make vet      # Static analysis
make lint     # golangci-lint
make install  # Install to GOPATH/bin
```

## License

MIT

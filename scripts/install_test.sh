#!/bin/sh
# install_test.sh — exercises scripts/install.sh against a local fake "release"
# served via file://. Verifies: (1) a good archive+checksum installs; (2) a
# tampered checksums.txt is rejected. No network and no minisign required.
set -eu

# shellcheck disable=SC1007
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
installer="${here}/install.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
archive="ws_${os}_${arch}.tar.gz"

# Build a fake release dir.
dist="${work}/dist"; mkdir -p "$dist"
printf '#!/bin/sh\necho fake-ws\n' > "${work}/ws"; chmod +x "${work}/ws"
tar -C "$work" -czf "${dist}/${archive}" ws
# shellcheck disable=SC2015
( cd "$dist" && { command -v sha256sum >/dev/null 2>&1 && sha256sum "$archive" || shasum -a 256 "$archive"; } > checksums.txt )

prefix="${work}/root"

# Case 1: good install.
WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="$prefix" sh "$installer"
test -x "${prefix}/bin/ws" || { echo "FAIL: ws not installed"; exit 1; }
echo "PASS: good install"

# Case 2: tampered checksum is rejected.
echo "0000000000000000000000000000000000000000000000000000000000000000  ${archive}" > "${dist}/checksums.txt"
if WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="${work}/root2" sh "$installer" 2>/dev/null; then
    echo "FAIL: tampered checksum was accepted"; exit 1
fi
echo "PASS: tampered checksum rejected"
echo "ALL PASS"

#!/bin/sh
# install_test.sh — exercises scripts/install.sh against a local fake "release"
# served via file://. Verifies: (1) a good archive+checksum installs; (2) a
# tampered checksums.txt is rejected; (3) an upgrade replaces the binary on a
# new inode; (4) --require-signature failures state a reason; (5) a binary that
# cannot execute fails the install. No network and no minisign required.
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

# Restore a good checksums.txt for the upgrade case.
# shellcheck disable=SC2015
( cd "$dist" && { command -v sha256sum >/dev/null 2>&1 && sha256sum "$archive" || shasum -a 256 "$archive"; } > checksums.txt )

# Case 3: upgrading over an existing binary lands on a NEW inode. An in-place
# copy reuses the inode, which leaves a stale per-inode code-signature cache on
# macOS (every exec of the upgraded binary is SIGKILLed).
inode_before=$(ls -i "${prefix}/bin/ws" | awk '{print $1}')
WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="$prefix" sh "$installer"
inode_after=$(ls -i "${prefix}/bin/ws" | awk '{print $1}')
if [ "$inode_before" = "$inode_after" ]; then
    echo "FAIL: upgrade reused the destination inode (in-place overwrite)"; exit 1
fi
echo "PASS: upgrade replaced the binary on a new inode"

# Case 4: --require-signature with no minisig asset fails with a reason, not a
# bare "not available". A stub minisign on PATH exercises the signature-required
# branch without needing the real tool.
stub="${work}/stub"; mkdir -p "$stub"
printf '#!/bin/sh\nexit 0\n' > "${stub}/minisign"; chmod +x "${stub}/minisign"
err="${work}/case4.err"
if PATH="${stub}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="${work}/root4" sh "$installer" --require-signature 2>"$err"; then
    echo "FAIL: missing minisig accepted under --require-signature"; exit 1
fi
if ! grep -Eq 'no checksums\.txt\.minisig asset|could not download checksums\.txt\.minisig' "$err"; then
    echo "FAIL: missing-minisig error does not state a reason:"; cat "$err"; exit 1
fi
echo "PASS: missing minisig under --require-signature fails with a reason"

# Case 5: a binary that installs but cannot execute must fail the install
# (post-install smoke check), not report success.
dist5="${work}/dist5"; mkdir -p "$dist5"
printf '#!/bin/sh\nexit 7\n' > "${work}/ws"; chmod +x "${work}/ws"
tar -C "$work" -czf "${dist5}/${archive}" ws
# shellcheck disable=SC2015
( cd "$dist5" && { command -v sha256sum >/dev/null 2>&1 && sha256sum "$archive" || shasum -a 256 "$archive"; } > checksums.txt )
err5="${work}/case5.err"
if WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist5}" PREFIX="${work}/root5" sh "$installer" 2>"$err5"; then
    echo "FAIL: non-executing binary reported as installed"; exit 1
fi
if ! grep -q 'failed to execute' "$err5"; then
    echo "FAIL: smoke-check failure message missing:"; cat "$err5"; exit 1
fi
echo "PASS: non-executing binary fails the post-install smoke check"
echo "ALL PASS"

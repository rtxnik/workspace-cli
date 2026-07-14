#!/bin/sh
# install_test.sh — exercises scripts/install.sh against a local fake "release".
# Fail-closed default: a valid signature is REQUIRED unless --allow-unsigned.
# A stub minisign on PATH stands in for the real tool (its exit code decides
# verify pass/fail); branches are driven by the .minisig asset's presence, so
# the suite does NOT depend on the runner's real minisign state.
set -eu

# shellcheck disable=SC1007
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
installer="${here}/install.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
archive="ws_${os}_${arch}.tar.gz"

mkgood() { # dir
    d="$1"; mkdir -p "$d"
    printf '#!/bin/sh\necho fake-ws\n' > "${work}/ws"; chmod +x "${work}/ws"
    tar -C "$work" -czf "${d}/${archive}" ws
    # shellcheck disable=SC2015
    ( cd "$d" && { command -v sha256sum >/dev/null 2>&1 && sha256sum "$archive" || shasum -a 256 "$archive"; } > checksums.txt )
}

# Signed dist (has .minisig); unsigned dist (no .minisig).
dist="${work}/dist";       mkgood "$dist";       printf 'fake-sig\n' > "${dist}/checksums.txt.minisig"
distnosig="${work}/nosig"; mkgood "$distnosig"

# Stub minisign that VERIFIES (exit 0) and one that FAILS (exit 1).
okbin="${work}/ok";   mkdir -p "$okbin";  printf '#!/bin/sh\nexit 0\n' > "${okbin}/minisign";  chmod +x "${okbin}/minisign"
badbin="${work}/bad"; mkdir -p "$badbin"; printf '#!/bin/sh\nexit 1\n' > "${badbin}/minisign"; chmod +x "${badbin}/minisign"

# Case 1: default install, verifying minisign + signature present -> OK.
p="${work}/root1"
PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="$p" sh "$installer"
test -x "${p}/bin/ws" || { echo "FAIL: default signed install did not install"; exit 1; }
echo "PASS: default signed install"

# Case 2 (RED for the flip): default, minisign present but NO signature asset, no opt-out -> REFUSE.
e="${work}/c2.err"
if PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${distnosig}" PREFIX="${work}/root2" sh "$installer" 2>"$e"; then
    echo "FAIL: fail-closed default installed without a signature"; exit 1
fi
grep -q 'refusing to install' "$e" || { echo "FAIL: fail-closed refusal missing reason:"; cat "$e"; exit 1; }
echo "PASS: fail-closed default refuses an unsigned release"

# Case 3: --allow-unsigned with no signature -> installs (checksum-only) + warn.
p3="${work}/root3"; e3="${work}/c3.err"
PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${distnosig}" PREFIX="$p3" sh "$installer" --allow-unsigned 2>"$e3"
test -x "${p3}/bin/ws" || { echo "FAIL: --allow-unsigned did not install"; exit 1; }
grep -q 'checksum-level' "$e3" || { echo "FAIL: --allow-unsigned did not warn checksum-level:"; cat "$e3"; exit 1; }
echo "PASS: --allow-unsigned downgrades to checksum-only"

# Case 4: --require-signature accepted as a no-op alias (still installs when signed).
p4="${work}/root4"; o4="${work}/c4.out"
PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="$p4" sh "$installer" --require-signature >"$o4" 2>&1
test -x "${p4}/bin/ws" || { echo "FAIL: --require-signature alias broke a signed install"; exit 1; }
grep -q 'no-op' "$o4" || { echo "FAIL: --require-signature did not report no-op:"; cat "$o4"; exit 1; }
echo "PASS: --require-signature accepted as no-op alias"

# Case 5: PRESENT but INVALID signature is fatal (stub exits 1).
e5="${work}/c5.err"
if PATH="${badbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="${work}/root5" sh "$installer" 2>"$e5"; then
    echo "FAIL: invalid signature accepted"; exit 1
fi
grep -q 'verification of checksums.txt FAILED' "$e5" || { echo "FAIL: invalid-sig message missing:"; cat "$e5"; exit 1; }
echo "PASS: present-but-invalid signature is fatal"

# Case 6: tampered checksum rejected (signature verifies, checksum mismatches).
tamp="${work}/tamp"; mkdir -p "$tamp"; cp "${dist}/${archive}" "$tamp/"; cp "${dist}/checksums.txt.minisig" "$tamp/"
echo "0000000000000000000000000000000000000000000000000000000000000000  ${archive}" > "${tamp}/checksums.txt"
if PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${tamp}" PREFIX="${work}/root6" sh "$installer" 2>/dev/null; then
    echo "FAIL: tampered checksum accepted"; exit 1
fi
echo "PASS: tampered checksum rejected"

# Case 7: upgrade lands on a NEW inode.
# shellcheck disable=SC2012
ib=$(ls -i "${p4}/bin/ws" | awk '{print $1}')
PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${dist}" PREFIX="$p4" sh "$installer" >/dev/null 2>&1
# shellcheck disable=SC2012
ia=$(ls -i "${p4}/bin/ws" | awk '{print $1}')
[ "$ib" != "$ia" ] || { echo "FAIL: upgrade reused inode"; exit 1; }
echo "PASS: upgrade replaced binary on a new inode"

# Case 8: non-executing binary fails the post-install smoke check.
d8="${work}/d8"; mkdir -p "$d8"
printf '#!/bin/sh\nexit 7\n' > "${work}/ws"; chmod +x "${work}/ws"
tar -C "$work" -czf "${d8}/${archive}" ws
# shellcheck disable=SC2015
( cd "$d8" && { command -v sha256sum >/dev/null 2>&1 && sha256sum "$archive" || shasum -a 256 "$archive"; } > checksums.txt )
printf 'fake-sig\n' > "${d8}/checksums.txt.minisig"
e8="${work}/c8.err"
if PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${d8}" PREFIX="${work}/root8" sh "$installer" 2>"$e8"; then
    echo "FAIL: non-executing binary reported installed"; exit 1
fi
grep -q 'failed to execute' "$e8" || { echo "FAIL: smoke-check message missing:"; cat "$e8"; exit 1; }
echo "PASS: non-executing binary fails the smoke check"

# Case 9: signature policy decided BEFORE the archive download.
d9="${work}/d9"; mkdir -p "$d9"; cp "${dist}/checksums.txt" "${d9}/checksums.txt"
e9="${work}/c9.err"
if PATH="${okbin}:${PATH}" WS_VERSION=v0.0.0-test WS_BASE_URL="file://${d9}" PREFIX="${work}/root9" sh "$installer" 2>"$e9"; then
    echo "FAIL: missing signature accepted under fail-closed default"; exit 1
fi
grep -q "failed to download ${archive}" "$e9" && { echo "FAIL: archive fetched before signature policy:"; cat "$e9"; exit 1; }
grep -Eq 'no checksums\.txt\.minisig asset|could not download checksums\.txt\.minisig' "$e9" || { echo "FAIL: missing-minisig reason absent:"; cat "$e9"; exit 1; }
echo "PASS: signature policy decided before the archive download"

echo "ALL PASS"

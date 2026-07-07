#!/bin/sh
# install.sh — POSIX installer for workspace-cli (ws).
#
# Trust model (SP-2 / audit H9):
#   * ALWAYS verifies the archive's SHA-256 against checksums.txt.
#   * If `minisign` is on PATH and checksums.txt.minisig is present, ALSO verifies
#     the signature against the public key embedded below (full chain of trust).
#   * If `minisign` is absent, prints a loud WARN and continues at checksum level
#     — UNLESS --require-signature was passed, in which case it is fatal.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/rtxnik/workspace-cli/main/scripts/install.sh | sh
#   sh install.sh [--require-signature]
#
# Environment overrides:
#   WS_VERSION    install a specific tag (e.g. v0.7.1) instead of latest
#   PREFIX        install root (default /usr/local); binary -> $PREFIX/bin/ws
#   WS_BASE_URL   override the asset base URL. Testing seam AND the offline /
#                 mirror path: download ws_<os>_<arch>.tar.gz, checksums.txt
#                 and checksums.txt.minisig on a connected machine, then
#                 WS_VERSION=vX.Y.Z WS_BASE_URL="file:///path/to/assets" \
#                   sh install.sh --require-signature

set -eu

# --- embedded release-signing public key -----------------------------------
# Minisign public key used to verify release signatures (see SECURITY.md).
RELEASE_PUBKEY="RWS9SKDBxXVQRL27p1aOVmdoSffl83dqJqKtnwDO6IqEMpdoRf+AMDGL"

REPO="rtxnik/workspace-cli"
PREFIX="${PREFIX:-/usr/local}"
REQUIRE_SIGNATURE=0

info() { printf '==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
err()  { printf 'ERROR: %s\n' "$*" >&2; }
die()  { err "$@"; exit 1; }

while [ $# -gt 0 ]; do
    case "$1" in
        --require-signature) REQUIRE_SIGNATURE=1 ;;
        -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) die "unknown argument: $1 (try --help)" ;;
    esac
    shift
done

need() { command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"; }
need uname; need mktemp; need tar; need chmod

# Bounded, retrying downloads: flaky paths to the release CDN must not hang the
# install (no timeout) or kill it on a single transient failure (no retry).
# --retry-all-errors covers connection resets / TLS handshake failures that
# curl's plain --retry does not classify as transient.
if command -v curl >/dev/null 2>&1; then
    DOWNLOAD() { curl -fsSL --retry 3 --retry-delay 1 --retry-all-errors --connect-timeout 10 --max-time 300 "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
    DOWNLOAD() { wget -q --tries=3 --connect-timeout=10 --timeout=300 "$1" -O "$2"; }
else
    die "need curl or wget to download release assets"
fi

if command -v sha256sum >/dev/null 2>&1; then
    SHA256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
    SHA256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
    die "need sha256sum or 'shasum -a 256' to verify the download"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    linux) os=linux ;;
    darwin) os=darwin ;;
    *) die "unsupported OS: $(uname -s)" ;;
esac
arch=$(uname -m)
case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "unsupported architecture: $arch" ;;
esac

version="${WS_VERSION:-}"
if [ -z "$version" ]; then
    info "resolving latest release tag"
    tmp_api=$(mktemp)
    DOWNLOAD "https://api.github.com/repos/${REPO}/releases/latest" "$tmp_api" \
        || die "could not reach the GitHub API (set WS_VERSION to install offline)"
    version=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp_api" | head -n1)
    rm -f "$tmp_api"
    [ -n "$version" ] || die "could not parse latest tag_name"
fi

# ws archives are version-independent: ws_<os>_<arch>.tar.gz
archive="ws_${os}_${arch}.tar.gz"
base="${WS_BASE_URL:-https://github.com/${REPO}/releases/download/${version}}"

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT INT TERM

# Fetch BOTH manifests in one downloader invocation, and BEFORE the archive.
# Throttling middleboxes were observed killing the Nth fresh TLS handshake in
# a burst (retries included) while already-established connections kept
# working — and the standalone signature fetch was exactly that Nth handshake.
# One curl process reuses its connections across URLs, so the tiny .minisig
# rides the same connection as checksums.txt instead of opening a new one.
# Manifest-first also fails --require-signature fast, before the big download.
#
# A signature failure must say WHY: "asset absent" (unsigned release) reads
# very differently from "network failure" (retry / check connectivity).
info "downloading release manifests (${version})"
have_sig=0
sig_reason=""
sig_err="${workdir}/manifests.err"
if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --retry-delay 1 --retry-all-errors --connect-timeout 10 --max-time 300 \
        -o "${workdir}/checksums.txt"         "${base}/checksums.txt" \
        -o "${workdir}/checksums.txt.minisig" "${base}/checksums.txt.minisig" 2>"$sig_err" || true
else
    DOWNLOAD "${base}/checksums.txt"         "${workdir}/checksums.txt"         2>"$sig_err"  || true
    DOWNLOAD "${base}/checksums.txt.minisig" "${workdir}/checksums.txt.minisig" 2>>"$sig_err" || true
fi
[ -s "${workdir}/checksums.txt" ] || die "failed to download checksums.txt ($(tr '\n' ' ' < "$sig_err"))"
if [ -s "${workdir}/checksums.txt.minisig" ]; then
    have_sig=1
elif grep -q '404' "$sig_err" 2>/dev/null; then
    sig_reason="the release has no checksums.txt.minisig asset (HTTP 404)"
else
    sig_reason="could not download checksums.txt.minisig — network failure, retry or check connectivity ($(tr '\n' ' ' < "$sig_err"))"
fi

verify_checksum() {
    expected=$(awk -v f="$archive" '$2 == f || $2 == "*"f {print $1}' "${workdir}/checksums.txt" | head -n1)
    [ -n "$expected" ] || die "no checksum entry for ${archive} in checksums.txt"
    actual=$(SHA256 "${workdir}/${archive}")
    [ "$expected" = "$actual" ] || die "checksum mismatch for ${archive}: expected ${expected}, got ${actual}"
    info "checksum OK (sha256 ${actual})"
}

# shellcheck disable=SC2329
verify_signature() {
    pubfile="${workdir}/ws.pub"
    printf 'untrusted comment: workspace-cli release signing key\n%s\n' "$RELEASE_PUBKEY" > "$pubfile"
    minisign -V -p "$pubfile" -m "${workdir}/checksums.txt" \
        || die "minisign verification of checksums.txt FAILED — refusing to install"
    info "minisign signature OK"
}

# Signature policy is decided (and the signature verified) BEFORE the archive
# download: a fatal signature problem must not cost the big transfer first.
if command -v minisign >/dev/null 2>&1; then
    if [ "$have_sig" -eq 1 ]; then
        verify_signature
    elif [ "$REQUIRE_SIGNATURE" -eq 1 ]; then
        die "--require-signature was set but ${sig_reason}"
    else
        warn "${sig_reason}; proceeding with checksum-level verification only"
    fi
else
    if [ "$REQUIRE_SIGNATURE" -eq 1 ]; then
        die "--require-signature was set but minisign is not installed: https://jedisct1.github.io/minisign/"
    fi
    warn "minisign not found — CHECKSUM-LEVEL protection only (no protection against a compromised release)."
    warn "Install minisign and re-run for the full chain of trust (apt-get install minisign / brew install minisign)."
fi

info "downloading ${archive} (${version})"
DOWNLOAD "${base}/${archive}" "${workdir}/${archive}" || die "failed to download ${archive}"

verify_checksum

info "extracting ws from ${archive}"
tar -xzf "${workdir}/${archive}" -C "$workdir" ws || die "could not extract 'ws' from ${archive}"
[ -f "${workdir}/ws" ] || die "archive did not contain a 'ws' binary"
chmod 0755 "${workdir}/ws"

# Install by staging next to the destination and rename()-ing over it: the
# replacement is atomic AND lands on a NEW inode. An in-place `cp` reuses the
# destination inode, and macOS caches code-signature validity per inode —
# overwriting an existing signed binary that way makes every subsequent exec
# die with SIGKILL until the file is recreated.
dest_dir="${PREFIX}/bin"; dest="${dest_dir}/ws"
staged="${dest_dir}/.ws.staged.$$"
if [ -w "$PREFIX" ] || { [ -d "$dest_dir" ] && [ -w "$dest_dir" ]; } || mkdir -p "$dest_dir" 2>/dev/null; then
    info "installing to ${dest}"
    if ! { mkdir -p "$dest_dir" && cp "${workdir}/ws" "$staged" && chmod 0755 "$staged" && mv -f "$staged" "$dest"; }; then
        rm -f "$staged" 2>/dev/null
        die "failed to install to ${dest}"
    fi
elif command -v sudo >/dev/null 2>&1; then
    info "installing to ${dest} (requires sudo)"
    if ! sudo sh -c "mkdir -p '$dest_dir' && cp '${workdir}/ws' '$staged' && chmod 0755 '$staged' && mv -f '$staged' '$dest'"; then
        sudo rm -f "$staged" 2>/dev/null
        die "failed to install to ${dest} (sudo)"
    fi
else
    die "cannot write to ${dest_dir} and sudo unavailable; set PREFIX to a writable location (e.g. PREFIX=\$HOME/.local)"
fi

# Smoke-check: the installed binary must actually execute. "Installed" must
# mean "runs", not "file copied" — this catches a broken binary and, on macOS,
# a stale per-inode code-signature cache (file present, every exec SIGKILLed).
if ! "$dest" version >/dev/null 2>&1 && ! "$dest" --version >/dev/null 2>&1; then
    die "installed binary at ${dest} failed to execute; remove it (rm '${dest}') and re-run this installer"
fi

info "installed ws ${version} to ${dest}"
info "ensure ${dest_dir} is on your PATH; run 'ws --help' to start."

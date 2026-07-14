#!/usr/bin/env bash
# govulncheck-gate.sh — fail CI on any called (symbol-reachable) vulnerability
# that is not an exact (OSV-ID, module) match in the allowlist. Fails CLOSED if
# govulncheck itself does not run cleanly (a silently empty result would else
# read as "all clear" and disable monitoring).
#
# NOTE on exit codes: `govulncheck -format json` exits 0 when the scan COMPLETES
# (found vulnerabilities are reported inside the JSON, not via the exit code);
# any non-zero exit is a build/analysis/module error. `go run …@v1.5.0` is used
# so the tool resolves regardless of GOBIN/GOPATH/bin layout.
#
# Seams (self-test only; unset in CI):
#   GOVULN_JSON_FILE   read govulncheck JSON from this file instead of running it
#   GOVULN_FAKE_RC     simulate govulncheck's exit code (default 0)
#   GOVULN_ALLOW_FILE  allowlist path (default scripts/ci/govulncheck-allow.txt)
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
ALLOW_FILE="${GOVULN_ALLOW_FILE:-$here/govulncheck-allow.txt}"

if [ -n "${GOVULN_JSON_FILE:-}" ]; then
    json="$(cat "$GOVULN_JSON_FILE")"
    rc="${GOVULN_FAKE_RC:-0}"
else
    gverr="$(mktemp)"
    json="$(go run golang.org/x/vuln/cmd/govulncheck@v1.5.0 -format json ./... 2>"$gverr")"
    rc=$?
fi

# Any non-zero exit is a build/analysis/module error — fail closed.
if [ "$rc" -ne 0 ]; then
    echo "::error::govulncheck did not run cleanly (exit $rc) — failing closed"
    [ -n "${gverr:-}" ] && [ -f "$gverr" ] && cat "$gverr" >&2
    exit 2
fi
[ -n "${gverr:-}" ] && rm -f "$gverr"

# A real scan emits a config object first; its absence means govulncheck did not
# produce its structured output (empty/garbage) — fail closed rather than pass.
if ! printf '%s' "$json" | jq -e -s 'any(.[]?; .config != null)' >/dev/null 2>&1; then
    echo "::error::govulncheck produced no structured output — failing closed"; exit 2
fi

# Every called (osv module) pair — NOT de-duplicated by osv, so a wrong-module
# occurrence of an allowlisted OSV is still checked. Sorted-unique by line.
if ! called="$(printf '%s' "$json" | jq -s -r '
  .[] | select(.finding) | .finding
  | select(any(.trace[]?; has("function")))
  | [ .osv, ( [ .trace[] | select(has("function")) | .module ] | first ) ]
  | "\(.[0]) \(.[1])"' | sort -u)"; then
    echo "::error::could not parse govulncheck JSON — failing closed"; exit 2
fi

allow="$(grep -vE '^[[:space:]]*#|^[[:space:]]*$' "$ALLOW_FILE" | awk 'NF>=2 {print $1" "$2}')"
offending=""
while IFS= read -r pair; do
    [ -n "$pair" ] || continue
    if ! printf '%s\n' "$allow" | grep -qxF -- "$pair"; then
        offending="${offending}${pair}"$'\n'
    fi
done <<EOJ
$called
EOJ
if [ -n "$offending" ]; then
    echo "::error::govulncheck gate: called vulnerabilities not in the allowlist (osv module):"
    printf '%s' "$offending"
    echo "Add the (OSV-ID module) pair to $ALLOW_FILE with justification, or bump the dependency/toolchain."
    exit 1
fi
n="$(printf '%s' "$called" | grep -c . || true)"
echo "govulncheck gate OK: ${n} called vulnerability(ies), all allowlisted"
exit 0

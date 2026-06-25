#!/usr/bin/env bash
# assert-ran.sh — fail unless each named Go test PASSED (not SKIP/absent) in a
# `go test -v` log read from stdin. Guards CI against false-green silent skips.
#
# Usage: go test -v ... | scripts/ci/assert-ran.sh TestName [TestName...]
set -euo pipefail

log="$(cat)"
rc=0
for name in "$@"; do
  if printf '%s\n' "$log" | grep -qE "^--- SKIP: ${name} "; then
    echo "::error::required test ${name} was SKIPPED (expected to run)"
    rc=1
  elif ! printf '%s\n' "$log" | grep -qE "^--- PASS: ${name} "; then
    echo "::error::required test ${name} did not run (no PASS line)"
    rc=1
  fi
done
exit "$rc"

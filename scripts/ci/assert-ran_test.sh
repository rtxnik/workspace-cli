#!/usr/bin/env bash
# Self-test for assert-ran.sh. Plain bash (no bats), mirroring scripts/install_test.sh.
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
sut="$here/assert-ran.sh"
fail=0

check() { # desc, expected_rc, input, args...
  local desc="$1" want="$2"; shift 2
  local input="$1"; shift
  printf '%s' "$input" | "$sut" "$@" >/dev/null 2>&1
  local got=$?
  if [ "$got" -ne "$want" ]; then
    echo "FAIL: $desc (rc=$got want=$want)"; fail=1
  fi
}

check "pass accepted"            0 '--- PASS: TestFoo (0.01s)' TestFoo
check "skip rejected"            1 '--- SKIP: TestFoo (0.00s)' TestFoo
check "absent rejected"          1 'ok  pkg  0.5s' TestFoo
check "one of two missing"       1 '--- PASS: TestA (0.01s)' TestA TestB
check "both present accepted"    0 '--- PASS: TestA (0.0s)
--- PASS: TestB (0.0s)' TestA TestB
check "prefix not matched"       1 '--- PASS: TestFooBar (0.0s)' TestFoo

if [ "$fail" -eq 0 ]; then echo "assert-ran_test.sh: all cases passed"; fi
exit "$fail"

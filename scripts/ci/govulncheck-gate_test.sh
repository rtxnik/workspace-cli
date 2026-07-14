#!/usr/bin/env bash
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
sut="$here/govulncheck-gate.sh"
allow="$here/govulncheck-allow.txt"
fail=0
run() { local desc="$1" want="$2" fx="$3" rc="$4"
  GOVULN_ALLOW_FILE="$allow" GOVULN_JSON_FILE="$here/testdata/$fx" GOVULN_FAKE_RC="$rc" "$sut" >/dev/null 2>&1
  local got=$?
  if [ "$got" -ne "$want" ]; then echo "FAIL: $desc (rc=$got want=$want)"; fail=1; fi
}
run "all-allowlisted passes"                 0 govuln-clean.json     0
run "clean scan (config, no findings)"       0 govuln-cleanscan.json 0
run "unknown called vuln fails"              1 govuln-newvuln.json   0
run "allowlisted id wrong module fails"      1 govuln-wrongmod.json  0
run "same osv two modules -> wrong fails"    1 govuln-twomod.json    0
run "govulncheck exec failure fails closed"  2 govuln-clean.json     1
run "empty output fails closed"              2 govuln-empty.json     0
if [ "$fail" -eq 0 ]; then echo "govulncheck-gate_test.sh: all cases passed"; fi
exit "$fail"

#!/usr/bin/env bash
# validate-workflow-security.sh — A5 CI gate (harness integration, Phase 5).
# CANONICAL SOURCE: workspace-meta/scripts/ci/validate-workflow-security.sh
# Vendored byte-identical into each repo's scripts/ci/; drift-checked by
# workspace-meta `make check-validator-drift`. Edit the canonical copy only.
#
# Deps: mikefarah yq v4 (https://github.com/mikefarah/yq) + coreutils. No Node.
# Deterministic, fail-closed: any finding -> exit 1; tooling/usage error -> exit 2; clean -> exit 0.
#
# Universal rules (OpenSSF Scorecard-aligned; project-policy rules excluded by design):
#   R1 Token-Permissions   — TOP-LEVEL-ONLY: the top-level `permissions:` must be present and
#                            restrictive (read/none); per-job write scopes are allowed by design
#                            (move write to the job that needs it). Catches workflow-wide id-token:write. (D1-11)
#   R2 Pinned-Dependencies — every external `uses:` pinned to a 40-hex commit SHA, not a tag/branch.
#   R3 Dangerous-Workflow  — `${{ }}`-ONLY template-injection check: no untrusted context interpolated
#                            directly into a `run:` body (also scans composite-action run: bodies);
#                            not a full dangerous-workflow/taint auditor. (D1-6/D1-11)
set -uo pipefail

readonly SHA_RE='^[0-9a-f]{40}$'
# INJ_RE: untrusted ${{ }} contexts. The bare `inputs\.` branch is intentionally left
# unanchored so it also matches the no-space form ${{inputs.x}}; inside the ${{…}} envelope
# this fails CLOSED (may over-flag an exotic ident ending in `inputs`), the safe direction.
readonly INJ_RE='github\.event\.(issue|pull_request|comment|review|discussion|commits|head_commit|pages|workflow_run)[^}]*\.(title|body|message|name|email|label|ref|head_branch|default_branch)|github\.head_ref|github\.ref_name|github\.event\.inputs\.[A-Za-z0-9_-]+|inputs\.[A-Za-z0-9_-]+'

require_yq() {
  command -v yq >/dev/null 2>&1 || { echo "::error::yq (mikefarah v4) not found on PATH" >&2; exit 2; }
  yq --version 2>&1 | grep -q 'mikefarah' || { echo "::error::wrong yq flavor; need mikefarah/yq v4" >&2; exit 2; }
}

declare -a FINDINGS=()
add() { FINDINGS+=("$1|$2|$3"); }   # RULE|FILE|MESSAGE

check_r1() {
  local f="$1" tag
  tag=$(yq '.permissions | tag' "$f" 2>/dev/null) || { add R1 "$f" "unparseable YAML"; return; }
  case "$tag" in
    '!!null') add R1 "$f" "missing top-level permissions: block (implicit broad GITHUB_TOKEN)";;
    '!!str')
      local v; v=$(yq '.permissions' "$f")
      [[ "$v" == "write-all" || "$v" == "write" ]] && add R1 "$f" "top-level permissions: $v (write-all)";;
    '!!map')
      local k
      while IFS= read -r k; do
        [[ -n "$k" ]] && add R1 "$f" "top-level permissions.$k: write (move to the job that needs it)"
      done < <(yq '.permissions | to_entries | .[] | select(.value == "write" or .value == "write-all") | .key' "$f")
      ;;
  esac
}

check_r2() {
  local f="$1" u ref
  while IFS= read -r u; do
    [[ -z "$u" || "$u" == "null" ]] && continue
    [[ "$u" == ./* || "$u" == docker://* ]] && continue
    # shellcheck disable=SC2016  # the literal ${{ }} is a GHA expression marker, not a shell expansion
    if [[ "$u" == *'${{'* ]]; then add R2 "$f" "action '$u' uses an unresolvable expression ref (pin the templated value to a SHA)"; continue; fi
    if [[ "$u" != *"@"* ]]; then add R2 "$f" "action '$u' has no version ref"; continue; fi
    ref="${u##*@}"
    [[ "$ref" =~ $SHA_RE ]] || add R2 "$f" "action '$u' not SHA-pinned (use 40-char commit SHA)"
  done < <(yq '(.jobs[].steps[]? | select(has("uses")) | .uses), (.jobs[] | select(has("uses")) | .uses)' "$f" 2>/dev/null)
}

check_r3() {
  local f="$1" s
  # (a) run: bodies
  while IFS= read -r -d '' s; do
    echo "$s" | grep -qE "\\\$\{\{[^}]*(${INJ_RE})[^}]*\}\}" \
      && add R3 "$f" "untrusted context interpolated into run: (route via an intermediate env: var)"
  done < <(yq -0 '.jobs[].steps[]? | select(has("run")) | .run' "$f" 2>/dev/null)
  # (b) actions/github-script `with: script:` is an injection sink too (not a run: body)
  while IFS= read -r -d '' s; do
    [[ "$s" == "null" ]] && continue
    echo "$s" | grep -qE "\\\$\{\{[^}]*(${INJ_RE})[^}]*\}\}" \
      && add R3 "$f" "untrusted context interpolated into github-script with.script (pass via inputs/env, not string interpolation)"
  done < <(yq -0 '.jobs[].steps[]? | select(has("uses")) | select(.uses | test("github-script")) | .with.script' "$f" 2>/dev/null)
}

# Composite actions live at .github/actions/**/action.yml. They have a top-level
# `runs:` (not `jobs:`/`permissions:`), so R1 (top-level-permissions-only) and R2
# (jobs[].steps[].uses) do not apply — only R3 template-injection on run: bodies.
check_r3_composite() {
  local f="$1" s
  while IFS= read -r -d '' s; do
    echo "$s" | grep -qE "\\\$\{\{[^}]*(${INJ_RE})[^}]*\}\}" \
      && add R3 "$f" "untrusted context interpolated into composite run: (route via an intermediate env: var)"
  done < <(yq -0 '.runs.steps[]? | select(has("run")) | .run' "$f" 2>/dev/null)
}

main() {
  require_yq
  local -a files=()
  if [[ $# -gt 0 ]]; then files=("$@"); else
    shopt -s nullglob
    files=(.github/workflows/*.yml .github/workflows/*.yaml \
           .github/actions/*/action.yml .github/actions/*/action.yaml)
    shopt -u nullglob
  fi
  [[ ${#files[@]} -gt 0 ]] || { echo "validate-workflow-security: no workflow files found"; exit 0; }
  local f ndocs
  for f in "${files[@]}"; do
    [[ -f "$f" ]] || { echo "::error::not a file: $f" >&2; exit 2; }
    # Multi-document files fail closed: GitHub Actions uses only the first doc, but
    # `yq '.permissions | tag'` returns one line per doc and slips past R1's case arms.
    ndocs=$(yq 'di' "$f" 2>/dev/null | grep -c .)
    if [[ "${ndocs:-1}" -gt 1 ]]; then
      add R1 "$f" "multi-document workflow file ($ndocs docs; GitHub Actions uses only the first — use one workflow per file)"
      continue
    fi
    if [[ "$(yq '.runs.using // ""' "$f" 2>/dev/null)" != "" ]]; then
      check_r3_composite "$f"          # composite action: R3 (template-injection) only
    else
      check_r1 "$f"; check_r2 "$f"; check_r3 "$f"
    fi
  done
  if [[ ${#FINDINGS[@]} -eq 0 ]]; then
    echo "validate-workflow-security: OK (${#files[@]} workflow(s))"; exit 0
  fi
  local deduped; deduped=$(printf '%s\n' "${FINDINGS[@]}" | sort -u)
  echo "$deduped" | while IFS='|' read -r rule file msg; do echo "::error file=${file}::[${rule}] ${msg}"; done
  echo "validate-workflow-security: FAILED ($(echo "$deduped" | grep -c .) finding(s))" >&2
  exit 1
}
main "$@"

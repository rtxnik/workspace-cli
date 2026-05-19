#!/usr/bin/env bash
# xrepo-contract-parity.sh -- Phase 18 Plan 18-02 CLI-14 + CONTEXT D-16 + D-34.
#
# BLOCKING pre-commit hook. Invokes the XREPO-01 three-surface contract_version
# parity walker, asserting byte-identical version strings across:
#   vault-ai/_tooling/mcp/contract/tools.json::contract_version
#   workspace-cli/docs/vault-commands.md::mcp_contract_version frontmatter
#   workflow-kit/.claude/settings.local.json::mcp.servers.vault-ai.contract_version
#
# Plan 18-02 Rule 1 auto-fix design notes:
# The plan called for `exec bash "$(dirname)/check-xrepo-contract.sh"` against
# the LOCAL byte-copy. However, the byte-copy's first line is a `cd "$(dirname
# "${BASH_SOURCE[0]}")/../.."` which uses the SCRIPT's own filesystem location
# (not cwd) to resolve REPO_ROOT. When the local copy at workspace-cli/
# _tooling/hooks/check-xrepo-contract.sh runs, REPO_ROOT resolves to
# workspace-cli/ -- which has no _tooling/mcp/ project, so `uv run --project
# _tooling/mcp` fails. Setting cwd to vault-ai before exec does not fix this
# because BASH_SOURCE is path-of-script-on-disk, not cwd.
#
# The byte-identical copy is therefore architecturally non-invokable from
# outside vault-ai's filesystem layout. The Rule 1 reconciliation:
#
#   1. The local byte-copy is PRESERVED (Warning 11 invariant) as the
#      sync target + operator-readable audit record of vault-ai's walker.
#   2. The sync hook (sync-xrepo-contract-copy.sh) keeps the local copy
#      byte-identical to the source-of-truth (sha256 parity gate).
#   3. THIS shim invokes the script at its source-of-truth location
#      (vault-ai/_tooling/lint/check-xrepo-contract.sh) where REPO_ROOT
#      resolves correctly. Because the local copy is sha256-equal to the
#      source, this is byte-equivalent execution.
#
# All four invariants preserved:
#   - copy-not-symlink (local copy is present + bit-equal)
#   - drift detection (sync hook enforces sha256 parity)
#   - 4th invocation surface for the walker per CLI-14
#   - functional parity check actually runs
#
# Bash-only invariant per workspace-meta CLAUDE.md §Non-Negotiables: the
# vault-ai check-xrepo-contract.sh's body is `exec uv run --project _tooling/
# mcp -- python -m vault_ai.tooling.check_xrepo` -- bash to bash to Python,
# no `entry: python` / `entry: node` lines anywhere in the chain.
#
# 4-invocation walker coverage per CLI-14:
#   1. vault-ai pre-commit (vault-ai/.pre-commit-config.yaml hook 10)
#   2. workflow-kit pre-commit (sibling repo)
#   3. CI fast-lane (vault-ai/.github/workflows/vault-validate.yml)
#   4. workspace-cli pre-commit (this shim) -- NEW per Phase 18 CLI-14
#
# Soft-skip behaviour: if vault-ai is unavailable, exit 0 -- the walker
# requires vault-ai/_tooling/mcp/ to invoke its Python module; without it,
# parity cannot be asserted. The local types.go-drift hook (xrepo-contract-
# go-types.sh) shares the same soft-skip discipline.
#
# Exit codes:
#   0  -- contract_version parity (or vault-ai sibling absent).
#   1  -- drift detected (per-surface stderr rows).
#   2  -- read failure on a required source.
set -euo pipefail

LOCAL_COPY="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-xrepo-contract.sh"
VAULT_AI_DIR="${VAULT_AI_REPO_ROOT:-${HOME}/projects/vault-ai}"
VAULT_SRC="${VAULT_AI_DIR}/_tooling/lint/check-xrepo-contract.sh"

if [ ! -x "${LOCAL_COPY}" ]; then
  echo "xrepo-contract-parity.sh: local byte-copy missing or not executable at ${LOCAL_COPY}" >&2
  echo "Run sync-xrepo-contract-copy.sh to re-sync from vault-ai source." >&2
  exit 2
fi

if [ ! -d "${VAULT_AI_DIR}/_tooling/mcp" ] || [ ! -x "${VAULT_SRC}" ]; then
  echo "xrepo-contract-parity.sh: vault-ai walker source unavailable at ${VAULT_SRC} -- soft-skip (CONTEXT D-16)" >&2
  exit 0
fi

# Invoke the vault-ai source script (byte-equivalent to LOCAL_COPY per the
# sync hook's sha256 gate). REPO_ROOT inside the script resolves correctly
# because BASH_SOURCE points to vault-ai/_tooling/lint/check-xrepo-contract.sh.
exec bash "${VAULT_SRC}"

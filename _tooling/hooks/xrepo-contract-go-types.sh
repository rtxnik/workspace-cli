#!/usr/bin/env bash
# xrepo-contract-go-types.sh -- Phase 18 Plan 18-02 CLI-13 + CLI-14.
#
# BLOCKING pre-commit hook. Delegates to vault-ai's check_xrepo --gen-types-check
# (chained shim per CONTEXT D-15) which in turn invokes gen-go-types.py --check
# against the canonical workspace-cli/internal/mcp/types.go. Exit non-zero on
# any drift between the generated output and the on-disk file.
#
# Bash-only invariant per workspace-meta CLAUDE.md §Non-Negotiables: Python is
# invoked exclusively via `uv run --project ~/projects/vault-ai/_tooling/mcp`;
# no `entry: python` / `entry: node` lines anywhere in the chain.
#
# Soft-skip behaviour: if the vault-ai sibling repo is unavailable
# (operator environment subset that lacks vault-ai), the hook prints a warning
# and exits 0 — types.go drift cannot be detected without the source-of-truth
# tools.json. The XREPO-01 walker itself is responsible for surfacing the
# vault-ai-absence case in environments that DO have vault-ai checked out.
#
# Exit codes:
#   0  -- on-disk types.go matches fresh codegen (or vault-ai sibling absent).
#   1  -- drift detected (codegen DRIFT marker + unified diff on stderr).
#   2  -- read failure on tools.json or the codegen script itself.
set -euo pipefail

VAULT_AI_DIR="${VAULT_AI_REPO_ROOT:-${HOME}/projects/vault-ai}"

if [ ! -d "${VAULT_AI_DIR}/_tooling/mcp" ]; then
  echo "xrepo-contract-go-types.sh: vault-ai unavailable at ${VAULT_AI_DIR} -- soft-skip (CONTEXT D-16)" >&2
  exit 0
fi

exec uv run --project "${VAULT_AI_DIR}/_tooling/mcp" -- python -m vault_ai.tooling.check_xrepo --gen-types-check

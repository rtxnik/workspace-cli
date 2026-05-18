#!/usr/bin/env bash
# check-xrepo-contract.sh -- Phase 10 D-23 XREPO-01 contract-drift bash wrapper.
#
# Mirrors Phase 8 D-22 (_tooling/lint/check-embedder-chokepoint.sh) and
# Plan 01 D-25 (_tooling/lint/check-mcp-envelope.sh) wrapper shapes.
# Bash-only invariant per workspace-meta CLAUDE.md.  Python is invoked
# exclusively via `uv run --project _tooling/mcp -- python -m` (no
# alternative runners; no node-based shims).
#
# Reads three sibling repos under $XREPO_REPOS_ROOT (default ~/projects):
#   vault-ai/_tooling/mcp/contract/tools.json::contract_version
#   workspace-cli/docs/vault-commands.md::mcp_contract_version frontmatter
#   workflow-kit/.claude/settings.local.json::mcp.servers.vault-ai.contract_version
#
# Asserts byte-identical string parity across non-skipped sources;
# workflow-kit is OPTIONAL (soft-skip when file or nested key absent).
# Output format follows Phase 7 D-12 convention
# (PATH:LINE:RULE_ID:message) so editor integrations surface findings
# uniformly across Phase 8/9/10 rails.
#
# Exit codes:
#   0 -- string parity across non-skipped sources
#   1 -- drift detected; one row per source on stderr
#        (rule xrepo.contract_drift)
#   2 -- read failure on vault-ai or workspace-cli (required source)
#        (rule xrepo.read_failure)
#
# Phase 13 wires this wrapper into vault-validate.yml as a merge-blocker
# job; sibling repos (workspace-cli, workflow-kit) get a copy of this
# wrapper in their own pre-commit chains per CONTEXT D-23 +
# workspace-meta TOPOLOGY (copy-not-symlink preserves repo independence).
#
# Manual invocation (early feedback without committing):
#   bash _tooling/lint/check-xrepo-contract.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

exec uv run --project _tooling/mcp -- python -m vault_ai.tooling.check_xrepo "$@"

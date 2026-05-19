#!/usr/bin/env bash
# sync-xrepo-contract-copy.sh -- Phase 18 Plan 18-02 CONTEXT D-34 + Warning 11 fix.
#
# BLOCKING pre-commit hook (always_run=true). On every commit, compares the
# sha256 of the LOCAL byte-copy at _tooling/hooks/check-xrepo-contract.sh
# against the vault-ai source at ~/projects/vault-ai/_tooling/lint/
# check-xrepo-contract.sh. On mismatch, re-syncs the local copy + refuses the
# commit with a `git add` re-stage instruction so the operator can review and
# include the re-sync in the same change set.
#
# Copy-not-symlink invariant rationale (CONTEXT D-34):
# Symlinking the file across repos would break repo independence (workspace-cli
# tests + commits would fail outside the meta-workspace). The byte-copy plus
# automated sha256 drift detection preserves both the independence invariant
# and the single-source-of-truth invariant.
#
# Soft-skip behaviour: if the vault-ai sibling repo is unavailable
# (operator environment subset that lacks vault-ai), the hook prints a warning
# and exits 0 -- the local copy is the last-known-good and the next operator
# who DOES have vault-ai checked out will surface any drift on their next
# commit.
#
# Exit codes:
#   0  -- sha256 parity (or vault-ai sibling absent).
#   1  -- drift detected; local copy was re-synced; operator must re-stage.
#   2  -- read failure on either file.
set -euo pipefail

VAULT_AI_DIR="${VAULT_AI_REPO_ROOT:-${HOME}/projects/vault-ai}"
VAULT_SRC="${VAULT_AI_DIR}/_tooling/lint/check-xrepo-contract.sh"
LOCAL_COPY="$(dirname "${BASH_SOURCE[0]}")/check-xrepo-contract.sh"

if [ ! -f "${VAULT_SRC}" ]; then
  echo "sync-xrepo-contract-copy.sh: vault-ai source unavailable at ${VAULT_SRC} -- soft-skip (CONTEXT D-16)" >&2
  exit 0
fi

if [ ! -f "${LOCAL_COPY}" ]; then
  echo "sync-xrepo-contract-copy.sh: local copy missing at ${LOCAL_COPY} -- bootstrapping from vault-ai source" >&2
  cp "${VAULT_SRC}" "${LOCAL_COPY}"
  chmod +x "${LOCAL_COPY}"
  echo "Re-stage ${LOCAL_COPY} and re-commit." >&2
  exit 1
fi

SRC_SHA=$(sha256sum "${VAULT_SRC}" | cut -d' ' -f1)
LOCAL_SHA=$(sha256sum "${LOCAL_COPY}" | cut -d' ' -f1)

if [ "${SRC_SHA}" != "${LOCAL_SHA}" ]; then
  echo "DRIFT: local copy ${LOCAL_COPY} (sha256 ${LOCAL_SHA}) differs from vault-ai source ${VAULT_SRC} (sha256 ${SRC_SHA})" >&2
  echo "Re-syncing now. Re-stage the file and re-commit." >&2
  cp "${VAULT_SRC}" "${LOCAL_COPY}"
  chmod +x "${LOCAL_COPY}"
  exit 1
fi

exit 0

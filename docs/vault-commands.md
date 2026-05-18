---
title: ws vault sub-command reference
mcp_contract_version: "1.3.0"
status: accepted
design_only: false
ships: v2.2
source_adr: vault-ai/docs/adr/adr-int-03-ws-vault-cli.md
---

# ws vault — Vault-AI CLI Reference

CLI facade over the vault-ai MCP server per [ADR-int-03](../../vault-ai/docs/adr/adr-int-03-ws-vault-cli.md). Provides shell-native access to the 25-tool MCP contract via 10 sub-commands with GNU-style flag parsing, Unix exit codes, and fd-3 token authentication. Invokes the stdio MCP transport per [ADR-ai-01](../../vault-ai/docs/adr/adr-ai-01-mcp-dual-mode.md).

## Status

- **Spec status:** `accepted` — Phase 18 (CLI-01..14) shipped 2026-05-18; all 10 sub-commands implemented under `workspace-cli/cmd/vault*.go`; the Go binary delivers the surface end-to-end via `internal/mcp/` Go MCP stdio client.
- **ADR status:** [ADR-int-03](../../vault-ai/docs/adr/adr-int-03-ws-vault-cli.md) `accepted`; §v2.2 Amendment body (D-int03-v2.2-A) records the 6 deltas landed Phase 18 (command-surface realignment + mark3labs/mcp-go pin + fd-3 token transport + gen-go-types.py codegen + XREPO-01 walker 4-surface + doctor read-only discipline).
- **Ships:** v2.2 (Phase 18 closure 2026-05-18). Go implementation under `workspace-cli/cmd/vault*.go` + `workspace-cli/internal/mcp/`.

The command list below realigns to the v2.2 implemented surface per `.planning/REQUIREMENTS.md` CLI-01..10. The v2.0 design-only list (`triage / export / push-drive / health / reindex / validate / coverage / restore / init / stats`) is superseded per ADR-int-03 §v2.2 Amendment §1.

## Contract version

Every vault-ai surface with a contract dependency on the MCP 25-tool lockfile stamps `contract_version: "1.3.0"`. Three surfaces participate:

| Surface | File | Field |
|---------|------|-------|
| MCP tools source-of-truth | [`vault-ai/_tooling/mcp/contract/tools.json`](../../vault-ai/_tooling/mcp/contract/tools.json) | top-level `contract_version` |
| MCP registration | [`workflow-kit/.claude/settings.local.json`](../../workflow-kit/.claude/settings.local.json) | `mcp.servers.vault-ai.contract_version` |
| CLI spec | this document | frontmatter `mcp_contract_version` |

vault-ai's [`_tooling/lint/check-xrepo-contract.sh`](../../vault-ai/_tooling/lint/check-xrepo-contract.sh) asserts byte-identical string-parity across all three surfaces (XREPO-01 drift detection per vault-ai Phase 10 D-23). Any bump to one file requires coordinated bumps to the other two in the same coordinated GO-label PR per [ADR-int-04](../../vault-ai/docs/adr/adr-int-04-adr-merge-blocker.md) — pre-commit + Phase 13 CI reject mismatch at merge time. Semver semantics follow semver.org: PATCH for additive flags, MINOR for new tools, additive error codes, or additive optional fields, MAJOR for tool removals, error-code removals, or contract re-shapes.

**Bump 1.0.0 → 1.1.0** (vault-ai Phase 10, 2026-05-02): additive minor. Two error codes added (`MISSING_DEPENDENCY`, `NOT_IMPLEMENTED`); `tools[].cost_service` optional field added; no removals; backward compatible. See [`vault-ai/_tooling/mcp/contract/CHANGELOG.md`](../../vault-ai/_tooling/mcp/contract/CHANGELOG.md) and [ADR-ai-01 §Amendment History](../../vault-ai/docs/adr/adr-ai-01-mcp-dual-mode.md) for the full record.

**Bump 1.1.0 → 1.2.0** (vault-ai Phase 17, 2026-05-08): additive minor. One error code added (`DEDUP_BLOCKED`, the 11th envelope code) plus three additive optional input flags on `create_note` (`dedup_strategy`, `dedup_threshold`, `dedup_force`) wiring the runtime dedup gate; no removals; backward compatible per semver MINOR semantics. Rationale: Phase 17 ships a content-hash + cosine-similarity dedup engine that fires at `create_note` time and emits `DEDUP_BLOCKED` when a near-duplicate already exists, plus a 6th audit stream (`dedup`) registered in `verify_audit_chain.py` STREAMS for forensic replay. See ADR-flow-05 §v2.2 amendment ("Dedup gate at note ingress") and vault-ai Phase 17 SUMMARY for the full record (forward-pointer once `17-02-SUMMARY.md` lands).

**Bump 1.2.0 → 1.3.0** (vault-ai Phase 16, 2026-05-09): additive minor. Two new tools added (`triage_run` 24th, `triage_override` 25th) wiring the Phase 16 triage agent runtime per ADR-flow-02 §Tool Surface; tool count moves 23 → 25. One error code (`AGENT_TOOL_NOT_BOUND`, raised by the agent's L2 11-tool dispatcher) was added in Plan 16-01 and stayed within v1.2.0 (additive within the same contract_version since only an error code landed); the 1.3.0 bump tracks the new tool surface. `triage_process.possible_errors` augmented with `DEDUP_BLOCKED` (the agent's pass-2 dedup-check route can fire it via update_note's transitive create_note path); the existing `triage_process` wire shape (`inbox_id, type, tags, target_zone, related`) is preserved BYTE-FOR-BYTE — Plan 16-03 Task 3 replaced its NOT_IMPLEMENTED stub with the real apply-decision orchestration (move_note + update_note(frontmatter+related)) per CONTEXT D-02. No removals; backward compatible per semver MINOR semantics. The 7th audit stream (`triage`) was registered in `verify_audit_chain.py` STREAMS in Plan 16-02. See ADR-flow-02 §v2.2 amendment (forward-pointer once Plan 16-06 lands) and vault-ai Phase 16 plan 16-03 SUMMARY for the full record.

## Exit codes

CLI exit codes map the MCP error envelope to Unix-native semantics (locked in ADR-int-03 §Exit codes). The mapping is fixed and cannot be reshaped without a supersession ADR. The 1.0.0 contract shipped 8 envelope codes mapped onto exit codes 1-4; the 1.1.0 contract (vault-ai Phase 10) extends the envelope to 10 codes by adding `MISSING_DEPENDENCY` (already mapped at exit 4 in 1.0.0; the codeset extension now formalises what this CLI already cited) and `NOT_IMPLEMENTED` (new exit 5); the 1.2.0 contract (vault-ai Phase 17) extends the envelope to 11 codes by adding `DEDUP_BLOCKED` (new exit 6, see Common flags / runtime dedup gate; safety rail — does NOT auto-retry); the 1.2.0 codeset was further extended by vault-ai Phase 16 Plan 16-01 to 12 codes by adding `AGENT_TOOL_NOT_BOUND` (new exit 7, raised by the triage agent's L2 11-tool dispatcher when the LLM emits a non-allowlisted tool name; safety rail — operator inspects audit row + retunes prompt):

| Exit | Meaning | MCP envelope error code | Caller action |
|------|---------|-------------------------|---------------|
| 0 | Success | — (no error block) | Proceed |
| 1 | Validation failure (frontmatter malformed, schema violation) | `VALIDATION_FAILED` | Fix note content, re-run |
| 2 | Budget exceeded (cost cap hit per ADR-int-01 + ADR-int-05) | `BUDGET_EXCEEDED` | Wait for daily/monthly reset or raise caps |
| 3 | Visibility leak attempted (egress blocked per ADR-ai-02) | `VISIBILITY_LEAK` | Do NOT retry; investigate + file incident (primary security rail) |
| 4 | Missing dependency (Qdrant container offline, `VAULT_AI_TOKEN` unset, age identity at `~/.config/age/keys.txt` absent, `~/.config/vault-ai/mcp-token.age` 0600 permissions wrong, sibling repo at stale `mcp_contract_version` per XREPO-01) | `MISSING_DEPENDENCY` | Fix environment, re-run |
| 5 | Tool deferred to v2.2 backend (`triage_process`, `generate_image`, `summarize_pdf`, `render_card`); handler returns a structured deferral envelope with `error.details = {ships: "v2.2", reference_adr: …}` rather than raising | `NOT_IMPLEMENTED` | Wait for v2.2 release; do NOT retry; consult `error.details.reference_adr` for the v2.2 ADR tracking the backend |
| 6 | Near-duplicate note already exists in vault (content-hash exact match or cosine-similarity ≥ threshold per Phase 17 dedup gate at `create_note` time); response envelope carries `error.details = {existing_id, similarity, strategy}` for caller introspection | `DEDUP_BLOCKED` | Inspect existing note, decide whether to merge / amend / explicitly bypass via `--dedup-force` (operator-confirmed); do NOT auto-retry — duplication is a content decision, not a transient failure |
| 7 | Triage agent (Phase 16) emitted a tool-call token for a name outside the 11-tool L2 allowlist (per ADR-flow-02 §Tool Surface); response envelope carries `error.details = {attempted_tool, reason: "tool_not_bound"}` | `AGENT_TOOL_NOT_BOUND` | Inspect the agent's `triage` audit row + retune the L1 system-prompt fragment; do NOT auto-retry — the structural binding is the contract |

Codes 8-127 are reserved for future sub-command-specific semantics. Codes ≥128 indicate a wrapper or signal-handling issue (bash convention) and are not part of the vault-ai contract.

## Auth

Single authentication secret: `VAULT_AI_TOKEN` (256-bit bearer per [ADR-ai-06](../../vault-ai/docs/adr/adr-ai-06-mcp-auth.md)).

**Provisioning.** `~/.config/vault-ai/mcp-token.age` is age-encrypted with multi-recipient recipients (primary + escrow, per [ADR-sec-02](../../vault-ai/docs/adr/adr-sec-02-secrets-stack.md)). Chezmoi decrypts at `chezmoi apply` time to `~/.config/vault-ai/mcp-token`; shell init (`~/.zshrc` or `~/.bashrc`) sources it into `VAULT_AI_TOKEN` env var. The CLI reads the env var at invocation.

**Unset token → exit 4.** If the CLI cannot read `VAULT_AI_TOKEN` from env it exits 4 with a pointer to the shell-init provisioning docs (no fallback to interactive prompt — scripts must fail fast).

**Rotation.** Quarterly per ADR-sec-02 rotation procedure; zero CLI changes required (env read is opaque to CLI version).

**iOS exception.** iOS hosts carry no `VAULT_AI_TOKEN` (per Phase 6 D-20 and Plan 05 iOS chezmoi template). `ws vault` is not invoked on iOS — capture-only + push-to-desktop workflow per [ADR-flow-01](../../vault-ai/docs/adr/adr-flow-01-mobile-capture.md) (Phase 5 forward-ref).

## Common flags

Every sub-command accepts:

- `--help` — print sub-command documentation + flag descriptions + sample invocation. Exits 0.
- `--version` — print the CLI binary version + `mcp_contract_version` string from this document's frontmatter. Exits 0.
- `--json` — emit stdout as newline-delimited JSON (NDJSON) for programmatic consumers. Overrides any command-specific output format.
- `--quiet` — suppress non-error stderr output (useful for cron jobs).

Sub-command-specific flags are documented in each sub-command section below.

## Sub-commands

Ten sub-commands lock the CLI surface per ADR-int-03 §10 sub-commands verbatim. Each section below documents flags, purpose, output, sample usage, and the MCP tool it wraps. The v2.2 surface is implemented per `.planning/REQUIREMENTS.md` CLI-01..10 (Phase 18, 2026-05-18).

### ws vault status

**Purpose.** Print a single-screen green/yellow/red summary of 6 composite signals: MCP liveness, Qdrant collection size, audit-chain integrity, cost-tracker headroom, dedup gate readiness, last-DR-drill-age. (CLI-01 v2.2 ship-gate; Phase 18 Plan 18-03.)

**Flags.** `--json` emits structured envelope for cron / dashboard consumers.

**Output.** Composite line per signal + overall verdict; exit 0 (green), 1 (yellow), 2 (red).

### ws vault search

**Purpose.** Hybrid-search top-K results from shell. Wraps MCP `search_notes` tool (v1.3.0). (CLI-02; Phase 18 Plan 18-03.)

**Flags.** `--k N` (default 10); `--type X`; `--zone Y`; `--json`.

### ws vault triage-run

**Purpose.** Trigger a triage pass over the inbox (or specified path). Wraps MCP `triage_run` tool (24th tool; Phase 16). (CLI-03; Phase 18 Plan 18-04.)

**Flags.** `--inbox <path>`; `--dry-run` (proposed routing without writing); `--limit N`.

### ws vault validate

**Purpose.** Run schema validators on a single note or the whole vault. Wraps MCP `validate_note` tool. (CLI-04; Phase 18 Plan 18-03.)

**Flags.** `--path <file>`; `--type X`; `--zone Y`. Exits 0 green / non-zero on findings.

### ws vault reindex

**Purpose.** Rebuild the Qdrant index for the given collection (or all). Shells out to `vault-ai/_tooling/mcp/scripts/embed_index.py` subcommand `index` per CONTEXT D-27 §OQ-3 Amendment. (CLI-05; Phase 18 Plan 18-04.)

**Flags.** `--collection <name>` (default: recipe-pinned); `--full` (drop + reinit).

### ws vault backup-verify

**Purpose.** Run the diff-aware DR backup-verify routine; reads latest `_tooling/logs/backup-verify-{ISO-week}.jsonl` log per Phase 21c HARD-09. Graceful Phase 21c-not-shipped fallback (informative skip rather than error). (CLI-06; Phase 18 Plan 18-04.)

**Flags.** `--strict` (exit non-zero on stale logs).

### ws vault ingest

**Purpose.** Ingest a single markdown file via `create_note` MCP tool with dedup gate check (Phase 17). (CLI-07; Phase 18 Plan 18-04.)

**Flags.** `<file>` (positional); `--dedup-force` (paired with `--yes` per CONTEXT D-24); `--zone <target>`; `--type X`.

**Safety rail.** `--dedup-force` requires `--yes` (operator-confirmed bypass per Phase 17 dedup override audit pattern); single-flag use rejected at exit 4.

### ws vault get-coverage-report

**Purpose.** Print the coverage-linter report per ADR-obs-02. Wraps MCP `get_coverage_report` tool. (CLI-08; Phase 18 Plan 18-03.)

**Flags.** `--json` (structured); `--out PATH` (write to file).

### ws vault vault-health-score

**Purpose.** Print the live composite health score (0-100) per ADR-obs-05 weights. Composed Go-side via `internal/mcp/health.go ComputeVaultHealthScore` (OQ-1 path-B per CONTEXT D-21 Amendment). (CLI-09; Phase 18 Plan 18-03.)

**Flags.** `--json` (structured envelope with per-signal breakdown).

### ws vault doctor

**Purpose.** Self-diagnose common failures: orphan MCP subprocess, stale lock files, missing token, broken token-fd-pass, qdrant container liveness. **Read-only by default** per memory `feedback_no_auto_state_mutation`. (CLI-10; Phase 18 Plan 18-05.)

**Flags.** `--kill-orphans` (opt-in mutation; requires `--yes`); `--clear-stale-locks` (opt-in mutation; requires `--yes`). Without flags, doctor diagnoses + prints remediation; doctor does not heal silently. Enforced by `TestVaultDoctorReadOnlyByDefault` tripwire.

**Sample.**
```
ws vault doctor               # read-only diagnosis
ws vault doctor --kill-orphans --yes  # opt-in cleanup of orphan vault-mcp-server processes
```

## See also

- [ADR-int-03 — ws vault CLI Contract](../../vault-ai/docs/adr/adr-int-03-ws-vault-cli.md) — ADR-level lockfile for this spec
- [ADR-ai-01 — MCP Dual-Mode](../../vault-ai/docs/adr/adr-ai-01-mcp-dual-mode.md) — the MCP contract this CLI wraps
- [ADR-ai-06 — MCP Auth](../../vault-ai/docs/adr/adr-ai-06-mcp-auth.md) — VAULT_AI_TOKEN provisioning
- [ADR-sec-02 — Secrets Stack](../../vault-ai/docs/adr/adr-sec-02-secrets-stack.md) — chezmoi+age multi-recipient auth provisioning
- [`vault-ai/_tooling/mcp/contract/tools.json`](../../vault-ai/_tooling/mcp/contract/tools.json) — 25-tool MCP contract (v1.3.0; +`triage_run` + `triage_override` per Phase 16)
- [`workflow-kit/.claude/settings.local.json`](../../workflow-kit/.claude/settings.local.json) — MCP registration surface

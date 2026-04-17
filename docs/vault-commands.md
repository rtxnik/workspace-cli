# `ws vault` CLI — Subcommand Specifications

**Owner:** rtxnik | **Status:** Design-only (v2.0); build ships v2.1+ | **ADR:** Phase 6 INT-14 (pending)

The `ws vault` namespace is the workspace-cli surface for vault-ai operational commands. v2.0 locks the FIRST subcommand (`triage`) as the handover from Phase 5; Phase 6 INT-14 authors the remaining 9 subcommands using the same specification pattern.

**Scope in v2.0:** design-only spec for `ws vault triage` (Phase 5 FLOW-02 handover).

**Scope in Phase 6 INT-14 (next):** `ws vault export | push-drive | health | reindex | validate | coverage | restore | init | stats` (9 more subcommands) authored in this same file.

**Scope in v2.1+:** Go + cobra build per `workspace-cli` passport stack (Go 1.24.2, cobra 1.8.x+).

---

## `ws vault triage`

**Name:** `ws vault triage` — Invoke the vault-ai triage agent against `00_Inbox/` per ADR-flow-02 contract.

**Synopsis:**

```
ws vault triage [--limit N] [--confidence-min X] [--dry-run] [--since YYYY-MM-DD] [--repo <sibling>]
```

**Description:**

Invokes the 5-step triage algorithm locked in `vault-ai/docs/adr/adr-flow-02-triage-agent-contract.md`:

1. `read_inbox` — list `00_Inbox/YYYY-MM/inbox-*.md` notes via MCP `list_inbox`
2. `classify_one` — pass-1 grammar-constrained JSON metadata extract (BGE-M3 local embedding; Gemini fallback for `internal | public` visibility + low confidence)
3. `dedup_check` — `semantic_similar` MCP call with thresholds from `vault-ai/_tooling/config/similarity.yaml` (Plan 03: 0.85 merge prompt / 0.70 related / <0.70 new)
4. `interact` — ask up to 2 clarifying questions per note if pass-1 confidence <0.60
5. `execute + journal` — append JSONL record to `_tooling/logs/triage-YYYY-MM-DD.jsonl` for every tool call; rollback via `validate_note` on post-write `{ok: false}`

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--limit N` | integer | 50 | Maximum inbox items to process in this session (ADR-flow-02 §Rate Limit; prevents agent-runaway on large backlogs). See CONTEXT D-34. |
| `--confidence-min X` | float in [0.0, 1.0] | 0.60 | Minimum classification confidence; items below this fall into the "ask max 2 clarifications" branch per D-34 thresholds. |
| `--dry-run` | boolean | false | Execute pass-1 classification only; no WRITE tool calls (no `update_note` / `move_note` / `merge_notes`). Useful for auditing classification decisions before acting. |
| `--since YYYY-MM-DD` | date | (unset) | Only process inbox items with `created >= $SINCE`. Defaults to all `00_Inbox/` items if unset. |
| `--repo <sibling>` | string | (unset) | Filter to inbox items with `sibling_repo == <sibling>` frontmatter (sibling-watcher-generated stubs from Phase 5 ADR-flow-03 — values: `workflow-kit` \| `workspace-cli` \| `dotfiles` \| `workspace-meta`). |

**Exit codes:**

| Code | Meaning |
|------|---------|
| 0 | Session completed; JSONL log appended |
| 1 | Partial session (some items failed; JSONL log records per-item outcomes) |
| 2 | Invalid arguments (unknown flag, bad `--since` date format, out-of-range `--confidence-min`) |
| 3 | MCP connection failed (check `VAULT_AI_MCP_URL` + `~/.config/vault-ai/mcp-token.age` per Phase 4 ADR-ai-06) |
| 4 | Authentication failed (token expired or rotated; re-run `chezmoi apply` then retry) |
| 5 | Validator rollback (post-write `validate_note` returned `{ok: false}`; note state restored to pre-write) |

**Environment variables:**

| Variable | Required | Description |
|----------|----------|-------------|
| `VAULT_AI_MCP_URL` | yes | MCP endpoint, typically `http://127.0.0.1:8765` (Phase 4 ADR-ai-01 §Bind) |
| `VAULT_AI_TOKEN` | yes | Bearer token consumed by MCP auth layer (Phase 4 ADR-ai-06; sourced from `~/.config/vault-ai/mcp-token.age` via chezmoi + age) |
| `VAULT_AI_LOG_DIR` | no | Override log output directory (default: `vault-ai/_tooling/logs/`) |
| `VAULT_AI_LOG_LEVEL` | no | `debug` \| `info` \| `warn` \| `error` (default: `info`) |

**Examples:**

Process up to 20 recent inbox items with confidence ≥0.75:

```
ws vault triage --limit 20 --confidence-min 0.75
```

Audit pass-1 classifications on items from the past 7 days without writing:

```
ws vault triage --dry-run --since $(date -d '7 days ago' +%Y-%m-%d)
```

Drain the workflow-kit sibling-watcher stub backlog:

```
ws vault triage --repo workflow-kit --limit 50
```

**See also:**

- `vault-ai/docs/adr/adr-flow-02-triage-agent-contract.md` — full agent contract (5-step algorithm, 5-layer security rail, audit-log JSONL schema)
- `vault-ai/docs/adr/adr-flow-03-sibling-watcher-daemon.md` — source of `--repo` inbox-stubs
- `vault-ai/_tooling/config/similarity.yaml` — dedup thresholds (0.85 merge / 0.70 related)
- `vault-ai/_tooling/logs/README.md` §triage-YYYY-MM-DD.jsonl — audit-log schema (Phase 5 locked, 5-family narrative)
- `vault-ai/docs/REVIEW-CHECKLIST.md#daily` — daily ritual invocation context

---

## Remaining subcommands (Phase 6 INT-14)

The following 9 subcommands are owned by Phase 6 INT-14 and will be authored in this same file using the structure above (Synopsis / Description / Flags table / Exit codes / Environment variables / Examples / See also):

- `ws vault export` — export a note or set with `--public` scope gate
- `ws vault push-drive` — Google Drive attachment mirror (Phase 6 INT-07 opt-in)
- `ws vault health` — vault_health_score read + emit
- `ws vault reindex` — full re-embed via alias-swap (Phase 4 ADR-ai-03 §Migration)
- `ws vault validate` — Ajv + sufficiency linter batch run
- `ws vault coverage` — coverage-linter sibling-entities report
- `ws vault restore` — invoke `disaster-restore.sh` (Phase 5 Plan 06 / v2.1 executable)
- `ws vault init` — one-shot first-time setup (chezmoi + age + MCP token)
- `ws vault stats` — monthly metrics summary

(Structure preserved for Phase 6 to append without re-designing.)

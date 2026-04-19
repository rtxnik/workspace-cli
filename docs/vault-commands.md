---
title: ws vault sub-command reference
mcp_contract_version: "1.0.0"
status: proposed
design_only: true
ships: v2.1+
source_adr: vault-ai/docs/adr/adr-int-03-ws-vault-cli.md
---

# ws vault — Vault-AI CLI Reference

CLI facade over the vault-ai MCP server per [ADR-int-03](../../vault-ai/docs/adr/adr-int-03-ws-vault-cli.md). Provides shell-native access to the 23-tool MCP contract via 10 sub-commands with GNU-style flag parsing, Unix exit codes, and env-var authentication. Invokes the stdio MCP transport per [ADR-ai-01](../../vault-ai/docs/adr/adr-ai-01-mcp-dual-mode.md).

## Status

- **Spec status:** `proposed` — this document is design-only per Phase 6 CONTEXT D-21. The `workspace-cli` Go binary does not yet implement `ws vault <cmd>`; every invocation currently returns `"not yet implemented; see workspace-cli/docs/vault-commands.md"` as a stub.
- **ADR status:** [ADR-int-03](../../vault-ai/docs/adr/adr-int-03-ws-vault-cli.md) is `accepted` (design is locked).
- **Ships:** v2.1+ build phase (Go implementation under `workspace-cli/cmd/vault/`).

The design-only split is intentional — v2.0 milestone ships architectural artefacts + contracts only (PROJECT.md). The pinned 10-command surface + exit-code mapping + auth contract gives v2.1 build phase a contract to implement against, not a blank page.

## Contract version

Every vault-ai surface with a contract dependency on the MCP 23-tool lockfile stamps `contract_version: "1.0.0"`. Three surfaces participate:

| Surface | File | Field |
|---------|------|-------|
| MCP tools source-of-truth | [`vault-ai/_tooling/mcp/contract/tools.json`](../../vault-ai/_tooling/mcp/contract/tools.json) | top-level `contract_version` |
| MCP registration | [`workflow-kit/.claude/settings.local.json`](../../workflow-kit/.claude/settings.local.json) | `mcp.servers.vault-ai.contract_version` |
| CLI spec | this document | frontmatter `mcp_contract_version` |

Plan 08 `phase-closure-check.sh` asserts string-parity across all three surfaces (XREPO-01 drift detection). Any bump to one file requires coordinated bumps to the other two in the same PR — CI rejects mismatch at merge time. Semver semantics follow semver.org: PATCH for additive flags, MINOR for new tools or breaking flag changes, MAJOR for tool removals or re-shapes.

## Exit codes

CLI exit codes map the 8-code MCP error envelope to Unix-native semantics (locked in ADR-int-03 §Exit codes). The mapping is fixed and cannot be reshaped without a supersession ADR:

| Exit | Meaning | MCP envelope error code | Caller action |
|------|---------|-------------------------|---------------|
| 0 | Success | — (no error block) | Proceed |
| 1 | Validation failure (frontmatter malformed, schema violation) | `VALIDATION_FAILED` | Fix note content, re-run |
| 2 | Budget exceeded (cost cap hit per ADR-int-01 + ADR-int-05) | `BUDGET_EXCEEDED` | Wait for daily/monthly reset or raise caps |
| 3 | Visibility leak attempted (egress blocked per ADR-ai-02) | `VISIBILITY_LEAK` | Do NOT retry; investigate + file incident (primary security rail) |
| 4 | Missing dependency (Qdrant container offline, VAULT_AI_TOKEN unset, etc.) | `MISSING_DEPENDENCY` | Fix environment, re-run |

Codes 5-127 are reserved for future sub-command-specific semantics. Codes ≥128 indicate a wrapper or signal-handling issue (bash convention) and are not part of the vault-ai contract.

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

Ten sub-commands lock the CLI surface per ADR-int-03 §10 sub-commands verbatim. Each section below documents flags, purpose, output, sample usage, and the MCP tool it wraps.

### ws vault triage

**Purpose.** Trigger the inbox-triage agent over the `10_Inbox/` zone. Wraps MCP `triage_inbox` tool (tools.json row 6).

**Flags.**
- `--limit N` — process at most N inbox notes per invocation (default `10`).
- `--confidence-min X` — skip classifications with `confidence < X` where X is `[0.0..1.0]` (default `0.8`; Phase 5 D-34 triage_tool_allowlist default).
- `--dry-run` — compute classifications + proposed moves without writing frontmatter or moving files.
- `--since YYYY-MM-DD` — only consider inbox notes created on or after date.
- `--repo <sibling>` — triage pulls context from a sibling repo when the inbox note references cross-repo material (e.g. `--repo workspace-cli`).

**Output.** Per-note classification summary on stdout (one line per processed note with type + zone + visibility + confidence). NDJSON via `--json`.

**Sample.**
```
ws vault triage --limit 5 --confidence-min 0.85 --dry-run
# → 5 classifications printed, no frontmatter writes
```

### ws vault export

**Purpose.** Emit a tar.gz of notes filtered by visibility for external consumption (publishing public subset, collaborator sharing). Wraps MCP `export_notes` tool.

**Flags.**
- `--public` — include only `visibility: public` notes. Required flag for any export to occur (safety rail — default behaviour never exports any notes).
- `--out PATH` — target tar.gz path. Required when `--public` is set.

**Safety rail.** Without `--public` the command emits a stderr warning and exits 1 (prevents accidental mass-export of internal/private content). ADR-sec-01 tier semantics enforced structurally — public = default-deny until opt-in.

**Sample.**
```
ws vault export --public --out ~/exports/public-$(date +%F).tar.gz
# → tar.gz with N public notes + attachments; exit 0
```

### ws vault push-drive

**Purpose.** Push qualifying attachments to Google Drive per Phase 6 D-28 3-2-1 backup. Wraps MCP `backup_attachments` tool.

**Flags.**
- `--attachments-only` — push only `90_Attachments/` files >5 MB (Git LFS threshold; FLOW-05 lockfile).
- `--dry-run` — list upload candidates without transferring.

**Auth.** Google Drive OAuth token separately managed via chezmoi+age at `~/.config/gdrive/token.age` (not `VAULT_AI_TOKEN`). Pre-requisite: `chezmoi apply` has provisioned the Drive token.

**Sample.**
```
ws vault push-drive --attachments-only --dry-run
# → lists 3 candidates over 5 MB; no upload
```

### ws vault health

**Purpose.** Print the composite `vault_health_score` (Phase 6 D-24: 40% coverage + 30% content-sufficiency + 20% orphan-compliance + 10% link-integrity). Wraps MCP `vault_health` tool.

**Flags.** None (scalar output; future flags reserved for v2.2+).

**Output.** Single integer `0-100` on stdout (machine-parseable for cron + dashboard consumers).

**Sample.**
```
ws vault health
# → 78
```

### ws vault reindex

**Purpose.** Rebuild the Qdrant vector index per `_tooling/index-recipe.yaml` ([ADR-ai-03](../../vault-ai/docs/adr/adr-ai-03-vector-store.md)). Wraps MCP `reindex` tool.

**Flags.**
- `--full` — drop + reinitialise the collection (destructive; required for embedder-version bumps per ADR-ai-03 alias-swap procedure).
- `--model <name>` — override the recipe's embedder pin for a single invocation (testing/debugging; default reads from recipe).

**Output.** Progress counters on stderr (notes processed / chunks indexed / elapsed); final summary on stdout.

**Sample.**
```
ws vault reindex --full
# → re-indexes all notes under recipe's current embedder pin
```

### ws vault validate

**Purpose.** Run the Ajv + content-sufficiency validator suite over part of the vault. Wraps MCP `validate_note` tool per [ADR-ai-07](../../vault-ai/docs/adr/adr-ai-07-tooling-isolation.md).

**Flags.**
- `--type X` — restrict to a single note type (e.g. `--type tool`).
- `--zone Y` — restrict to a PARA zone (e.g. `--zone 30_Resources`).

**Output.** Per-note report on stdout: frontmatter issues + content-sufficiency label (sufficient/partial/stub) + any LINK-05 / FM-XX violations.

**Sample.**
```
ws vault validate --type adr
# → validates all adr-*.md files; exit 1 if any fail
```

### ws vault coverage

**Purpose.** Emit the coverage-linter report per ADR-obs-02 (Wave 3 ADR forward-ref; lands in Plan 06 of Phase 6). Wraps MCP `coverage_report` tool.

**Flags.**
- `--out PATH` — write markdown report to file (default stdout).

**Output.** Markdown table — sibling-repo entities extracted × vault IDs/aliases matched; MEDIUM/HIGH priority bands.

**Sample.**
```
ws vault coverage --out coverage-$(date +%Y-%m).md
# → monthly coverage report, markdown format
```

### ws vault restore

**Purpose.** Restore the vault from a snapshot (disaster-recovery drill + real restore). Wraps MCP `restore` tool.

**Flags.**
- `--from <snapshot>` — path or identifier of the snapshot (e.g. `backup-2026-Q2.tar.gz`).
- `--drill` — dry-run mode: validate snapshot integrity without overwriting working tree (quarterly DR drill per Phase 5 DR-04).

**Use-case.** Quarterly DR drill + real disaster-recovery after laptop loss / disk failure. CLI-first by design — DR drill cannot rely on MCP transport being operational at drill time.

**Sample.**
```
ws vault restore --from backup-2026-Q2.tar.gz --drill
# → validates snapshot; dry-run only; exit 0 if snapshot healthy
```

### ws vault init

**Purpose.** Bootstrap a fresh vault-ai workstation on a given host. No MCP required (chicken-and-egg: MCP needs the vault; init creates the vault).

**Flags.**
- `--host <devpod|macos|ios>` — one of three hosts; drives chezmoi template selection per Phase 6 D-20.

**Actions.**
1. `chezmoi apply` (host-appropriate template — `obsidian.{host}.tmpl`).
2. Install Obsidian plugins per host template (git, templater, etc.).
3. Prime `_tooling/index-recipe.yaml` with host-specific embedder pin.
4. Print next-step operator checklist (e.g., "now run `ws vault validate --type adr` to verify ADR integrity").

**Sample.**
```
ws vault init --host devpod
# → DevPod-specific bootstrap: git plugin + schema-tooling + obsidian theme
```

### ws vault stats

**Purpose.** Emit a summary readout: note counts by type, histogram, visibility distribution, vault_health_score composite. Wraps MCP `stats` tool.

**Flags.** None (monolithic summary; future flags reserved for v2.2+).

**Output.** Markdown-formatted readout on stdout:

```
vault-ai stats (vault: /home/vscode/projects/vault-ai, generated 2026-04-18T12:00:00Z)
  Total notes: 142
  By type: tool=23 person=12 concept=31 ...
  Visibility: private=89 internal=41 public=12
  vault_health_score: 78 (target v2.1=70)
```

**Sample.**
```
ws vault stats
# → full summary readout
```

## See also

- [ADR-int-03 — ws vault CLI Contract](../../vault-ai/docs/adr/adr-int-03-ws-vault-cli.md) — ADR-level lockfile for this spec
- [ADR-ai-01 — MCP Dual-Mode](../../vault-ai/docs/adr/adr-ai-01-mcp-dual-mode.md) — the MCP contract this CLI wraps
- [ADR-ai-06 — MCP Auth](../../vault-ai/docs/adr/adr-ai-06-mcp-auth.md) — VAULT_AI_TOKEN provisioning
- [ADR-sec-02 — Secrets Stack](../../vault-ai/docs/adr/adr-sec-02-secrets-stack.md) — chezmoi+age multi-recipient auth provisioning
- [`vault-ai/_tooling/mcp/contract/tools.json`](../../vault-ai/_tooling/mcp/contract/tools.json) — 23-tool MCP contract
- [`workflow-kit/.claude/settings.local.json`](../../workflow-kit/.claude/settings.local.json) — MCP registration surface

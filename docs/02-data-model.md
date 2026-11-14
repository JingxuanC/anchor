# Anchor Data Model (Proposed)

**Status:** design proposal. Schema names may change before implementation, but
the invariants and provenance rules are part of the requirement baseline.

## 1. Workspace layout

```text
.anchor/
  manifest.json                 # workspace identity + schema version
  config/
    anchor.toml                 # unified Anchor config (or config.json)
    agents.toml                 # adapter selections + capabilities cache
    rules/                      # source rules/templates
    mcp/                        # MCP server definitions (secret refs only)
    skills/                     # source skills (optional)
  events/                       # canonical append-only event log
    <ulid>-<device>.json
  projections/                  # derived, rebuildable
    TREE.md                     # human-readable tree
    state.json                  # machine snapshot/cache
  evidence/
    <evidence-id>.json          # immutable evidence metadata
  artifacts/
    <sha256>.<ext>              # optional synced bytes (bundles/patches)
    manifests/                  # artifact manifests + hashes
  sessions/
    <session-id>.json           # agent/session metadata + transcript pointer
  vault/
    refs.json                   # secret references/metadata only
  sync/
    state.json                  # remote refs, last sync, conflict records
  handoff/
    <handoff-id>.json           # frozen handoff packet manifest
  local/                        # gitignored, device-specific
    device.json                 # device id, local cache, queue
    locks/                      # workspace.lock, sync.lock
  .gitignore                    # ignores local/ and transient caches
```

**Canonical:** `events/`, `evidence/`, `sessions/` metadata, `config/`,
`vault/refs.json`, `handoff/` manifests, `sync/state.json`.
**Derived:** `projections/` (safe to regenerate; never merge line-by-line).
**Local-only:** `local/` (device id, pending MCP queue, locks, caches).
**Never present:** `causal.db`, secret values, full transcript copies by
default.

## 2. Manifest

```json
{
  "schema_version": 1,
  "workspace_id": "01J...",
  "name": "my-project",
  "created_at": "2026-11-15T00:00:00Z",
  "created_by": {"kind": "user", "id": "local-user"},
  "min_anchor_version": "0.1.0",
  "active_goal_id": "goal_01J...",
  "default_branch": "main",
  "sync": {
    "enabled": false,
    "remote": null,
    "mode": "dedicated-state-repo"
  }
}
```

- `schema_version` gates migrations.
- `workspace_id` is stable across devices; a new clone keeps it.
- `active_goal_id` is a projection of events, not mutable truth; it is
  convenience only.
- No absolute paths, usernames, hostnames or tokens.

## 3. Event envelope

Every state change is one immutable event file. Events are never edited; a
correction is a new event.

```json
{
  "schema_version": 1,
  "event_id": "01J9Z6Q8R7T0Y3M4N5P6Q7R8S9",
  "workspace_id": "01J...",
  "device_id": "dev_...",
  "sequence": 42,
  "recorded_at": "2026-11-15T00:00:00Z",
  "occurred_at": "2026-11-15T00:00:00Z",
  "actor": {
    "kind": "agent",
    "agent": "claude-code",
    "agent_version": "2.1.325",
    "session_id": "sess_...",
    "user_id": "local-user"
  },
  "type": "node_created",
  "idempotency_key": "hook:claude:sess:42:node_created",
  "correlation_id": "task_...",
  "causation_id": "evt_...",
  "expected_revision": 3,
  "payload": {}
}
```

Rules:

- `event_id` is time-sortable (ULID); `sequence` is per device and monotonic.
- `idempotency_key` is unique for mutating commands/hooks; replay returns the
  same result.
- `expected_revision` is required for updates to versioned entities; mismatch is
  a `conflict` error, not a blind overwrite.
- Unknown event types are preserved and ignored by older projections (forward
  compatibility).
- Events are size-capped; large content goes to `evidence/` or `artifacts/`
  with a hash reference.
- Events never contain secret values; redaction happens before append.

### Event types (initial set)

| Type | Purpose |
|---|---|
| `workspace_created` | Initial identity/schema. |
| `goal_set` | North star, constraints, success criteria, active goal. |
| `goal_updated` | Corrections; records provenance. |
| `node_created` | Decision/task/branch/question node. |
| `node_updated` | Title/body/decision/rationale/alternatives. |
| `status_changed` | Status transition with actor and reason. |
| `node_reparented` | Structural change with old/new parent. |
| `branch_proposed` / `branch_merged` / `branch_abandoned` | Branch lifecycle. |
| `node_tombstoned` | Soft delete; retains history. |
| `ownership_claimed` / `ownership_heartbeat` / `ownership_released` / `ownership_taken_over` | Leases and fencing. |
| `evidence_added` / `evidence_verified` / `evidence_rejected` | Evidence lifecycle. |
| `causal_link_proposed` / `causal_link_confirmed` / `causal_link_rejected` | Remote memory refs. |
| `session_started` / `session_ended` / `context_injected` | Capture metadata. |
| `handoff_prepared` / `handoff_acked` / `handoff_verified` | Resume lifecycle. |
| `config_updated` / `config_applied` | Unified config/rules rendering. |
| `sync_merged` / `sync_conflict` | Sync outcomes. |
| `secret_ref_added` / `secret_ref_removed` / `secret_accessed` | Vault metadata/audit (never values). |
| `policy_blocked` / `policy_warned` | Drift/security decisions. |

## 4. Session Tree entities

### 4.1 Goal

```json
{
  "id": "goal_01J...",
  "type": "goal",
  "title": "Ship Anchor handoff",
  "north_star": "A developer can resume work on another device/agent without re-explaining.",
  "constraints": {
    "must": ["Keep secrets out of context", "Use remote causal-memory MCP"],
    "must_not": ["Reimplement causal-memory", "Force-push"]
  },
  "success_criteria": [
    "Restore packet includes code state and evidence",
    "Successor verifies evidence before editing"
  ],
  "status": "in_progress",
  "provenance": "user_confirmed",
  "created_at": "...",
  "revision": 1
}
```

### 4.2 Node

```json
{
  "id": "node_01J...",
  "type": "decision",
  "goal_id": "goal_01J...",
  "parent_id": null,
  "title": "Use event sourcing",
  "body": "Append-only events + rebuilt projections.",
  "decision": {
    "chosen": "event sourcing",
    "alternatives": ["mutable state.json"],
    "rationale": "Git merges become additive; audit is intrinsic.",
    "consequences": ["replay cost", "projection cache"]
  },
  "status": "done",
  "provenance": "agent_inferred",
  "confidence": 0.7,
  "owner": {
    "agent": "claude-code",
    "session_id": "sess_...",
    "lease_id": "lease_...",
    "fencing_token": 4
  },
  "evidence_ids": ["ev_..."],
  "causal_ref_ids": ["cr_..."],
  "created_at": "...",
  "updated_at": "...",
  "revision": 5,
  "deleted": false
}
```

Node types: `goal`, `decision`, `task`, `branch`, `question`, `note`.
Statuses: `todo`, `in_progress`, `done`, `blocked`, `abandoned`.
`branch` nodes additionally carry `hypothesis`, `forked_from`, and
`merge_target`; `question` nodes carry `answer`/`resolved_by`.

### 4.3 Tree invariants

1. Each non-goal node has exactly one parent.
2. No cycles; parent depth is bounded by a configured maximum.
3. A branch forks from a goal/decision and cannot silently become the main line.
4. Status transitions are validated:
   `todo → in_progress → done|blocked|abandoned`,
   `blocked → in_progress|abandoned`,
   `done → in_progress` only with an explicit reopen event.
5. `done` requires at least one evidence reference or a recorded
   `user_override`.
6. `user_confirmed` provenance can only be set by an event with
   `actor.kind == "user"` (CLI interactive or explicit `--confirm`).
7. Tombstones remain in history; a tombstoned node cannot be edited until
   restored by an explicit event.

## 5. Ownership and concurrency

```json
{
  "lease_id": "lease_01J...",
  "node_id": "node_01J...",
  "owner": {"kind": "agent", "agent": "codex", "session_id": "..."},
  "fencing_token": 4,
  "acquired_at": "...",
  "expires_at": "...",
  "heartbeat_interval_seconds": 30,
  "state": "active"
}
```

- Acquiring a lease is an event; a new token increments the fencing token.
- Every mutation to an owned node includes the token and expected revision.
- Stale/expired leases can be taken over with an audited event; old tokens are
  rejected even if the old process wakes up later.
- Ownership is per active task, not per workspace; multiple agents may work on
  different branches concurrently.
- The UI shows owner, heartbeat age and expiry.
- Sync does not merge active ownership as a normal field; conflicts produce a
  `sync_conflict` requiring a takeover decision.

## 6. Evidence

```json
{
  "evidence_id": "ev_01J...",
  "type": "test",
  "node_id": "node_01J...",
  "source": {
    "kind": "command",
    "command": ["go", "test", "./..."],
    "cwd": ".",
    "exit_code": 0,
    "duration_ms": 1234
  },
  "artifacts": [
    {"kind": "log", "path": "artifacts/<sha256>.log", "sha256": "..."}
  ],
  "captured_at": "...",
  "captured_by": {"kind": "agent", "agent": "claude-code", "session_id": "..."},
  "verification": {
    "state": "unverified",
    "verified_by": null,
    "verified_at": null,
    "method": null
  },
  "redactions": ["env", "secret"]
}
```

Evidence types: `test`, `command`, `file`, `commit`, `diff`, `artifact`,
`url`, `manual`.
Rules:

- Evidence is immutable; corrections reference the original.
- A `done` transition references evidence IDs; a human override is a separate
  event with a reason.
- The successor's `handoff_verified` event references the code/evidence it
  checked (commit hashes, file hashes, test output hashes).
- A chat summary cannot be evidence; it may point to evidence.

## 7. Handoff packet and code state

```json
{
  "handoff_id": "ho_01J...",
  "workspace_id": "01J...",
  "created_at": "...",
  "created_by": {"kind": "user", "id": "local-user"},
  "goal": {"id": "goal_01J...", "north_star": "...", "constraints": {}},
  "active_node_id": "node_01J...",
  "open_branches": [{"id": "node_...", "hypothesis": "...", "owner": null}],
  "decisions": [{"id": "node_...", "summary": "...", "provenance": "user_confirmed"}],
  "next_steps": ["..."],
  "open_questions": [{"id": "q_...", "question": "...", "status": "open"}],
  "evidence": [{"id": "ev_...", "verified": true}],
  "code_state": {
    "repo_root": ".",
    "branch": "feat/x",
    "head": "abc123...",
    "dirty": true,
    "staged_diff_sha256": "...",
    "unstaged_diff_sha256": "...",
    "untracked_manifest_sha256": "...",
    "base_commit": "def456...",
    "bundle": {"path": "artifacts/<sha256>.bundle", "sha256": "..."},
    "patch": {"path": "artifacts/<sha256>.patch", "sha256": "..."},
    "changed_files": ["..."],
    "submodules": [{"path": "...", "status": "..."}]
  },
  "ownership": {"node_id": "...", "lease_id": "...", "state": "released"},
  "warnings": ["evidence ev_... is unverified", "artifact bundle missing"],
  "provenance_summary": {
    "user_confirmed": 4,
    "agent_inferred": 2,
    "unverified_claims": 1
  },
  "content_hash": "sha256:..."
}
```

- `handoff_prepared` freezes the packet; `handoff_acked` records successor
  acceptance; `handoff_verified` records code/evidence checks.
- Restore does not silently apply code changes. It prints a plan:
  - commit matches / diverges / missing;
  - dirty tree status;
  - files that would be overwritten;
  - required manual steps.
- `--apply-code` requires a clean tree or explicit `--force`, creates a backup
  ref/stash, and refuses path traversal/outside-workspace targets.
- Artifact bytes are optional for sync; manifests and hashes are mandatory.
- A chat summary is only one field (`session summary`), never the packet.

## 8. Session and capture metadata

```json
{
  "session_id": "sess_...",
  "agent": "codex",
  "agent_version": "...",
  "adapter_version": "anchor-adapter-codex/0.1",
  "started_at": "...",
  "ended_at": null,
  "transcript": {
    "kind": "external_path",
    "path": "/home/user/.codex/sessions/...",
    "sha256_at_capture": "...",
    "copy": "none"
  },
  "message_range": {"first_id": "...", "last_id": "..."},
  "summary": {
    "text": "...",
    "created_by": "agent",
    "confidence": 0.6,
    "source_refs": ["..."]
  },
  "capture_state": "partial",
  "redactions": ["secret", "env"]
}
```

- Transcript paths are machine-specific; the synced session record stores
  hashes/ranges and pointers, not absolute paths. A per-device mapping is local.
- `capture_state` is `full|partial|hook_gap`; restore must not call a partial
  capture complete.
- Summaries carry `created_by` and confidence; they are never promoted to
  `user_confirmed` automatically.

## 9. Causal references (remote memory only)

```json
{
  "causal_ref_id": "cr_01J...",
  "provider": "causal-memory",
  "remote_id": "mem_...",
  "relation": "caused",
  "status": "proposed",
  "decision_node_id": "node_...",
  "outcome_node_id": "node_...",
  "evidence_ids": ["ev_..."],
  "confidence": "evidence_backed",
  "created_by": {"kind": "agent", "agent": "claude-code"},
  "confirmed_by": null,
  "payload": {
    "decision": "...",
    "outcome": "...",
    "task_tag": "..."
  },
  "idempotency_key": "causal:node:...",
  "created_at": "..."
}
```

Rules:

- `status=proposed` is the default for agent-initiated writes.
- `status=confirmed` requires either `confirmed_by.kind == "user"` or at least
  one verified evidence reference plus an explicit confirmation event.
- `relation` is `caused|enabled|prevented|unknown`; `prevented` is a warning
  signal, not proof.
- Anchor stores no memory content beyond the payload needed for audit and the
  remote ID.
- Deleting a causal ref locally does not delete remote memory; it records a
  `causal_link_rejected` event and optionally a remote delete if the remote
  tool supports it. Remote deletion is always explicit.

## 10. Unified config and vault refs

`config/anchor.toml` (proposal):

```toml
schema_version = 1

[workspace]
name = "my-project"

[agents]
enabled = ["claude-code", "codex", "gemini"]

[hooks]
capture = true
drift_check = "warn"          # warn | block | off
require_handoff_ack = true

[causal_memory]
enabled = true
mcp_url = "https://causal-memory.com/memory/mcp"
auth = "vault:causal-memory/token"
timeout_ms = 8000
max_retries = 3
offline_queue = true

[sync]
enabled = false
remote = ""
mode = "dedicated-state-repo"

[vault]
backend = "auto"              # keychain | encrypted-file | auto
```

`vault/refs.json`:

```json
{
  "refs": [
    {
      "name": "causal-memory/token",
      "provider": "keychain",
      "key": "anchor:project:my-project:causal-memory/token",
      "scope": ["mcp:causal-memory"],
      "created_at": "...",
      "rotated_at": null
    }
  ]
}
```

- No values, no hashes of low-entropy secrets that could be brute-forced.
- Secret access emits an audit event with the ref name and purpose, never the
  value.

## 11. Deletion, tombstones and conflicts

- `node_tombstoned` is an event; projections mark the node deleted.
- Tombstones sync and prevent resurrection. A later `node_restored` is allowed
  only with an explicit user event.
- A concurrent edit and tombstone produce a `sync_conflict` record:
  - files: `sync/conflicts/<conflict-id>.json`
  - fields: node id, local/remote event ids, conflict type, resolution state.
- Resolution choices: `keep_local`, `keep_remote`, `merge_fields`, `restore`,
  `leave_conflicted`. Every resolution is an event.
- Projection conflicts (same event ID with different content, which should be
  impossible) are integrity errors; quarantine, never auto-merge.

## 12. Migration and compatibility

- Every file has `schema_version` (manifest and events).
- `anchor migrate --dry-run` reports changes; `anchor migrate` backs up
  `.anchor/` into `local/backups/<timestamp>/` before writing.
- Downgrade is not silently attempted: a newer schema refuses older binaries
  with an actionable error and a restore path.
- Event replay skips unknown event types and records a warning; unknown fields
  are preserved in the raw JSON.
- Projections are rebuilt from events after migration; they are never the
  source of truth.

## 13. Data integrity and privacy

- Content hashes (SHA-256) for artifacts, evidence, handoff packets and
  transcript snapshots.
- Atomic writes via temp file + rename; file modes 0600, directory 0700.
- Redaction markers are recorded without the secret.
- Retention policy is per workspace: transcripts, artifacts, queued MCP writes
  and evidence can be exported/deleted independently.
- No telemetry, no remote index, no hidden analytics database.

## 14. Open data-model questions

| ID | Question | Default proposal |
|---|---|---|
| O-011 | One active goal per workspace or multiple concurrent goals? | Multiple goals supported; one active for restore. |
| O-012 | Are branch nodes first-class in the tree or a separate DAG? | First-class nodes with `forked_from`/`merge_target`. |
| O-013 | Should `projections/` be committed or gitignored? | Commit `TREE.md` for review; gitignore `state.json`. |
| O-014 | How large can synced artifacts be, and where do bytes go? | Manifest in Git; bytes in a configured artifact store, size-capped. |
| O-015 | Should events be signed? | Optional per-device signing; verify if configured. |

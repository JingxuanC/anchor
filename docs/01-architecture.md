# Anchor Architecture (Proposed)

**Status:** design proposal for review. `[I]` marks engineering choices that are
not yet user-confirmed. Boundaries marked `[?]` remain open.

## 1. Design principles

1. **State, not chat.** The durable artifact is a structured event log plus
   evidence, not a transcript summary.
2. **Event-sourced and merge-friendly.** Append-only events make Git conflicts
   additive; structural/deletion conflicts are surfaced, not guessed.
3. **Local-first.** Capture, tree edits, restore and CLI work offline. Remote
   MCP and Git sync are optional and degrade visibly.
4. **Protocols are boundaries, not products.** Anchor is an MCP server to
   agents and an MCP client to causal-memory. It does not proxy model traffic or
   reimplement MCP.
5. **Untrusted by default.** Captured prompts, tool output, repository files,
   MCP responses and Git remotes are untrusted data. Instructions never gain
   authority just because they appear in data.
6. **Evidence before certainty.** Task completion, a chat summary, or an agent's
   claim is not proof. Evidence is recorded and verified by the successor.
7. **Secrets stay outside the model.** Values live in the OS keychain and are
   injected into explicitly authorized runtime calls; the workspace stores refs.
8. **One binary, many adapters.** A Go core embeds the web UI and ships as a
   single static-ish binary per platform; adapter capabilities are descriptors,
   not hard-coded assumptions.
9. **Roost is an edge, not a dependency.** A small runtime-provider interface
   keeps the integration extensible without touching existing Roost repos.

## 2. Stack decision

### Recommendation: Go core + TypeScript web UI

| Layer | Choice | Rationale |
|---|---|---|
| Core/CLI/daemon | Go 1.27+ | Fast startup (hooks run on every tool event), single cross-compiled binary, strong stdlib for file/Git/process/HTTP, official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`, v1.x, Apache-2.0), easy `go:embed` of web assets. |
| Web UI | TypeScript + React + Vite | Mainstream maintainability, rich graph/tree interaction, type-safe API client, static build embedded into the binary. |
| MCP | Official Go SDK | Server and client transports; no protocol reimplementation. Version pinned; transport shims only if a needed transport is missing. |
| Storage | JSON event log + Markdown projections | Human-readable/diffable, Git-mergeable, no hidden database, no causal DB. |
| Sync | Git CLI/library | Uses the user's existing Git identity/credentials; no bespoke sync server. |
| Vault | OS keychain via a Go keyring library | Platform-native secret storage; explicit fallback only. |

**Why not Rust core?** Rust is a reasonable alternative, especially for safety
and a single binary. Go wins on iteration speed, cross-compilation simplicity,
hook startup latency, the official MCP Go SDK, and a smaller contributor
learning curve for a workspace/CLI product. The design keeps the core behind
interfaces so a Rust core could be reconsidered without changing the data model.
**Decision status:** `[I]` proposed; architect/user review required. The
architect explicitly did not make Go+TS mandatory.

**Why not Node/TypeScript everywhere?** A Node runtime is a heavier install for
a CLI that runs inside every agent hook; Go gives a predictable single binary.
The web UI remains TypeScript.

## 3. System context

```text
┌──────────────────────┐   hooks / MCP / CLI    ┌─────────────────────────┐
│ Claude Code           │ ─────────────────────▶ │                         │
│ Codex CLI             │                        │  Anchor core            │
│ Gemini CLI            │ ◀───────────────────── │  (single Go binary)     │
└──────────────────────┘   context injection      │                         │
                                                 │  ┌───────────────────┐  │
┌──────────────────────┐   local HTTP (loopback) │  │ MCP server        │  │
│ Browser (web UI)      │ ◀───────────────────── │  │ MCP client        │  │
└──────────────────────┘                         │  │ adapters          │  │
                                                 │  │ event store       │  │
┌──────────────────────┐   Git (optional)        │  │ sync engine       │  │
│ Git remote            │ ◀───────────────────── │  │ config renderer   │  │
└──────────────────────┘                         │  │ vault             │  │
                                                 │  └───────────────────┘  │
┌──────────────────────┐   remote MCP (HTTPS)    │                         │
│ causal-memory MCP     │ ◀───────────────────── │                         │
└──────────────────────┘                         └─────────────────────────┘
```

Roost is **not** in the runtime path. The optional `RuntimeProvider` interface
is documented in section 11.

## 4. Components

### 4.1 CLI (`cmd/anchor`)

Subcommands (full scope, sequenced in delivery):

```text
anchor init | attach | status | doctor | version
anchor goal | tree | node | branch | claim | release | heartbeat
anchor capture | import | restore | handoff | ack
anchor sync | pull | push | resolve
anchor config | rules | mcp | skills | install | uninstall | migrate
anchor vault | secret | exec
anchor memory | lessons | record-outcome
anchor web
anchor wrap --agent <claude|codex|gemini> -- <agent command...>
```

- Human output by default, `--json` for stable machine output.
- Exit codes: 0 success; 2 usage; 3 auth/permission; 4 not found; 5 conflict;
  6 transient/retry; 7 upstream/network; 1 generic.
- `anchor doctor` checks: workspace schema, lock state, Git remote, adapter
  installation/version, hook config trust, MCP endpoint reachability, vault
  backend, secret scan.

### 4.2 Event store

- Canonical state = immutable event files under `.anchor/events/`.
- Events are named by sortable IDs (`<ULID>-<device-id>.json`) so Git merges
  are union-like and do not conflict on the same file.
- State is derived by replay; projections (`TREE.md`, `state.json`) are
  rebuildable and may be regenerated during merge.
- Local write lock avoids two processes interleaving read-modify-write.
- Per-node revisions and fencing tokens reject stale writes.
- Schemas are versioned; migrations are explicit commands with backups.

### 4.3 Adapter layer

```go
type Adapter interface {
    Name() string
    Detect(ctx) (Capabilities, error)
    Install(ctx, Options) (Plan, error)
    Uninstall(ctx, Options) error
    RenderInstructions(workspace) (FilePatch, error)
    RenderMCPConfig(workspace) (FilePatch, error)
    CaptureHook(ctx, HookInput) (HookOutput, error)
    RestoreContext(ctx, Packet) (Injection, error)
    SessionInfo(ctx) (SessionRef, error)
}
```

- Adapters are thin: they translate the agent's documented hook/context/MCP
  formats into Anchor events and render Anchor config into the agent's files.
- Capabilities are data (`hooks`, `context_injection`, `can_block_prompt`,
  `can_block_tool`, `mcp_stdio`, `mcp_http`, `rules_file`, `transcript_path`,
  `resume`, `fork`, limits). Enforcement is derived from these capabilities and
  reported as `enforced_within_wrapper|best_effort_hook|advisory|observed|unavailable`.
  Hooks can time out or return malformed output, so a hook can never be a total
  guarantee; only commands/writes routed through `anchor wrap` or an
  Anchor-mediated write path can be enforced within that boundary.
- `GenericAdapter` covers manual `anchor capture`/`anchor restore` and
  `anchor wrap` for agents without reliable hooks.
- Real adapters vs mocks are labeled in tests and `doctor` output.

### 4.4 MCP server (Anchor → agents)

- Transport: stdio by default (agent launches `anchor mcp serve`), optional
  loopback streamable HTTP for the web UI or integrations.
- Tools (proposed, versioned):
  - `anchor_status` — active goal, node, ownership, warnings.
  - `anchor_get_node` / `anchor_list_children`.
  - `anchor_create_node` / `anchor_update_status` (with provenance and
    evidence requirement).
  - `anchor_propose_branch` / `anchor_merge_branch` / `anchor_abandon_branch`.
  - `anchor_claim` / `anchor_release` (fencing token returned).
  - `anchor_record_evidence` / `anchor_verify_evidence`.
  - `anchor_search_lessons` / `anchor_propose_outcome` (remote MCP bridge).
  - `anchor_handoff_prepare` / `anchor_handoff_ack`.
- Tools never return secrets, never execute arbitrary shell strings, and
  validate paths against the workspace root.
- The agent's rules file tells it when to call these tools; the MCP server is
  not a hidden proxy.

### 4.5 causal-memory MCP client (Anchor → remote)

- Streamable HTTP MCP client using the official SDK.
- Token from vault or an explicit env var; never logged.
- Tool discovery via `tools/list`; **tool names are not hardcoded**. Anchor
  ships a capability mapping (`search`/`write` roles) that is validated against
  the live `tools/list`; a missing/renamed tool degrades explicitly. Names such
  as `search_memory`/`record_decision` are candidates only, not the contract.
- Reads: discovered read/allowlisted tools mapped to the search role.
- Writes: discovered write/allowlisted tools mapped to the write role; payload
  includes decision, outcome, relation, task tag, evidence IDs and a
  deterministic cross-device idempotency key.
- Anchor's MCP server exposes convenience memory tools, but agents may call the
  causal-memory MCP server directly. Anchor is **not** a mandatory memory proxy.
- Failure policy:
  - Timeouts + bounded retries with jitter; idempotency key stable.
  - Circuit breaker; `doctor`/UI show degraded state.
  - Bounded durable queue only for non-secret write payloads; user can inspect,
    flush or delete.
  - Reads fall back to cached last-known lessons with a stale marker; never
    block core work.
- Response handling: schema validation, size caps, treat content as untrusted
  data, never execute instructions from it.
- No local causal database, no `.anchor/causal/`, no Git sync of memory data.

### 4.6 Sync engine

- Uses Git CLI (or a library wrapper) against a configured remote.
- Canonical events are append-only, so most merges are unions of event files.
- Projections are regenerated after merge, not merged line-by-line.
- Structural conflicts (reparent, delete vs edit, ownership) are recorded in a
  conflict file and surfaced by CLI `sync resolve` and the web UI.
- Tombstones are events; deletion never relies on file absence.
- Git operations:
  - `fetch` → inspect divergence → `merge --no-ff` (or user-selected rebase)
    → conflict resolution → `push` without force.
  - Refuse to push to a remote with unexpected URL or protected branch.
  - Credentials through the user's Git credential helper; never logged.
- Uncommitted code:
  - `anchor handoff prepare` captures HEAD/branch, staged/unstaged diffs,
    untracked manifests, and a `git bundle`/patch artifact with hashes.
  - Artifacts live in `.anchor/artifacts/` (or a configured artifact store) with
    size limits; manifests are synced, bytes are synced only if configured.
  - Restore is dry-run first; dirty workspaces require `--force` with explicit
    confirmation and a backup ref.
- Sync topology default is a **dedicated private state repo/remote**, configurable; Anchor does not auto-create or auto-configure a remote. See `docs/06` O-001. Uncommitted artifact byte upload is **off by default**; only explicit configuration authorizes it. Missing bytes are `incomplete`, never a complete handoff.

### 4.7 Vault

- Backends: macOS Keychain; Windows Credential Manager/DPAPI; Linux Secret
  Service; explicit encrypted-file fallback.
- `vault` stores named secrets with scope metadata (project/agent/command).
- Injection modes:
  - Anchor-side calls (MCP to causal-memory, Git auth) read the value directly.
  - `anchor exec --secret NAME -- cmd argv...` injects only into that child
    process; no shell; output redaction; process-group timeout.
  - Preferred for agents: an Anchor MCP tool executes the configured command
    and returns only its redacted output.
- The workspace stores `vault/refs.json` with provider/key IDs and metadata, no
  values. Audit events record access without values.

### 4.8 Web UI

- `anchor web` starts a loopback-only HTTP server, opens a browser with a
  one-time token, and serves the embedded SPA.
- Views: tree/branches, node detail (decision/rationale/evidence/causal refs),
  ownership, sync/restore diff, evidence timeline, config drift, doctor.
- Security: loopback bind by default, random port, per-session bearer cookie,
  CSRF token, `Origin`/`Host` validation, CSP, no secret rendering, no CORS by
  default.
- Read-only mode for inspection without a writable workspace.

### 4.9 Roost runtime boundary

A narrow interface, not an implementation against live Roost services:

```go
type RuntimeProvider interface {
    Name() string
    Describe(ctx) (RuntimeInfo, error)       // environment, region, limits
    PrepareWorkspace(ctx, WorkspaceSpec) error
    HandoffHints(ctx, Packet) (map[string]string, error)
}
```

- No imports from Roost repos; no network calls by default.
- Optional metadata can be attached to handoff packets when a provider is
  configured.
- `[?]` Concrete contract to be reviewed with the Roost side later.

## 5. Key flows

### 5.0 SessionEnd budget and handoff preparation

`SessionEnd` is lightweight and must fit the adapter budget (Claude Code shares
1.5 s; Codex caps at 3 s; Gemini has its own timeout). It writes only a local
marker/checkpoint event and schedules no network or large artifact work.

Heavy handoff preparation (`git bundle`, patch, transcript hashing, artifact
export, sync) runs as an explicit `anchor handoff prepare` or a post-session
observable job with status `pending|running|complete|failed|timed_out`. A
timeout or interrupted process is never reported as complete; restore lists the
missing artifacts and marks `recovery_status=incomplete` (the unified enum is `complete|partial|incomplete|stale_code`). If no handoff was prepared before the next start, that start is explicitly `incomplete`, not “fresh/complete”.


### 5.1 Session start / restore

```text
agent hook (SessionStart)
  → anchor hook session-start --agent ... --session ...
  → load workspace + snapshot projection
  → resolve handoff packet (goal, active node, open branches, evidence, code state)
  → redact/size-cap
  → return adapter-specific additionalContext
  → write session_started + context_injected events
```

### 5.2 Capture

```text
UserPromptSubmit / PreToolUse / PostToolUse / Stop / PreCompact / SessionEnd
  → anchor hook <event> (stdin JSON from agent)
  → validate schema + agent/session identity
  → redact secrets, cap sizes
  → idempotent event append (event id + session + device_seq)
  → update ownership heartbeat if applicable
  → return allow/deny/context as the adapter permits
```

### 5.3 Handoff / successor verification

```text
anchor handoff prepare [--include-uncommitted]
  → freeze packet: goal/constraints, active node, branches, decisions,
    evidence refs, code state, artifacts, open questions, warnings
  → write handoff event + manifest hash
  → optional sync
successor session:
  → restore injects packet + explicit verification checklist
  → agent calls anchor_verify_evidence / records code check
  → anchor ack (verification event) before editing where the adapter can enforce it; otherwise the gate is advisory and visibly labeled
```

### 5.4 Causal bridge

```text
task start → search_memory(query from goal/node) → lessons (untrusted, labeled)
task end   → propose_outcome(decision, outcome, evidence_ids, idempotency_key)
           → remote MCP record_decision (if online)
           → local causal_ref event status=proposed
user/evidence verification → status=confirmed (or rejected)
```

## 6. Concurrency, deterministic replay and consistency

### 6.1 What is authoritative

`event_id` is a unique sortable ULID, but **ULID/time order is not an
authoritative replay order**. Authoritative order comes from causal metadata:

- `event_id`: unique identity only.
- `device_id` + `device_seq`: per-device monotonic sequence.
- `causal_parents`: event IDs this event causally follows (at least the event
  that established the base revision).
- `base_revision` / `expected_revision`: the node revision the event expects.
- `event_hash`: SHA-256 of the canonical event body.
- `lease_id` + `fencing_token`: required for owned-node mutations.

Replay builds a DAG, performs a deterministic topological sort (ties broken by
`event_id`), and never uses wall-clock time to decide precedence. Clock values
are descriptive only.

### 6.2 Same-revision concurrent events and ownership races

Two events that target the same `expected_revision` without a causal relation
are **concurrent siblings**, not sequential writes.

- The node stays at its base revision.
- A `conflict` record is created with both event IDs, devices, actors and
  payloads.
- Neither event is applied silently.
- Resolution is a new event (`conflict_resolved`) that references the
  conflicting events and the chosen/merged payload; it advances the revision.
- Projections surface the node as `conflicted` until resolution.
- The local write path prevents most of these with the workspace lock and
  optimistic checks; the conflict path covers offline/sync concurrency.

**Ownership is part of the isolation boundary.** If a node mutation is causally
unordered with *any* ownership event for that node (claim, heartbeat,
release, takeover), the whole mutation is isolated as a conflict — even if its
own lease looked active at its own causal point. A mutation is replayable only
when its `causal_parents` include the ownership event/version it relied on (or a
later causal barrier). Missing `causal_parents` means the event cannot be
safely replayed and stays quarantined/conflicted; it is not applied by guessing.
This prevents “each event is locally legal, so both take effect.”

### 6.3 Fencing and ownership conflicts

- A lease is acquired with a `lease_id` and a `fencing_token` that is monotonic
  within that lease lineage.
- A takeover references the prior `lease_id`, increments the fencing token, and
  is recorded as an event.
- Two concurrent takeover events for the same prior lease are a conflict, not a
  “highest token wins” race. A user must choose or a later explicit event must
  resolve the lineage.
- A mutation causally unordered with a takeover/ownership change is isolated as
  a whole, even if its `lease_id`/token looks valid at its own causal point.
  Isolation waits for a later causal barrier/ownership event; it cannot be
  replayed from partial causal metadata.
- A mutation is applied only if its lease is the unique active lease at its
  causal point. Stale tokens are rejected; events from an unresolved concurrent
  lease are isolated as conflicts.
- Ownership is per-node; different branches may have different owners.

### 6.4 Deterministic projection

Given the same event set and conflict-resolution events, every device must
produce byte-identical projections:

1. Validate event hashes and schemas; quarantine invalid events.
2. Build the causal DAG; reject cycles as integrity errors.
3. Topologically sort with the documented tie-breaker.
4. Apply non-conflicting events in order.
5. Materialize conflict records for concurrent same-revision events.
6. Serialize projections with stable key order and no machine-local data.

### 6.5 Mutable authority files

Manifest, config, vault refs, session metadata and handoff manifests are not
allowed to be unmergeable mutable authority. Their canonical changes are events;
the files are projections or event-tracked inputs with three-way merge rules
(detailed in `docs/02-data-model.md` §5). A plain event union is not enough for
these files; each has a declared merge/conflict policy.

### 6.6 Sync merge

- Event files are immutable; Git merge is a union of event files.
- Projections are regenerated, never line-merged.
- Structural conflicts (reparent, delete-vs-edit, ownership, same-revision
  siblings) become conflict records.
- Deletion is a tombstone event; no silent resurrection.
- No force-push; remote history is validated as untrusted input.

## 7. Error handling, degraded modes and enforcement boundaries

Two paths must be separated:

| Path | Default | Why |
|---|---|---|
| **Capture** | **Fail-open**: log a gap, keep the agent working, surface `capture_state=partial` in `doctor`/handoff. | Losing a capture event must not block the user's coding session. |
| **Mutation policy** | **Guaranteed only within a known boundary**: `enforced_within_wrapper` for writes routed through `anchor wrap`/Anchor-mediated calls; `best_effort_hook` where the adapter can deny but may time out/malform; otherwise `advisory`/`observed`/`unavailable`. | Hook timeout or malformed output may fail open. Anchor must not claim it intercepts every native edit, and must not add an MCP proxy to pretend it does. `anchor wrap` is the explicit fallback for a guarantee. |

| Failure | Behavior |
|---|---|
| causal-memory offline | Bounded write queue; stale/no lessons allowed; UI/doctor show degraded; core continues. |
| Git remote unavailable | Local work continues; sync status shows pending/offline; no blocking capture. |
| Hook timeout/malformed output | Capture gap logged; agent continues. A hook is `best_effort_hook`, never a hard guarantee; timeout/malformed output may fail open. `anchor wrap` is the enforceable fallback for writes it controls. |
| Adapter cannot block | `policy_enforcement=advisory|observed|unavailable`; handoff/UI warns; `anchor wrap`/Anchor-mediated writes provide the enforceable path. |
| Adapter missing | `anchor wrap`/manual capture/restore; `doctor` explains. |
| Vault unavailable | Secret-dependent actions fail closed; tree/capture still work. |
| Corrupt event | Quarantine, continue valid replay, `doctor` reports; no silent deletion. |
| Schema mismatch | Refuse writes, offer `anchor migrate --dry-run`; back up first. |
| Handoff artifact bytes missing | `recovery_status=incomplete`; restore lists missing artifacts and refuses to claim full recovery. |
| SessionEnd timeout | Lightweight marker remains; handoff preparation stays `pending|timed_out`, and the next start is `incomplete`; never marked complete. |

## 8. Repository layout (proposed)

```text
anchor/
  cmd/anchor/                # main binary
  internal/
    cli/                     # command surface, JSON output, exit codes
    workspace/               # manifest, locks, paths
    events/                  # event store, replay, projections
    tree/                    # goals/nodes/branches/ownership/constraints
    handoff/                 # restore packets, code state, verification
    adapters/
      claude/ codex/ gemini/ generic/
      contract/              # shared capability + hook contracts
    mcp/
      server/                # Anchor MCP tools
      causalmemory/          # remote MCP client + offline queue
    sync/                    # Git sync, merge, conflicts, tombstones
    config/                  # unified config, rendering, drift
    vault/                   # keychain backends, injection, redaction
    evidence/                # evidence store and verification
    security/                # path/command validation, redaction primitives
    runtime/                 # Roost boundary interface (stub)
    web/                     # local HTTP server + embedded assets
  web/                       # TypeScript/React/Vite source
  docs/                      # this design baseline + user/security docs
  acceptance/                # requirement/test matrix, fixtures
  packaging/                 # goreleaser, install scripts, systemd/launchd? 
  .github/workflows/         # CI
```

## 9. Packaging and operations

- `goreleaser` (or equivalent) builds static-ish binaries for macOS
  (amd64/arm64), Linux (amd64/arm64), Windows (amd64); web assets embedded.
- Release artifacts: checksums, signatures, SBOM, install script, Homebrew tap
  (optional), winget/scoop (optional).
- `anchor install` writes adapter hooks/MCP/rules with backups and a manifest of
  managed files; `anchor uninstall` reverses only managed files.
- `anchor migrate` handles schema upgrades with backup and rollback.
- No self-update in v1 unless a signed updater is designed; package managers
  handle upgrades.

## 10. Testing strategy (summary)

- Unit: event replay, tree invariants, constraints, path/command validation,
  redaction, config renderers, sync merge.
- Integration: temp workspace + real Git + mock MCP server + adapter hook
  contract fixtures.
- E2E: real Claude Code installed in a temp project (available in this
  environment); Codex/Gemini e2e only when the CLIs are installed and
  authenticated; otherwise contract tests with explicit skip labels.
- Security: path traversal/symlink, command injection, secret leakage,
  prompt-injection corpora, CSRF/Origin, Git conflict/force-push refusal.
- Real vs mock: every test declares `real`, `recorded`, or `fake`; a mock may
  not be reported as a live adapter in `doctor` or demo output.
- Performance: hook latency benchmark, restore packet size/latency, UI bundle
  and first paint.
- Full mapping: `docs/05-acceptance-mapping.md` and `acceptance/matrix.yaml`.

## 11. Roost boundary (explicitly out of runtime scope)

- Define `internal/runtime` interface and config docs only.
- No imports of Roost code, no calls to market/roostbot/LangAlpha endpoints.
- Optional runtime metadata may appear in handoff packets behind a feature flag.
- A future provider implementation can be added without touching core.

## 12. Risks

| Risk | Mitigation |
|---|---|
| Hook/config formats change | Runtime capability detection, versioned adapters, contract tests, graceful fallback. |
| Context window limits | Packet size budgets, tiered restore (summary → full), per-adapter caps. |
| Git conflicts | Event-sourced union, tombstone semantics, conflict UI, no force-push. |
| Secret leakage via prompt/tool output | Exact-value redaction, vault refs only, output caps, secret scans, e2e leak tests. |
| MCP remote outage | Queue/retry/circuit breaker, stale cache, visible degraded state. |
| Concurrent agents | Lease/fencing, per-node revisions, ownership UI. |
| Uncommitted changes lost on restore | Dry-run, backup refs, explicit force, artifact hashes. |
| Summary mistaken for recovery | Handoff packet requires evidence/open questions; successor verification event. |
| Scope creep into model routing | Non-goals enforced in review and requirements coverage. |

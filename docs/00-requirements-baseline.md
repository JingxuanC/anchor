# Anchor Requirements Baseline

**Status:** review draft for architect/user sign-off  
**Date:** 2026-11-15  
**Sources:** `Anchor-PRD-v0.1.md`, `-v0.2.md`, `-v0.3.md`, and the latest
product decisions relayed by the architect on 2026-11-15.  
**Rule:** the latest user decision wins over any PRD line that conflicts with it.
Engineering proposals never silently become user requirements.

## 0. Provenance legend

| Tag | Meaning |
|---|---|
| `[U-R]` | User-confirmed product intent, relayed through the architect. Recorded here; not a substitute for direct approval of irreversible actions. |
| `[U-P]` | Explicit in the PRD (v0.1–v0.3). |
| `[A]` | Architect instruction for this delivery. |
| `[I]` | Engineering inference or proposed design; requires review. |
| `[?]` | Open boundary; must not be silently decided. |

The baseline separates **confirmed intent** (section 2) from **engineering
proposals** (sections 5–8 and the architecture documents).

## 1. Source precedence and conflict resolution

| Topic | Older PRD text | Latest decision (wins) |
|---|---|---|
| Delivery shape | “MVP 必须有 / 可后置” (v0.1–v0.3) | `[U-R]` No shrunken MVP and no self-removed scope. Full product scope below; implementation phases are sequencing only. |
| Memory engine | causal-memory is Rust + SQLite, `.anchor/causal/causal.db` | `[U-R]` causal-memory is an independent project consumed through its **remote MCP** endpoint. Do not embed it, do not add a Rust/SQLite engine to Anchor, do not sync `causal.db` through Git, do not reimplement the engine. |
| Causal writes | “节点完成时 → 自动向 causal-memory 写入一条因果边” | `[U-R]` Task completion must **not** automatically assert causality. Anchor may propose a `decision → outcome` link; it becomes a conclusion only with evidence or explicit user confirmation. |
| Protocol | “协议不互通” as a product problem | `[U-R]` MCP is the tool integration protocol. Do not rebuild MCP or force all traffic through an Anchor proxy. Anchor is an MCP server to agents and an MCP client to causal-memory. |
| Runtime environment | Roost appears adjacent to the product | `[U-R]` Keep only a clear, extensible Roost runtime-integration **boundary**. Do not modify `roostbot`, `LangAlpha`, or `market` repositories now. |
| Stack | Go CLI + Rust causal-memory | `[A]` Go core + TS Web is a discussion suggestion, not a hard constraint. Justify the stack on maintainability. Proposal: Go + TypeScript, section 5. |
| Sync scope | Tree + `causal.db` via Git | `[U-R]` Git sync covers Anchor state/config/rules/artifact metadata, never a causal database. |

## 2. Confirmed product decisions

These are the decisions the design must satisfy. Changing them requires the user,
not an implementation convenience.

| ID | Decision | Source |
|---|---|---|
| D-001 | Anchor is a personal-developer agent workspace, not a cloud multi-user product. | `[U-P]` |
| D-002 | Full scope: cross Claude Code/Codex/Gemini CLI handoff, cross-device sync, Session Tree, goal constraints and branch management, capture/restore, long-term experience, unified config/rules, secret management, CLI and visualization. | `[U-R]` |
| D-003 | Do not reduce scope to an MVP. Delivery can be phased, but every listed capability remains in the release contract. | `[U-R]` |
| D-004 | causal-memory remains independent and is used over remote MCP only. | `[U-R]` |
| D-005 | No causal DB in the workspace, no Git sync of causal.db, no local memory-engine reimplementation. | `[U-R]` |
| D-006 | MCP is used as the tool protocol; do not reimplement MCP or force an Anchor relay/proxy. | `[U-R]` |
| D-007 | Causal conclusions require evidence or explicit confirmation; task completion alone is never evidence. | `[U-R]` |
| D-008 | Chat summaries are not a complete recovery. Full handoff includes code state, evidence, open questions and provenance. | `[U-R]` |
| D-009 | A successor agent must verify code and evidence before continuing. | `[U-R]` |
| D-010 | Secrets never enter model context, transcripts, logs, or synced files; store references only. | `[U-P]` + `[U-R]` |
| D-011 | Same-task concurrent ownership must be prevented or detected, not left to last-writer-wins. | `[U-R]` |
| D-012 | Roost integration is a boundary only; no changes to existing Roost repos in this delivery. | `[U-R]` |
| D-013 | No real payments and no real production keys in development, tests, demos, or fixtures. | `[A]` |
| D-014 | Real adapters and mocks must be visibly distinguished; mocks may not masquerade as a working product. | `[A]` |
| D-015 | Tests must cover unit, integration and e2e; CI is a required deliverable. | `[A]` |
| D-016 | Deliver install packaging, migration/upgrade, uninstall, security docs, usage docs, requirement coverage and known limitations. | `[A]` |

## 3. Product goals and non-goals

### Goals

| ID | Goal | Source |
|---|---|---|
| G-001 | Carry the current task structure across agents and devices so work resumes without re-explaining. | `[U-P]` |
| G-002 | Prevent goal drift with an explicit goal/constraints tree and branch discipline. | `[U-P]` |
| G-003 | Preserve decision→outcome experience for retrieval after context compression. | `[U-P]` |
| G-004 | Keep credentials out of model context while still letting authorized commands use them. | `[U-P]` |
| G-005 | Give the user a visible, inspectable tree, evidence trail, sync state and ownership state. | `[U-P]` |
| G-006 | Make every resume auditable and verifiable, not a plausible-sounding summary. | `[U-R]` |

### Non-goals (confirmed)

| ID | Non-goal | Source |
|---|---|---|
| NG-001 | A model router, LLM proxy, or subscription-sharing gateway (Magpie/New-API territory). | `[U-P]` |
| NG-002 | Cloud multi-user collaboration or enterprise RBAC. | `[U-P]` |
| NG-003 | Reimplementing causal-memory, its graph engine, or its database. | `[U-R]` |
| NG-004 | Syncing `causal.db` through Git or embedding it in the workspace. | `[U-R]` |
| NG-005 | Rebuilding the MCP protocol or requiring all agent traffic to pass through Anchor. | `[U-R]` |
| NG-006 | Modifying the existing Roost/LangAlpha/market repositories in this project. | `[U-R]` |
| NG-007 | Real payment processing or real production secret material. | `[A]` |

## 4. Functional requirements

Every requirement below is in the **full release scope**. The phase column in
`docs/06-decisions-and-open-questions.md` is sequencing, not a scope cut.

### 4.1 Workspace core and data format

| ID | Requirement | Source |
|---|---|---|
| REQ-WS-001 | `anchor init` creates/attaches a workspace in any project; identity and schema version are recorded. | `[U-P]` |
| REQ-WS-002 | Canonical state is human-readable, diffable, deterministic and Git-friendly. | `[U-P]` |
| REQ-WS-003 | Schema versions have explicit migration, backup, dry-run and rollback behavior. | `[A]` |
| REQ-WS-004 | Writes are atomic and crash-safe; local writers use a workspace lock. | `[I]` |
| REQ-WS-005 | Synced data contains no absolute machine paths; device-specific data is local-only. | `[I]` |
| REQ-WS-006 | Derived indexes/projections are rebuildable from canonical state; hidden state is not allowed. | `[I]` |
| REQ-WS-007 | Workspaces can be exported, inspected and imported without Anchor. | `[A]` |
| REQ-WS-008 | Uninstall removes integration files but preserves workspace data unless `--purge` is explicit. | `[A]` |

### 4.2 Session Tree

| ID | Requirement | Source |
|---|---|---|
| REQ-ST-001 | Each workspace has one or more goals; the active goal has a north star, must/must-not constraints and success criteria. | `[U-P]` + `[U-R]` |
| REQ-ST-002 | Decision nodes store decision, rationale, alternatives, consequences and provenance. | `[U-P]` |
| REQ-ST-003 | Task nodes use the statuses `todo`, `in_progress`, `done`, `blocked`, `abandoned`. | `[U-P]` |
| REQ-ST-004 | Branch nodes fork from a goal/decision, carry a hypothesis, and can be merged or abandoned. | `[U-R]` |
| REQ-ST-005 | Every node records provenance: `user_confirmed`, `agent_inferred`, `imported`; inferences carry confidence. | `[U-R]` |
| REQ-ST-006 | Nodes record actor, agent type/version, session and timestamps. | `[U-P]` |
| REQ-ST-007 | Changes are append-only events with stable IDs, revisions and idempotency keys. | `[I]` |
| REQ-ST-008 | Deletion is a tombstone; a concurrent edit-vs-delete is a surfaced conflict, never silent resurrection. | `[U-R]` |
| REQ-ST-009 | Tree invariants: one parent, no cycles, valid status transitions, branch lineage preserved. | `[I]` |
| REQ-ST-010 | Drift checks warn when a proposed node/action is outside the goal or violates a constraint. The warning is advisory unless a policy hook explicitly blocks. | `[U-R]` |
| REQ-ST-011 | Users can correct, merge, reparent, abandon and annotate nodes; an agent cannot mark a node `user_confirmed` by itself. | `[U-R]` |
| REQ-ST-012 | Chat/message summaries are stored as summaries with source pointers and hashes, not as the canonical history. | `[U-R]` |

### 4.3 Capture / Collector

| ID | Requirement | Source |
|---|---|---|
| REQ-CAP-001 | Native hook capture for Claude Code, Codex and Gemini CLI: session start/end, prompt, tool before/after, stop, compaction. | `[U-R]` |
| REQ-CAP-002 | Capture is side-effect-minimal and fail-open for the agent unless a configured policy hook explicitly blocks. | `[I]` |
| REQ-CAP-003 | Captured content is redacted for secrets and size-capped before persistence. | `[U-R]` |
| REQ-CAP-004 | Store transcript/session pointers and hashes; full transcripts are not copied into the tree by default. | `[U-R]` |
| REQ-CAP-005 | Adapter events normalize into one versioned Anchor event schema. | `[I]` |
| REQ-CAP-006 | Ingestion is idempotent per session/event and safe to replay. | `[I]` |
| REQ-CAP-007 | Capture works offline and does not require causal-memory or a network. | `[I]` |
| REQ-CAP-008 | Tool inputs/outputs are captured only within configurable limits; secrets and denied paths are dropped. | `[U-R]` |
| REQ-CAP-009 | Capture failures are visible in `anchor doctor` and logs, not silently lost. | `[I]` |

### 4.4 Restore / handoff

| ID | Requirement | Source |
|---|---|---|
| REQ-RST-001 | A handoff packet contains goal/constraints, active node, open branches, decisions, next steps, evidence, warnings, ownership and code state. | `[U-R]` |
| REQ-RST-002 | Injection uses each adapter's supported context channel and respects its size limits. | `[I]` |
| REQ-RST-003 | The successor agent must acknowledge the handoff and verify referenced code/evidence before editing. | `[U-R]` |
| REQ-RST-004 | A chat summary alone is never labeled or treated as full recovery. | `[U-R]` |
| REQ-RST-005 | Handoff works across Claude Code, Codex and Gemini CLI in both directions. | `[U-R]` |
| REQ-RST-006 | Handoff works across devices after Git sync. | `[U-P]` |
| REQ-RST-007 | Uncommitted code handoff captures branch/HEAD, staged/unstaged/untracked changes, diff/bundle hashes and restore instructions. | `[U-R]` |
| REQ-RST-008 | Restore is plan/dry-run first; applying to a dirty tree requires explicit confirmation and never silently overwrites. | `[U-R]` |
| REQ-RST-009 | Restore reports missing artifacts, stale commits and unreachable evidence rather than guessing. | `[I]` |
| REQ-RST-010 | The packet labels user-confirmed facts, agent inferences, and unverified claims. | `[U-R]` |

### 4.5 Adapters

| ID | Requirement | Source |
|---|---|---|
| REQ-AD-001 | Detect installed agent CLI and version; record capabilities and degrade explicitly. | `[I]` |
| REQ-AD-002 | Claude Code adapter supports hooks, MCP registration, rules/instructions, config/install/uninstall. | `[U-R]` |
| REQ-AD-003 | Codex adapter supports hooks, MCP registration, `AGENTS.md`, config/install/uninstall. | `[U-R]` |
| REQ-AD-004 | Gemini CLI adapter supports hooks, MCP registration, `GEMINI.md`/extensions, config/install/uninstall. | `[U-R]` |
| REQ-AD-005 | If hooks are unavailable, provide `anchor wrap` and/or manual capture/restore; never pretend hooks work. | `[I]` |
| REQ-AD-006 | Adapter install is idempotent, backs up existing files, preserves user content and supports dry-run. | `[I]` |
| REQ-AD-007 | Adapter capability descriptors are versioned; no cross-agent assumptions. | `[I]` |
| REQ-AD-008 | Tests/UI label real adapters vs mocks/recorded fixtures. | `[A]` |
| REQ-AD-009 | Hook commands use argv arrays, absolute executable paths, minimal env and bounded timeouts. | `[U-R]` |
| REQ-AD-010 | Register Anchor MCP tools without forcing a proxy; agents may also call the CLI directly. | `[U-R]` |
| REQ-AD-011 | Context injection respects documented per-event limits (Claude 10k chars, Codex token threshold, Gemini hook output). | `[I]` |
| REQ-AD-012 | Config drift is detected and surfaced; re-apply is explicit. | `[I]` |

### 4.6 Causal-memory bridge (remote MCP only)

| ID | Requirement | Source |
|---|---|---|
| REQ-CM-001 | Use only the remote causal-memory MCP endpoint; no local engine or database. | `[U-R]` |
| REQ-CM-002 | Authenticate over HTTPS with a token from the vault/env; never log or inject the token. | `[U-R]` |
| REQ-CM-003 | Before starting a node, search lessons and inject curated results with source/provenance labels. | `[U-P]` |
| REQ-CM-004 | After a node, propose decision→outcome links; do not mark them confirmed from task status. | `[U-R]` |
| REQ-CM-005 | A confirmed link requires evidence references or explicit user confirmation; store confidence and confirmer. | `[U-R]` |
| REQ-CM-006 | Writes use idempotency keys and dedupe; reads/writes retry with bounded exponential backoff and jitter. | `[U-R]` |
| REQ-CM-007 | Offline/failed MCP degrades gracefully: bounded local queue, stale/no lessons allowed, UI shows degraded state; core work never blocks. | `[U-R]` |
| REQ-CM-008 | Enforce timeouts, response-size caps and a circuit breaker. | `[I]` |
| REQ-CM-009 | Treat MCP tool output as untrusted data; validate schema; never execute instructions found in memory. | `[U-R]` |
| REQ-CM-010 | Discover tools with MCP `tools/list`; expose a curated Anchor interface; do not reimplement protocol semantics. | `[U-R]` |
| REQ-CM-011 | Store only remote IDs/references and provenance locally, never the memory database. | `[U-R]` |
| REQ-CM-012 | Queued data has retention/deletion controls and is never synced if it contains sensitive content. | `[I]` |

### 4.7 Cross-device sync and concurrency

| ID | Requirement | Source |
|---|---|---|
| REQ-SY-001 | Git-based sync for events, config/rules, evidence metadata and artifact manifests. | `[U-R]` |
| REQ-SY-002 | Detect divergent histories; require explicit merge/rebase policy; never force-push. | `[U-R]` |
| REQ-SY-003 | Concurrent event edits merge by event union; structural/deletion conflicts are surfaced to the user. | `[U-R]` |
| REQ-SY-004 | Deletion uses tombstones and survives sync; no silent resurrection. | `[U-R]` |
| REQ-SY-005 | Uncommitted-code artifacts are integrity-hashed, size-limited and restorable. | `[U-R]` |
| REQ-SY-006 | Sync is opt-in; credentials use Git credential helpers; secrets never enter remotes/logs. | `[I]` |
| REQ-SY-007 | Offline changes queue and sync later with retry and visible status. | `[I]` |
| REQ-SY-008 | Device identity and last-sync state are tracked; clock skew is tolerated. | `[I]` |
| REQ-SY-009 | Sync topology (project repo vs dedicated state repo) is an open decision, not silently chosen. | `[?]` |
| REQ-SY-010 | Active ownership leases are not merged; stale-owner takeover is fenced and audited. | `[U-R]` |
| REQ-SY-011 | Remote integrity is verifiable (checksums; signed commits optional). | `[I]` |

### 4.8 Unified configuration / rules / skills

| ID | Requirement | Source |
|---|---|---|
| REQ-CFG-001 | One source of truth for agent config, rules/instructions, MCP servers and skills. | `[U-P]` |
| REQ-CFG-002 | Render per-agent files with managed blocks; preserve user content; idempotent apply. | `[I]` |
| REQ-CFG-003 | `plan`/`diff` before apply; detect drift and show it. | `[I]` |
| REQ-CFG-004 | Secret values are references, never rendered into files. | `[U-R]` |
| REQ-CFG-005 | Project/user scopes and precedence are documented and tested. | `[I]` |
| REQ-CFG-006 | Config versions can be rolled back. | `[I]` |
| REQ-CFG-007 | The rendered rules instruct agents to use Anchor tools at start, completion and branch points. | `[U-P]` |

### 4.9 Vault and secret handling

| ID | Requirement | Source |
|---|---|---|
| REQ-VT-001 | OS keychain backends (macOS Keychain, Windows Credential Manager/DPAPI, Linux Secret Service); encrypted-file fallback only if explicit. | `[I]` |
| REQ-VT-002 | Workspace stores secret references/metadata only. | `[U-R]` |
| REQ-VT-003 | Runtime injection never puts the secret in model context, transcript, log or sync payload. | `[U-R]` |
| REQ-VT-004 | Redaction runs on capture, logs, MCP output and UI. | `[U-R]` |
| REQ-VT-005 | Rotation/revocation updates references and invalidates old material. | `[U-P]` |
| REQ-VT-006 | Access is scoped by project/secret/agent; least privilege by default. | `[U-R]` |
| REQ-VT-007 | No secret in argv; pass via stdin/fd/0600 temp file and clean up. | `[U-R]` |
| REQ-VT-008 | Audit records access without the value. | `[I]` |
| REQ-VT-009 | Secret scanning runs in `anchor doctor`, pre-commit guidance and CI. | `[A]` |
| REQ-VT-010 | Tests use fake secrets only and label them; no real keys. | `[A]` |

### 4.10 CLI and visualization

| ID | Requirement | Source |
|---|---|---|
| REQ-UI-001 | CLI covers init/status/tree/branch/claim/release/capture/restore/sync/config/vault/memory/doctor/web/install/uninstall/migrate. | `[I]` |
| REQ-UI-002 | Stable machine-readable JSON and exit codes. | `[U-P]` |
| REQ-UI-003 | Human output is readable, color/TTY-aware and actionable. | `[I]` |
| REQ-UI-004 | Web UI shows tree/branches, node details, evidence, causal references, ownership, sync/restore diff. | `[U-R]` |
| REQ-UI-005 | Web UI binds loopback by default with auth, CSRF and Origin/Host checks. | `[I]` |
| REQ-UI-006 | UI never renders secret values or raw transcript secrets. | `[U-R]` |
| REQ-UI-007 | UI supports read-only mode and degrades if the local service is unavailable. | `[I]` |

### 4.11 Ownership and evidence

| ID | Requirement | Source |
|---|---|---|
| REQ-CONC-001 | Exactly one active owner per task; ownership is a lease with heartbeat and fencing token. | `[U-R]` |
| REQ-CONC-002 | Stale owners can be taken over after expiry with an audit event; old fencing writes are rejected. | `[I]` |
| REQ-CONC-003 | Per-node optimistic concurrency (`revision`/etag) detects lost updates. | `[I]` |
| REQ-CONC-004 | Ownership state is visible in CLI, UI and handoff packets. | `[I]` |
| REQ-EVID-001 | Evidence entries store type, source, hash, command/exit status, timestamp, actor and verification state. | `[U-R]` |
| REQ-EVID-002 | A node can be marked done only with evidence or an explicit user override recorded as such. | `[U-R]` |
| REQ-EVID-003 | The successor verifies code/evidence and records a verification event before continuing. | `[U-R]` |
| REQ-EVID-004 | Causal outcome links require evidence or user confirmation, never task status alone. | `[U-R]` |
| REQ-EVID-005 | Evidence is immutable; corrections are new events referencing the old IDs. | `[I]` |
| REQ-EVID-006 | Chat summaries are not evidence. | `[U-R]` |

### 4.12 Roost integration boundary

| ID | Requirement | Source |
|---|---|---|
| REQ-RT-001 | Define a runtime-provider interface (environment metadata, optional launch context) with no hard dependency. | `[U-R]` |
| REQ-RT-002 | Do not modify `roostbot`, `LangAlpha`, or `market` repositories. | `[U-R]` |
| REQ-RT-003 | Core Anchor works without Roost; integration is opt-in and feature-flagged. | `[I]` |

## 5. Non-functional and delivery requirements

| ID | Requirement | Source |
|---|---|---|
| REQ-NFR-001 | macOS, Linux and Windows support; no platform-specific state in synced data. | `[A]` |
| REQ-NFR-002 | Hook capture adds bounded latency (target p95 < 150 ms excluding MCP network; restore packet < 1 s locally; UI first paint < 2 s). Targets are proposals to validate. | `[I]` |
| REQ-NFR-003 | Offline-first: core capture/tree/restore works with no network; remote features degrade visibly. | `[U-R]` |
| REQ-NFR-004 | No telemetry by default; no analytics endpoint; logs are local and redacted. | `[U-R]` |
| REQ-NFR-005 | User-controlled retention/export/delete for transcripts, artifacts and queued data. | `[I]` |
| REQ-NFR-006 | Deterministic, testable modules with clear boundaries; no `unsafe` in Go core. | `[I]` |
| REQ-NFR-007 | Accessibility: keyboard navigation and non-color-only status in the web UI. | `[I]` |
| REQ-OPS-001 | Install packages with checksums/signatures for supported platforms. | `[A]` |
| REQ-OPS-002 | Upgrade/migrate with backup, dry-run and rollback; downgrade behavior documented. | `[A]` |
| REQ-OPS-003 | Uninstall by default removes integrations and preserves data; `--purge` destroys data after confirmation. | `[A]` |
| REQ-OPS-004 | CI runs format/lint/unit/integration/contract/e2e/security/coverage gates. | `[A]` |
| REQ-OPS-005 | User, security, architecture, migration, uninstall, requirement-coverage and known-limitations docs ship with the release. | `[A]` |
| REQ-OPS-006 | Reproducible builds where practical; SBOM and dependency pinning; no unvetted postinstall. | `[I]` |
| REQ-OPS-007 | No real payment integration, no real production secrets in any artifact or test. | `[A]` |
| REQ-OPS-008 | Roost boundary documented but not implemented against live Roost services. | `[U-R]` |

## 6. Deliverables

| ID | Deliverable | Source |
|---|---|---|
| DEL-001 | Source code (Go core, TypeScript web, embedded assets). | `[A]` |
| DEL-002 | Installers/packages for supported platforms plus checksums. | `[A]` |
| DEL-003 | Migration, upgrade and uninstall flows. | `[A]` |
| DEL-004 | Security model and operational security guide. | `[A]` |
| DEL-005 | User guide and CLI reference. | `[A]` |
| DEL-006 | CI pipeline with the test layers in `docs/05-acceptance-mapping.md`. | `[A]` |
| DEL-007 | Requirement-coverage report and known-limitations list. | `[A]` |
| DEL-008 | Draft PRs on a feature branch; no merge, no force-push, no direct writes to `main` without approval. | `[A]` |

## 7. Open questions (must not be silently decided)

| ID | Question | Why it matters | Default proposal |
|---|---|---|---|
| O-001 | Should `.anchor/` sync in the project Git repo, or in a dedicated state Git repo/orphan branch? | Affects privacy, merge conflicts, uncommitted-code artifacts and onboarding. | Separate state repo recommended; requires user confirmation. |
| O-002 | Which platforms are release-blocking for v1 (macOS, Linux, Windows; architectures)? | Packaging and keychain test matrix. | macOS + Linux first; Windows supported with CI build, e2e gated. |
| O-003 | Which exact agent CLI versions are supported? | Hook/config formats change. | Runtime capability detection + latest stable at release; minimum versions from adapter spike. |
| O-004 | Does the user want workspace state committed to the project repo at all? | Some users will not want agent state in code history. | Store in `.anchor/` ignored by project Git; dedicated sync repo. |
| O-005 | What is the causal-memory MCP endpoint, tool contract version and test account? | Bridge integration and live e2e. | Mock in CI; live test opt-in with a dedicated test token. |
| O-006 | Vault fallback on Linux without Secret Service? | Headless/CI environments. | Explicit encrypted-file fallback with warning; not silent. |
| O-007 | Should uncommitted code handoff use Git bundles, patches, or both? | Binary files, submodules, large trees. | Bundle + manifest; patches for review. |
| O-008 | How much of the full transcript should restore inject? | Token limits vs recovery completeness. | Pointers + structured summaries by default; full excerpts on explicit `--full`. |
| O-009 | Is local web UI single-user only, or does it need LAN access? | Security model. | Loopback-only default; LAN requires explicit opt-in and auth. |
| O-010 | What is the Roost integration contract in concrete terms? | Avoid speculative implementation. | Interface stub + docs only; no live calls. |

## 8. Traceability to PRD pain points

| PRD pain point | Requirements |
|---|---|
| Unified workspace / fragmented config | REQ-WS-001/002, REQ-CFG-001..007 |
| Cross-device context loss | REQ-RST-001..010, REQ-SY-001..011 |
| Goal drift | REQ-ST-001, REQ-ST-004, REQ-ST-010 |
| No tree visualization | REQ-ST-001..012, REQ-UI-004 |
| Secret leakage | REQ-VT-001..010, REQ-SEC controls |
| Subscription/limit reuse | **Out of scope** (model routing; NG-001) |
| Multi-agent config fragmentation | REQ-CFG-001..007, REQ-AD-002..004 |
| Protocol incompatibility | MCP is the protocol boundary; Anchor does not rebuild it (D-006) |
| Skills/rules duplication | REQ-CFG-001, REQ-CFG-002, REQ-CFG-007 |
| Missing long-term causal memory | REQ-CM-001..012 |
| Context visualization blank | REQ-UI-004, REQ-ST-007, REQ-EVID-001 |

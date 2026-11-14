# anchor
Personal agent workspace for session continuity and causal memory

---

# Anchor

Anchor is the workspace layer for coding agents: it carries the *current task
structure* (Session Tree), the *constraints that keep the task from drifting*,
and the *evidence needed to resume safely* across Claude Code, Codex, and Gemini
CLI, and across devices.

Anchor deliberately does **not** implement a memory engine. Long-term causal
experience lives in the user's independent **causal-memory** project and is used
through its remote MCP endpoint. Anchor stores tree state, decisions, evidence,
configuration and secret *references* only.

> **Status: design baseline (pre-implementation).**
> The design documents in `docs/` are the review artifact for the architect and
> the user. No application code has been written yet. The private repository
> `JingxuanC/anchor` exists and was verified at initialization commit
> `39b6fffdb35511859a82632d174b97ca304e26f5`; no push, PR, or further
> default-branch write is authorized in this chat (see
> `docs/07-repo-bootstrap.md`).

## What Anchor will deliver

- **Session Tree** — goals, decisions, tasks, branches, status, provenance,
  evidence, ownership and tombstones, stored as an append-only, Git-mergeable
  event log with human-readable projections.
- **Cross-agent handoff** — capture and restore through native hooks/MCP for
  Claude Code, Codex, and Gemini CLI, with explicit graceful degradation.
- **Safe resume** — restore packets carry code state (including uncommitted
  changes), open questions, evidence, and warnings. A chat summary alone is
  never treated as a full recovery; the successor must verify code and evidence.
- **Goal constraints and branch management** — a north star plus must/must-not
  constraints, drift warnings, branch proposals and merge/abandon flows.
- **Causal experience bridge** — remote MCP only: search lessons at task start,
  propose decision→outcome links at completion, require evidence or user
  confirmation before a link becomes a causal conclusion.
- **Cross-device sync** — Git-based sync for workspace metadata and artifacts;
  never syncs `causal.db`, never force-pushes, and treats deletes as tombstones.
- **Unified config/rules/secrets** — one source of truth rendered per agent;
  secrets live in the OS keychain and are injected at runtime, never into agent
  context or logs.
- **CLI and local web UI** — machine-readable CLI plus an embedded TypeScript
  visualization for the tree, causal references, evidence, sync and ownership.

## Documents

| File | Purpose |
|---|---|
| `docs/00-requirements-baseline.md` | Full requirement baseline, provenance, scope, open questions |
| `docs/01-architecture.md` | Proposed architecture, stack decision, process/data flows |
| `docs/02-data-model.md` | Event-sourced Session Tree, evidence, ownership, handoff schema |
| `docs/03-adapter-matrix.md` | Claude Code / Codex / Gemini CLI capability matrix and fallbacks |
| `docs/04-security-model.md` | Threat model, secret handling, path/command/MCP/Git/web controls |
| `docs/05-acceptance-mapping.md` | Requirement → unit/integration/e2e/security test mapping |
| `docs/06-decisions-and-open-questions.md` | Confirmed decisions vs proposals vs blockers |
| `docs/07-repo-bootstrap.md` | GitHub verification result and required approvals |
| `docs/08-design-review-response.md` | Architect P0/P1 review findings and point-by-point disposition |
| `acceptance/matrix.yaml` | Machine-readable requirement/test coverage plan |

## Provenance

Requirements and decisions are tagged so user-confirmed intent is not mixed with
model inference:

- `[U]` user-confirmed (PRD or the latest decisions relayed by the architect)
- `[A]` architect instruction
- `[I]` engineering inference / proposal
- `[?]` open question requiring user or architect decision

The latest user decisions override v0.1–v0.3 where they conflict; the conflict
resolution is recorded in `docs/00-requirements-baseline.md` and
`docs/06-decisions-and-open-questions.md`.

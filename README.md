# Anchor

[![ci](https://github.com/JingxuanC/anchor/actions/workflows/ci.yml/badge.svg)](https://github.com/JingxuanC/anchor/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/JingxuanC/anchor)](https://github.com/JingxuanC/anchor/releases/latest)
[![Go 1.23+](https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

**Anchor is the workspace layer for coding agents.** It keeps the *current task
structure* (goals, constraints, decisions, tasks, evidence), the *code state*,
and the *handoff packet* needed to resume work across sessions, agents and
machines — so a fresh agent can continue safely instead of guessing from a chat
summary.

It is a local, offline Go binary with **no third-party dependencies**, no
network calls and no telemetry.

---

## The problem

Coding agents are good at a single session and bad at continuity:

- a new session starts with no durable memory of *why* the code looks the way it
  does;
- a different agent (Claude Code → Codex → another tool) has even less context;
- chat summaries are lossy and unverified, and an agent can silently resume from
  a stale worktree or a deleted file;
- there is no cheap way to prove "the successor actually inspected the code and
  evidence before editing".

Anchor fixes this by recording decisions/evidence as an append-only event log,
freezing a content-hashed handoff packet, and gating edits on the successor
verifying that packet.

## What you get

| Capability | Command / artifact |
|---|---|
| Project-local state that never touches your files | `anchor init --attach` → `<project>/.anchor/` |
| Session Tree: goals with constraints, decisions with trade-offs, tasks, questions, evidence | `anchor node ...`, `anchor evidence add ...` |
| Successor handoff packet with the task structure + code state hashes | `anchor handoff prepare` → `handoff/ho_*.json` |
| Gate: node edits refused until the successor acked **and** verified the packet | `anchor handoff restore --ack/--verify` |
| Code transport (commit + staged/unstaged diffs + untracked bytes) with backup ref, stash and rollback | `anchor handoff export --include-code --include-bytes`, `anchor handoff apply-code --apply` |
| Agent adapters: real CLI detection, Claude Code hook install, hookless `wrap` fallback | `anchor adapter detect`, `anchor adapter install`, `anchor wrap` |
| Read-only MCP server for agents that can call tools | `anchor mcp serve` (four tools, stdio JSON-RPC 2.0) |
| Human tree view: terminal, JSON, Markdown, self-contained HTML | `anchor tree`, `anchor tree --json/--md/--html` |
| Local read-only web UI (live tree, loopback only) | `anchor ui` |
| MCP registration snippets per agent | `anchor mcp config --adapter claude-code|codex|gemini-cli` |
| Agent skill teaching the resume/record workflow | `anchor skill print`, `anchor skill install` |
| Unified config: managed rules blocks, MCP registration, skills, hooks, drift/rollback | `anchor config init/plan/apply/status/rollback` |
| Automatic SessionStart context injection (Claude Code hook) | installed by `adapter install` / `config apply` |
| Verifiable evidence blobs, secret/path guards, quarantine of corrupt event files | `internal/core`, `internal/pathguard` |

## Install

macOS and Linux, checksum-verified release binary:

```sh
curl -fsSL https://raw.githubusercontent.com/JingxuanC/anchor/main/install.sh | sh
# installs to ~/.local/bin (override with ANCHOR_INSTALL_DIR)
```

Or with Go 1.23+:

```sh
go install github.com/JingxuanC/anchor/cmd/anchor@latest
```

Or from a checkout: `go build -o anchor ./cmd/anchor`.

## Quick start

One command in your project does the setup: it attaches the workspace
(`<project>/.anchor`), generates a workspace id and a machine-stable device id,
keeps `.anchor/` out of Git, detects installed agent CLIs and writes their
managed config (rules file, MCP registration, hooks incl. SessionStart context
injection, skill).

```sh
cd ~/code/my-project
anchor init
# → .anchor/ state, .gitignore entry, CLAUDE.md/.mcp.json/.claude/… for Claude Code
#   (and AGENTS.md/GEMINI.md for detected Codex/Gemini CLIs)
# Use `anchor init --agents none` or --no-agent-setup to keep it state-only,
# `anchor init --agents claude-code,codex` to choose, and `--json` for scripts.
```

Commands below assume `anchor` is on `PATH` (prefix with `./` if you built a
local binary). Record the task structure — goals carry constraints, decisions
carry rationale:

```sh
anchor node create --dir ~/code/my-project --id goal --type goal \
  --title "Ship the rate limiter" --north-star "no dropped requests"
anchor node create --dir ~/code/my-project --id task --type task --parent goal \
  --title "Implement WaitTime"
anchor node create --dir ~/code/my-project --id dec --type decision --parent goal \
  --title "Whole-interval rounding" --decision "round to whole refill intervals" \
  --rationale "successor must reproduce the boundary behavior"

# re-render the agent integration written by init
anchor config apply --dir ~/code/my-project/.anchor --repo ~/code/my-project --apply

# hand off when the session ends
anchor handoff prepare --dir ~/code/my-project/.anchor --repo ~/code/my-project

# successor: inspect the plan, acknowledge, verify (this unlocks edits)
anchor handoff restore --dir ~/code/my-project/.anchor --repo ~/code/my-project \
  --packet ~/code/my-project/.anchor/handoff/ho_*.json
anchor handoff restore ... --ack
anchor handoff restore ... --verify

# optional: tree view, local web UI and the agent skill
anchor tree --dir ~/code/my-project/.anchor
anchor ui --dir ~/code/my-project/.anchor --addr 127.0.0.1:0
anchor skill install --dir ~/code/my-project/.anchor --repo ~/code/my-project --apply
```

No native hooks? Wrap any agent CLI:

```sh
anchor wrap --dir ~/code/my-project/.anchor --node task \
  -- claude -p "continue the task"
```

`wrap` records session start/end events, exports `ANCHOR_WORKSPACE`,
`ANCHOR_NODE`, `ANCHOR_SESSION`, `ANCHOR_CONTEXT_FILE` (a generated context file
with goal constraints, decisions, tasks and open questions), forwards signals and
propagates the child's exit code. It never runs the child through a shell, and
`--prepare-handoff` can prepare the successor packet when the session exits.

### Cross-machine handoff

```sh
# sender: package workspace + event log + evidence + handoff packet + code bytes
anchor handoff export --dir proj/.anchor --repo proj \
  --out handoff.anchorpkg --include-bytes --include-code

# receiver: rebuild in a fresh directory, then verify and apply the code
anchor handoff import --package handoff.anchorpkg --target newproj --apply
anchor handoff restore --dir newproj/.anchor --repo newproj --packet newproj/.anchor/handoff/ho_*.json
anchor handoff restore ... --ack
anchor handoff restore ... --verify
anchor handoff apply-code --package handoff.anchorpkg --repo newproj --apply
```

Packages are hash-verified, text-scanned for secrets, and never executed.
Imported workspaces are rebuilt by replaying the event log, not by copying a
working tree.

## Tree view and agent skill

See the goal tree without reading raw events:

```sh
anchor tree --dir proj/.anchor                    # terminal tree
anchor tree --dir proj/.anchor --json             # machine-readable tree
anchor tree --dir proj/.anchor --md TREE.md       # human-readable projection
anchor tree --dir proj/.anchor --html TREE.html   # shareable, self-contained
anchor state --dir proj/.anchor --json            # alias of `status`
```

The terminal view shows goals with their north star/must/must-not, decisions with
rationale and consequences, tasks, questions and evidence counts; the HTML view
is a single static file with no external resources (safe to attach to a PR or
open from disk). Tombstoned nodes are hidden unless `--include-deleted` is given.

The agent skill is a short, opinionated playbook for agents: read the context,
check the tree, record decisions/evidence, prepare the handoff, and verify it on
resume. `anchor skill install` writes it to
`<project>/.claude/skills/anchor/SKILL.md` (plan by default, backup, idempotent,
uninstallable); `anchor skill print` prints the embedded copy:

```sh
anchor skill print
anchor skill install --dir proj/.anchor --repo proj --apply
```

## Configuration management

One source of truth for agent rules, MCP registration and skills, rendered into
per-agent files with **managed blocks**:

```sh
# project scope config: .anchor/config/config.json
anchor config init --dir proj/.anchor --repo proj --agents claude-code,codex --apply
anchor config show --json

anchor config plan  --dir proj/.anchor --repo proj [--diff]   # plan only (default)
anchor config apply --dir proj/.anchor --repo proj --apply    # write + snapshot
anchor config status --dir proj/.anchor --repo proj           # drift/missing/ok
anchor config rollback --dir proj/.anchor --repo proj [--to TS] --apply
```

What `apply` renders:

| Output | Details |
|---|---|
| `CLAUDE.md` / `AGENTS.md` / `GEMINI.md` | managed block between `<!-- anchor:begin … -->` / `<!-- anchor:end -->`; user content outside the block is preserved and never treated as drift |
| `.mcp.json` | managed `mcpServers.anchor` entry, other servers preserved |
| `.claude/skills/anchor/SKILL.md` | embedded agent playbook |
| `.anchor/config/rendered/` | Codex `config.toml` / Gemini `settings.json` MCP snippets to merge (shared agent config is never written automatically) |

`anchor config` also owns native **Claude Code hooks** (`hooks.claude_code` and
`hooks.session_start` in the config file): apply installs/removes the managed
`SessionStart` / `PostToolUse` / `SessionEnd` entries in
`.claude/settings.json`, and `adapter install` remains a shortcut for hook-only
setups. The `SessionStart` hook calls `anchor adapter context`, which returns
`additionalContext` built from the Session Tree and the latest handoff packet —
so a fresh session starts with the goal constraints, decisions and open questions
already in context. Injection is size-capped (`--max-bytes`, default 8000) and
secret-scanned; it never records prompts or transcripts.

The rendered rules instruct the agent to check Anchor at session start, record
decisions when direction changes, and prepare a handoff at completion. Codex and
Gemini receive the same playbook through their rules file (their activation
mechanism) instead of an assumed skill directory. Every apply snapshots the files
it touches (rollback-safe) and backs up existing files.

**Secrets are references only.** `secret_refs` accepts `env:NAME`,
`keychain:NAME`, `op://…` or `file:PATH`; literal values are rejected and
references are never rendered into agent files.

**Scopes.** User-scope config (`~/.config/anchor/config.json`) is merged with
project-scope config (`.anchor/config/config.json`); project values override
user values.

## Local web UI

```sh
anchor ui --dir proj/.anchor
# anchor ui listening on http://127.0.0.1:54321/
```

A read-only local view of the Session Tree: live-refreshing (2 s), goals with
constraints, decisions with rationale/consequences, tasks, ownership badges and
the handoff gate. The server binds **loopback only** (non-loopback `--addr` is
rejected), serves `GET`/`HEAD` only (mutations are not exposed), has no external
resources, renders data with `textContent` (no HTML injection), and re-reads the
workspace on every request. `--addr 127.0.0.1:PORT` picks a fixed port; the
default `127.0.0.1:0` picks a free one.

## MCP tools

`anchor mcp serve --dir <state>` speaks newline-delimited JSON-RPC 2.0 over stdio
and exposes read-only tools:

| Tool | Returns |
|---|---|
| `anchor_status` | workspace id/device, projection/recovery status, handoff gate |
| `anchor_tree` | nodes (goal/decision/task/…), evidence, sessions |
| `anchor_evidence_verify` | verification of one evidence blob |
| `anchor_restore_plan` | the read-only successor plan (blockers, code comparison, actions) |

Mutations stay in the CLI, where the gate, lease/fencing checks and event
validation live.

Register the server per agent:

```sh
# Claude Code (project scope): writes/merges .mcp.json automatically
anchor adapter install --adapter claude-code --dir proj/.anchor --repo proj --with-mcp --apply

# any agent: print the snippet for its config format
anchor mcp config --adapter claude-code --dir proj/.anchor   # .mcp.json JSON
anchor mcp config --adapter codex       --dir proj/.anchor   # ~/.codex/config.toml
anchor mcp config --adapter gemini-cli  --dir proj/.anchor   # settings.json JSON
```

The full per-agent walkthrough (hooks, MCP, wrap, skill, troubleshooting) is in
[`docs/10-agent-integration.md`](docs/10-agent-integration.md).

## A/B benchmark

`bench/` contains a reproducible two-phase benchmark (implement, then resume
from a fresh process) with hidden acceptance tests, baseline vs Anchor arms, a
deterministic mock agent for CI, and token/cost accounting parsed from the
agent's result JSON. It records the task hash lock, harness metadata, pass^k,
Wilson 95% intervals, paired bootstrap/McNemar deltas, budget caps, negative
controls (sabotage/cheater agents), test-edit detection with canonical-test
restoration, and per-trial prompt/patch/log artifacts — see
[`bench/README.md`](bench/README.md) §3b for the mapping to industry practice.
`bench/polyglot` adds multi-task Aider-polyglot-style coding tasks (visible
tests, same anti-cheat/metrics) and can load a real `polyglot-benchmark`
checkout.

Real run on 2026-11-16 (Claude Code 2.1.252 + DeepSeek `deepseek-v4-flash`,
3 trials per arm, 600 s per phase, one custom task):

| arm | phase-1 pass | phase-2 (resume) pass | mean tokens | mean cost | mean resume score |
|---|---|---|---|---|---|
| baseline (chat summary) | 1.00 | **0.00** | 1,010,812 | $1.644 | 1.00 |
| Anchor | 1.00 | **1.00** | 1,347,739 | $1.731 | 2.00 |

Anchor used ~33% more tokens (+$0.087/trial) because it actually completes the
resume: the baseline's failed phase-2 attempts still cost ~184k tokens each, so
it has no finite cost per successful resume.

### What these numbers do and do not show

- **Do:** the continuity mechanism works when the information needed later is
  not recoverable from the repo or tests. In the resume run the baseline (lossy
  chat summary) failed phase 2 in 3/3 trials while the Anchor packet passed 3/3.
- **Do not:** Anchor does not make an agent better at ordinary single-session
  coding. On the Aider-polyglot exercises both arms passed 100% (a ceiling
  effect), with Anchor slightly more expensive. A 33% number from a small
  single-task run is not "33% better code".
- **Caveat:** in the resume benchmark the hidden conventions are seeded by the
  harness into the Anchor tree (phase 1 is scripted), and the baseline receives
  a deliberately lossy summary. That makes it a *context-loss stress test*, not a
  measure of general agent capability; the newer `bench/stress` suite fixes this
  by having the phase-1 agent record the conventions itself with randomized
  variants.
- **Stress suite:** [`bench/stress`](bench/stress/README.md) implements the
  discriminating design (3 authored multi-session tasks, randomized hidden
  conventions, forced process restart, arms baseline/rules/packet/full). Its
  first real run was **invalidated as a continuity test**: the baseline wrote the
  convention into the repo (`CONVENTIONS.md`), so it recovered it without session
  state; full still recorded/conveyed 6/6 but cost ≈+62% per trial. The harness
  now flags `variant_leaked_in_repo`; a leak-proof protocol or invalidating leaked
  trials is required before drawing conclusions.
- **Practical guidance:** use Anchor when work spans sessions/agents/machines,
  when constraints are not encoded in the repo, or when a successor must prove
  what it verified. For one-shot tasks, `anchor init`'s lightweight state is
  enough; hooks/MCP/handoff add small overhead without a capability gain.

ReadThese are small-N, single-task results, not a leaderboard — read
[`bench/README.md`](bench/README.md) for the design, the honesty caveats and the
mapping to public benchmarks (SWE-bench Verified, Terminal-Bench, Aider polyglot,
τ-bench `pass^k`, HAL, Inspect Evals).

Reproduce the harness mechanics offline:

```sh
go test ./bench/...                       # mock-mode contract test
go run ./bench/ab --mock-agent --trials 1 # deterministic self-test
```

## How it works

- **Append-only event log** (`events/`, one JSON batch per transaction,
  temp+fsync+rename, flock, SHA-256 event identity) replayed into a causal DAG.
  Concurrent writes on the same base revision are isolated as conflicts, not
  silently merged.
- **Session Tree**: goals (north star, must/must-not, success criteria),
  decisions (decision, rationale, alternatives, consequences), tasks, questions,
  notes, evidence, ownership leases with fencing tokens, tombstones, provenance
  and confidence.
- **Evidence chain**: content-addressed blobs re-hashed on replay; `done`
  requires node-bound verified evidence; audits are read-only.
- **Handoff packet**: content-hashed JSON with goal constraints, full decisions,
  next steps, open questions, ownership, provenance summary and hashed code state
  (HEAD, staged/unstaged diffs, untracked files). `handoff_prepared` events arm
  the successor gate, so deleting the packet file cannot bypass it.
- **Gate**: node mutations are refused until the successor records
  `handoff_acked` + `handoff_verified`; verification requires HEAD and the
  working tree to match the packet (an exact-state successor can ack without
  `--force`).
- **Code transport**: `git bundle` of the prepared commit plus hashed diffs and
  untracked bytes, applied with a backup ref, stash, symlink-safe writes, ignored
  file detection and rollback. A package never executes code.

## Agent support

| Capability | Claude Code | Codex CLI | Gemini CLI |
|---|---|---|---|
| Version detection (`adapter detect`) | ✅ real probe | ✅ real probe | ✅ real probe |
| Native hook installation | ✅ incl. SessionStart context injection (contract-tested) | fragment only | fragment only |
| Hookless `wrap` fallback | ✅ | ✅ | ✅ |
| Metadata capture/ingest | ✅ | ✅ (contract) | ✅ (contract) |
| MCP server client | ✅ auto `.mcp.json` | manual config | manual config |
| Handoff/restore/apply-code | ✅ | ✅ | ✅ |

Hook capture is metadata-only (session id/cwd/tool/timestamp references), never
prompts or transcripts. The Claude hook path is contract-tested; running it
inside a live logged-in session is on the roadmap.

## Status and roadmap

This is an early, honest implementation — not a finished product. The
authoritative requirement-by-requirement audit is
[`docs/09-implementation-status.md`](docs/09-implementation-status.md)
(140 requirements: 45 implemented, 53 partial, 41 missing, 1 deferred).

Known gaps, roughly by priority:

1. **Ownership lifecycle**: leases/fencing already reject stale writes, but
   expiry is not enforced and there are no `claim`/`release`/`heartbeat`
   commands yet — serial agent handoff is safe, concurrent editing is not.
2. **Rules-file injection**: no managed `CLAUDE.md`/`AGENTS.md`/`GEMINI.md`
   blocks or SessionStart context injection.
3. **Real-agent validation of hooks/MCP** (the benchmark focuses on `wrap`).
4. **Broader benchmark evidence**: more trials, multiple tasks, public benchmark
   adapters (SWE-bench Verified / Aider polyglot / Terminal-Bench).
5. **Shippability**: `migrate`, `uninstall --purge`, signed releases.

## Documentation

| Document | Contents |
|---|---|
| [`docs/00-requirements-baseline.md`](docs/00-requirements-baseline.md) | requirement baseline (140 items) |
| [`docs/01-architecture.md`](docs/01-architecture.md) | architecture and invariants |
| [`docs/02-data-model.md`](docs/02-data-model.md) | events, Session Tree, handoff, ownership |
| [`docs/03-adapter-matrix.md`](docs/03-adapter-matrix.md) | per-agent capture/install matrix |
| [`docs/04-security-model.md`](docs/04-security-model.md) | threat model and controls |
| [`docs/09-implementation-status.md`](docs/09-implementation-status.md) | audited implementation status |
| [`docs/10-agent-integration.md`](docs/10-agent-integration.md) | per-agent setup: hooks, MCP, wrap, skill, troubleshooting |
| [`bench/README.md`](bench/README.md) | A/B benchmark protocol, results, caveats |

## Development

```sh
go build ./...
go test ./... -count=1
go test -race ./...          # takes a few minutes
go vet ./... && gofmt -l cmd internal bench
```

CI runs gofmt/vet/tests on Linux and macOS, `-race` on Linux, cross-builds
Windows/Linux/macOS, and a mock-mode benchmark self-test. See
[`CONTRIBUTING.md`](CONTRIBUTING.md) for ground rules (no new dependencies, no
secret capture, fail-closed security paths).

## License

[Apache License 2.0](LICENSE). Security reports: see [`SECURITY.md`](SECURITY.md).

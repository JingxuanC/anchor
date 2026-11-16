# Agent integration guide

How to connect coding agents to Anchor so they can (a) capture durable session
metadata, (b) read/write the Session Tree, and (c) call Anchor's read-only MCP
tools.

There are three integration layers; pick the ones your agent supports:

| Layer | What it does | Claude Code | Codex CLI | Gemini CLI |
|---|---|---|---|---|
| Native hooks | auto-record session/tool **metadata** (no prompts/transcripts) | `adapter install` | fragment only | fragment only |
| MCP server (read-only) | agent calls `anchor_status` / `anchor_tree` / `anchor_evidence_verify` / `anchor_restore_plan` | auto `.mcp.json` | manual `config.toml` | manual `settings.json` |
| `anchor wrap` | fallback for any CLI: session events, env + context file, exit/signal propagation | ✅ | ✅ | ✅ |

The agent skill (`anchor skill install`) teaches the record/resume workflow; it
is optional but recommended.

---

## 1. Workspace setup (all agents)

```sh
anchor init --attach --dir ~/code/project        # state in ~/code/project/.anchor
anchor node create --dir ~/code/project --id goal --type goal --title "…"
```

If the project is already initialised, `anchor status --dir <project>` resolves
the attached state root.

### One-shot managed setup (`anchor config`)

Instead of installing hooks/MCP/skill piecemeal, one config file can render
everything with managed blocks, drift detection and rollback:

```sh
anchor config init --dir project/.anchor --repo project --agents claude-code,codex --apply
anchor config plan --dir project/.anchor --repo project --diff
anchor config apply --dir project/.anchor --repo project --apply
anchor config status --dir project/.anchor --repo project
```

It writes managed blocks into `CLAUDE.md`/`AGENTS.md`/`GEMINI.md`, merges
`.mcp.json` for Claude Code, installs the Claude skill and renders Codex/Gemini
MCP snippets to `.anchor/config/rendered/`; user content, other MCP servers and
secret *references* are preserved. Hook installation for Claude Code is still
`anchor adapter install` (native hook entries are not part of the config file).

---

## 2. Claude Code

### Install hooks, MCP and skill

Two equivalent paths:

```sh
# one-shot: config owns rules + MCP + skill + hooks
anchor config init --dir ~/code/project/.anchor --repo ~/code/project \
  --agents claude-code,codex --apply
anchor config apply --dir ~/code/project/.anchor --repo ~/code/project --apply

# or adapter-only (hooks + MCP + skill helpers)
anchor adapter install --adapter claude-code \
  --dir ~/code/project/.anchor --repo ~/code/project \
  --with-mcp --apply
anchor skill install --dir ~/code/project/.anchor --repo ~/code/project --apply
anchor doctor --dir ~/code/project --json     # includes an `adapters` check
```

This writes/merges:

| File | Content |
|---|---|
| `~/code/project/.claude/settings.json` | managed `SessionStart` + `PostToolUse` + `SessionEnd` hook entries (backed up first) |
| `~/code/project/.mcp.json` | `mcpServers.anchor` = absolute `anchor mcp serve --dir <state>` |
| `~/code/project/.claude/skills/anchor/SKILL.md` | the agent playbook |

Restart Claude Code and check it sees the server (`/mcp` in the UI, or ask the
agent to call `anchor_status`). `SessionStart` injects a size-capped,
secret-scanned context block (goal constraints, decisions, tasks, open
questions, handoff pointer) through the hook's `additionalContext`; the other
hooks write one metadata-only capture file per event under
`<state>/local/captures/` and print nothing. Disable/tune hooks in the config
file (`hooks.claude_code`, `hooks.session_start`); `anchor config status` reports
hook drift.

To plan before writing, omit `--apply` and inspect the printed operations/backup
paths; `--uninstall` removes only the recorded entries.

### Manual configuration (if you prefer not to install)

```sh
anchor mcp config --adapter claude-code --dir ~/code/project/.anchor --json
# merge the printed {"mcpServers":{"anchor":…}} into .mcp.json
```

---

## 3. Codex CLI

Hooks are not auto-installed (only a `config.toml.fragment` is rendered); use
`anchor wrap` for session recording. MCP registration:

```sh
anchor mcp config --adapter codex --dir ~/code/project/.anchor
# prints e.g.
# [mcp_servers.anchor]
# command = "/abs/path/anchor"
# args = ["mcp", "serve", "--dir", "/abs/code/project/.anchor"]
```

Paste the block into `~/.codex/config.toml` (or a project-scoped config),
restart Codex, and verify the `anchor_*` tools are listed. For session capture:

```sh
anchor wrap --dir ~/code/project/.anchor --node <task> -- codex exec "continue the task"
```

`wrap` exports `ANCHOR_WORKSPACE`, `ANCHOR_NODE`, `ANCHOR_SESSION`,
`ANCHOR_CONTEXT_FILE` and records `adapter_session_seen/ended` events.

---

## 4. Gemini CLI

```sh
anchor mcp config --adapter gemini-cli --dir ~/code/project/.anchor
# merge the printed mcpServers block into ~/.gemini/settings.json
```

Hooks/MCP are documentation-level for this adapter (the CLI was not available in
the development environment); `anchor wrap --dir … --node … -- gemini …` is the
supported capture path. The Anchor skill playbook is rendered into `GEMINI.md`
(and `AGENTS.md` for Codex) rather than assuming an adapter-specific skill
directory.

---

## 5. Any MCP client

The server is a standard stdio MCP server: newline-delimited JSON-RPC 2.0,
`initialize` / `tools/list` / `tools/call` / `ping`. Run it as a subprocess:

```sh
anchor mcp serve --dir ~/code/project/.anchor
```

Tools (read-only):

| Tool | Use it when |
|---|---|
| `anchor_status` | start of a session: workspace/projection/recovery/handoff gate |
| `anchor_tree` | you need goals, constraints, decisions, tasks, evidence |
| `anchor_evidence_verify` | you are about to rely on an evidence blob |
| `anchor_restore_plan` | a handoff is pending; read the plan before editing |

The server never mutates the workspace and never records ack/verify; mutations go
through the CLI so gate, ownership fencing and event validation apply.

---

## 6. Recommended agent workflow

1. **Start**: read `ANCHOR_CONTEXT_FILE` (under `wrap`) and/or call
   `anchor_status` + `anchor_tree`; if a handoff is pending, read
   `anchor_restore_plan`, then `anchor handoff restore --ack` / `--verify`.
2. **Work**: keep the tree current with `anchor node transition/edit`,
   `anchor node create` for decisions (with rationale/consequences) and
   `anchor evidence add` for verifiable artifacts. Never paste secrets or
   transcripts.
3. **Code restore**: if the handoff package carries code bytes,
   `anchor handoff apply-code --package … --repo … --apply` (backup ref + stash).
4. **End**: `anchor handoff prepare --dir <state> --repo <project>` and report the
   packet path; unresolved work becomes a task/question node, not a chat aside.

The embedded skill (`anchor skill print`) contains the same checklist in a form
agents can load as instructions.

---

## 7. Security notes

- MCP tools are **read-only**; the server has no mutation endpoints.
- Hook capture is metadata-only: prompts, tool inputs, transcripts and tokens are
  dropped by design.
- `anchor ui` binds loopback only, serves GET/HEAD only, and has no write surface.
- Exported packages are hash-verified and text-scanned for secrets; a git bundle
  can still contain secrets from the source history (committed history is not
  scanned), so verify provenance before sharing.
- Anchor is not a sandbox: an agent with filesystem permissions can still edit
  files; the gate protects against accidental stale resumes, not hostile
  processes.

## 8. Troubleshooting

| Symptom | Fix |
|---|---|
| `workspace is not initialized` | run `anchor init --attach --dir <project>` first; pass the project dir or the `.anchor` state dir |
| agent does not list `anchor_*` tools | check the config path/casing, restart the agent; verify the command runs: `anchor mcp serve --dir <state>` then send `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` on stdin |
| hooks do not fire | ensure the settings file is the one the agent reads, restart it; check `anchor doctor` adapters detail and `<state>/local/captures/` |
| node edits are refused | a handoff is pending: `anchor handoff restore … --ack`, `--verify` (or the operator must `--force`) |
| MCP tool returns "no prepared handoff" | run `anchor handoff prepare` first |

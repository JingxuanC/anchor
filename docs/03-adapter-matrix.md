# Adapter Capability Matrix

**Verified against official documentation on 2026-11-15.** Capabilities change
quickly; Anchor detects the installed CLI version at runtime and records the
detected capability descriptor. The matrix is a design input, not a promise that
every version supports every row.

Sources:

- Claude Code hooks: <https://code.claude.com/docs/en/hooks>
- Claude Code MCP: <https://code.claude.com/docs/en/mcp>
- Claude Code settings: <https://code.claude.com/docs/en/settings>
- Codex hooks: <https://learn.chatgpt.com/docs/hooks>
- Codex config: <https://learn.chatgpt.com/docs/config-file/config-sample>
- Codex customization/AGENTS.md: <https://learn.chatgpt.com/docs/customization/overview>
- Gemini CLI hooks: <https://geminicli.com/docs/hooks/>
- Gemini CLI MCP: <https://geminicli.com/docs/tools/mcp-server/>
- Gemini CLI extensions: <https://geminicli.com/docs/extensions/writing-extensions/>

## 1. Capability matrix

| Capability | Claude Code | Codex CLI | Gemini CLI | Generic fallback |
|---|---|---|---|---|
| Lifecycle hooks | Yes: `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, `PreCompact`, plus many subagent/config events | Yes: `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, `PreCompact`, `SubagentStart/Stop`, `Interrupt` | Yes: `SessionStart`, `SessionEnd`, `BeforeAgent`, `AfterAgent`, `BeforeModel`, `AfterModel`, `BeforeTool`, `AfterTool`, `PreCompress`, `Notification`, `BeforeToolSelection` | `anchor wrap` / manual commands; no automatic lifecycle events |
| Hook config location | `~/.claude/settings.json`, `.claude/settings.json`, `.claude/settings.local.json`, managed/plugin/skill scopes | `~/.codex/hooks.json`, `~/.codex/config.toml`, `<repo>/.codex/hooks.json`, `<repo>/.codex/config.toml`; project hooks require trust | `.gemini/settings.json` (project), `~/.gemini/settings.json`, `/etc/gemini-cli/settings.json`, extensions | Anchor config only; user runs commands |
| Hook schema | Event → matcher group → handler; handlers `command`, `http`, `mcp_tool`, `prompt`, `agent` | Event → matcher group → handler; command/mcp_tool handlers; JSON or TOML | Event → matcher group → handler; command type | N/A |
| SessionStart context injection | Yes: plain stdout and `additionalContext`; sources `startup/resume/clear/compact/fork` | Yes: plain stdout or `additionalContext`; `compact` variant supported | Yes: context-related effects listed for `SessionStart`, `BeforeAgent`, `AfterTool`; verify exact JSON field in adapter spike | `anchor restore` prints/writes a packet; user pastes or rules file points to it |
| Prompt capture / blocking | `UserPromptSubmit`: exit 2 or `decision: block` blocks the prompt; stdout/`additionalContext` injected | `UserPromptSubmit`: stdout/`additionalContext`; `decision: block` or exit 2 rejects | `BeforeAgent`: can block the turn or add context | Manual `anchor capture prompt` |
| Pre-tool policy | `PreToolUse`: deny/allow/ask, `updatedInput`, `additionalContext`; cannot escalate permissions | `PreToolUse`: deny/block/exit 2; `permissionDecision: allow` + `updatedInput`; `additionalContext` | `BeforeTool`: block or rewrite the call | `anchor wrap` can inspect argv before launch; no per-tool interception |
| Post-tool evidence | `PostToolUse`: `additionalContext`; exit 2 shows stderr to Claude; cannot undo | `PostToolUse`: `additionalContext`; block/exit 2 replaces result; cannot undo | `AfterTool`: block/hide result or add context | Manual evidence command |
| Stop / checkpoint | `Stop`: exit 2 or `decision: block` continues; `last_assistant_message` available | `Stop`: exit 0 requires JSON; block/exit 2 continues; `continue: false` stops | `AfterAgent`: can force retry or halt | `anchor checkpoint` manual |
| Compaction | `PreCompact`: exit 2 blocks; stdout becomes compaction instructions | `PreCompact`; `continue: false` prevents compaction | `PreCompress`: advisory | Persist before/after via `anchor` commands |
| Session end flush | Yes; all `SessionEnd` hooks share a 1.5 s budget (can extend up to 60 s) | Yes; `SessionEnd` default 1 s, cap 3 s, advisory, not for subagents | Yes: `SessionEnd` | `anchor wrap` flush on exit |
| Subagents | `SubagentStart/Stop`, task/teammate events; `additionalContext` supported | `SubagentStart/Stop`; context injection supported | Not listed as a dedicated subagent hook; `BeforeAgent`/`AfterAgent` | N/A |
| Session/transcript identity | Hook input includes `session_id`, `transcript_path`, `cwd`, agent metadata | Session id/transcript surfaces via hook/session config; verify exact fields in spike | Hook env: `GEMINI_SESSION_ID`, `GEMINI_PROJECT_DIR`, `GEMINI_CWD`, `GEMINI_EXTENSION_PATH` | Generate local session id |
| MCP client config | `~/.claude.json` (local/user scopes), project `.mcp.json`, managed/plugin servers; transports stdio/http/sse/ws | `[mcp_servers.<name>]` in `~/.codex/config.toml` / `.codex/config.toml`; stdio and HTTP; OAuth/keyring options | `mcpServers` in `.gemini/settings.json` / `~/.gemini/settings.json`; stdio (`command`), SSE (`url`), streamable HTTP (`httpUrl`) | `anchor mcp serve` can be launched manually or by a generic client |
| MCP transports | stdio, HTTP (streamable-http alias), SSE (fallback), WebSocket | stdio, HTTP | stdio, SSE, streamable HTTP | stdio/HTTP via SDK |
| Tool naming | `mcp__<server>__<tool>`; plugin names include plugin | `mcp__<server>__<tool>`-style matchers in hooks; verify at runtime | `mcp_{server}_{tool}`; avoid underscores in server names | N/A |
| Rules / instructions | `CLAUDE.md`, `.claude/rules/`, imports; managed blocks are Anchor's target | `AGENTS.md` (global/project/nested; no `@import`; nearest wins) | `GEMINI.md`, extension `contextFileName` | `.anchor/instructions.md` + user's own file |
| Config precedence | Managed > CLI > project local > shared project > user; list keys merge | Global `~/.codex/config.toml`, project `.codex/config.toml`, profiles, trust per project | Project > user > system > extensions | Anchor config only |
| Secrets in settings | Keychain/credential helpers used by Claude/MCP OAuth; env expansion can leak if misused | MCP OAuth credential store (`auto/file/keyring`), `env_vars` | Extension `settings` with `sensitive` stored in system keychain; host env filtered | Anchor vault; no secret in generated config |
| Session resume/fork | Native resume/fork; hooks expose `SessionStart` sources `resume`/`fork` | Resumed/forked sessions documented (`[tui] resume_cwd`, history) | Session lifecycle hooks; verify resume semantics in spike | `anchor restore` recreates context manually |
| Hook trust | Project settings/hooks load after workspace trust; `.mcp.json` approval prompts | Project `.codex/` hooks require project trust; `/hooks` review/trust | Project settings have their own trust model; MCP `trust` defaults false | Anchor installer must explain trust prompts |
| Context/output limits | `additionalContext`/stdout capped at 10,000 chars; oversized saved to file + preview | `additionalContext` ~2,500 tokens default; `additionalContextLimit` override | Hook timeout default 60,000 ms; context schema to verify | Anchor packet size budget |
| Hook timeout | 600 s default for command/http/mcp_tool; 30 s prompt; 60 s agent; SessionEnd 1.5 s shared | 600 s default; SessionEnd/Interrupt 1 s cap 3 s | 60,000 ms default, configurable | `anchor wrap` no fixed hook budget |
| Blocking semantics | exit 2 blocks where supported; exit 1 is not policy; malformed JSON is non-blocking | exit 2 blocks supported events; Stop/SubagentStop success requires JSON | exit 0 parses JSON; exit 2 blocks target; other codes warn | Anchor policy enforced by wrapper/CLI exit code |

## 2. Chosen integration strategy per adapter

### Claude Code

- **Install**: write a managed block to `CLAUDE.md` (and optionally
  `.claude/rules/`), merge hook entries into project `.claude/settings.json`,
  register `anchor` MCP server in `.mcp.json` or user scope. Preserve existing
  user content; back up before writes.
- **Capture**: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
  `Stop`, `PreCompact`, `SessionEnd`.
- **Restore**: `SessionStart` additionalContext (size-capped); `PreCompact`
  instructions to preserve the handoff packet; rules tell the agent to call
  `anchor_status` and `anchor_verify_evidence`.
- **Policy**: `PreToolUse` can warn/deny configured dangerous commands and
  enforce ownership; never escalate permissions.
- **MCP**: project `.mcp.json` (stdio `anchor mcp serve`) or user scope for
  cross-project use; the UI can use the local HTTP endpoint.
- **Known limits**: 10k-char context cap; SessionEnd 1.5 s budget; hooks cannot
  override managed denials.

### Codex CLI

- **Install**: merge `[mcp_servers.anchor]` into `.codex/config.toml` (or user
  config), hooks into `.codex/hooks.json` (or inline tables), managed block in
  `AGENTS.md`. Project hooks require trust; explain and never bypass.
- **Capture**: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
  `Stop`, `PreCompact`, `SessionEnd`, `SubagentStart/Stop` where available.
- **Restore**: `SessionStart` / `UserPromptSubmit` `additionalContext`;
  `AGENTS.md` instructions tell the agent to call Anchor MCP tools.
- **Policy**: `PreToolUse` deny/exit 2 for configured policy; `additionalContext`
  for drift warnings.
- **MCP**: `[mcp_servers.anchor]` stdio; HTTP for the UI.
- **Known limits**: Stop/SubagentStop require JSON on exit 0; SessionEnd 1 s cap
  3 s; project trust; context threshold ~2.5k tokens.

### Gemini CLI

- **Install**: merge `mcpServers.anchor` into `.gemini/settings.json` (or user
  settings), hooks into the same file, managed block in `GEMINI.md`; optionally
  package as an extension later. `trust` defaults false.
- **Capture**: `SessionStart`, `BeforeAgent`, `AfterAgent`, `BeforeTool`,
  `AfterTool`, `PreCompress`, `SessionEnd`.
- **Restore**: `SessionStart`/`BeforeAgent` context injection; `GEMINI.md`
  instructions.
- **Policy**: `BeforeTool` block/rewrite; `AfterTool` redact results.
- **MCP**: stdio `command` or streamable HTTP `httpUrl`; avoid underscores in
  the server name for policy parsing.
- **Known limits**: hook timeout default 60 s; stdout must be a single JSON
  object; context schema requires an adapter spike against the installed
  version.

## 3. Fallbacks when a capability is missing

| Missing capability | Fallback |
|---|---|
| Lifecycle hooks | `anchor wrap --agent <agent> -- <command>`; wrapper sets env, captures stdin/stdout where possible, imports session on exit; manual `anchor capture` commands. |
| Context injection | `anchor restore --write-instructions` updates the agent's rules file with a short pointer; prints a compact packet for the user/agent to read. |
| MCP client support | Rules instruct the agent to run `anchor` CLI commands; `anchor mcp serve` remains available for clients that support it. |
| Transcript access | Capture hook payloads and command output; mark `capture_state=partial`; never claim full recovery. |
| Session resume/fork | `anchor restore` reconstructs the packet; no reliance on native resume. |
| Project trust | Installer prints the exact trust step; no automatic trust modification. |

## 4. Real vs mock capability policy

| Adapter | Contract tests | Integration tests | E2E tests | Status |
|---|---|---|---|---|
| Claude Code | Hook JSON schema + config renderer | Temp workspace + local hook runner | Real `claude` installed in a temp project; SessionStart/hook/MCP round-trip | `claude` 2.1.325 is installed in the development environment; real e2e required for release. |
| Codex CLI | Recorded hook/config fixtures from official docs | Config renderer + fake hook runner | Real `codex` e2e only when installed/authenticated; otherwise `SKIP` with reason | CLI not installed here; not claimed as live. |
| Gemini CLI | Recorded hook/config fixtures from official docs | Config renderer + fake hook runner | Real `gemini` e2e only when installed/authenticated; otherwise `SKIP` with reason | CLI not installed here; not claimed as live. |
| causal-memory | MCP contract tests against a local mock server | Offline/retry/idempotency/circuit-breaker tests | Live remote MCP test is opt-in only, with a dedicated test token; never in default CI | Mock is clearly labeled; live test is not run by default. |
| Git sync | Unit merge tests | Real local bare repository | Two-worktree/two-device simulation; GitHub remote opt-in | Real Git used; no fake Git. |
| Vault | Fake backend unit tests labeled `fake` | OS keychain integration where available | macOS/Windows keychain e2e in matrix; Linux Secret Service e2e if available; encrypted-file fallback tested explicitly | Never use real user secrets. |

## 5. Adapter contract test fixtures

The repo will keep `acceptance/fixtures/adapters/<agent>/` with:

- official hook input/output examples (redacted, synthetic);
- config renderer golden files;
- capability descriptor snapshots with the agent version;
- a `source` field pointing to the official documentation URL and retrieval date.

A fixture is never presented as a live integration. `anchor doctor` reports
`adapter=<name> source=<real|recorded|fake> version=<detected>`.

## 6. Version support policy

- Target the latest stable agent CLIs at release time; record the exact versions
  used for e2e.
- Detect major/minor versions and disable unsupported features with actionable
  messages.
- Keep adapter logic isolated; a breaking hook schema change should require an
  adapter update, not a core change.
- Minimum supported versions are an open question (O-003) until the adapter
  spike records the actual behavior.

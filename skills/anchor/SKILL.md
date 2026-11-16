---
name: anchor
description: Persist task state, decisions, evidence and handoffs with Anchor so work can resume across sessions and agents. Use at task start (read the state/tree), while working (record decisions and evidence), and at session end (prepare a handoff).
---

# Using Anchor

Anchor is a local CLI + MCP server that keeps the task's durable state for you.
Treat the Session Tree as the project's memory: chat history is not.

The operator may have set these environment variables (e.g. under `anchor wrap`):

- `ANCHOR_WORKSPACE` — state directory (usually `<project>/.anchor`)
- `ANCHOR_NODE` — the node this session is attached to
- `ANCHOR_SESSION` — the session id
- `ANCHOR_CONTEXT_FILE` — a generated context file (goal constraints, decisions,
  tasks, open questions) — **read it first**
- `ANCHOR_BIN` — absolute path to the `anchor` binary

If the variables are missing, use the state directory the operator gives you
(often `<project>/.anchor`) and the `anchor` binary on `PATH`.

## At the start of a session

1. Read the context file (`cat "$ANCHOR_CONTEXT_FILE"`) if it exists.
2. Inspect the workspace state and tree before editing:

   ```sh
   "$ANCHOR_BIN" status --dir "$ANCHOR_WORKSPACE" --json
   "$ANCHOR_BIN" tree   --dir "$ANCHOR_WORKSPACE"
   # machine-readable tree, or a shareable visual:
   "$ANCHOR_BIN" tree --dir "$ANCHOR_WORKSPACE" --json
   "$ANCHOR_BIN" tree --dir "$ANCHOR_WORKSPACE" --html .anchor/TREE.html
   ```

3. If a handoff is pending, check the plan before touching files:

   ```sh
   "$ANCHOR_BIN" handoff restore --dir "$ANCHOR_WORKSPACE" --repo . \
     --packet "$ANCHOR_WORKSPACE"/handoff/ho_*.json
   ```

   Node edits are refused until a successor acknowledges **and** verifies the
   packet (`--ack`, then `--verify`). Do not use `--force` unless the operator
   explicitly tells you to.

## While working

Record what future sessions cannot re-derive from the code:

```sh
# a decision with its rationale (this is what successors rely on)
"$ANCHOR_BIN" node create --dir "$ANCHOR_WORKSPACE" --id dec-<slug> --type decision \
  --parent "$ANCHOR_NODE" --title "<short>" --decision "<what>" \
  --rationale "<why>" --consequence "<impact>" --provenance inferred

# task status
"$ANCHOR_BIN" node transition --dir "$ANCHOR_WORKSPACE" --id <task> --to in_progress

# verifiable evidence (logs, test output, diffs)
"$ANCHOR_BIN" evidence add --dir "$ANCHOR_WORKSPACE" --node <task> --file <path> --kind test_output
```

Rules:

- Never record secrets, tokens, prompts or transcripts. Evidence files are
  content-addressed and verified; keep them small and relevant.
- Decisions need a rationale and consequences; a bare title is not enough.
- Do not fabricate provenance or `user_confirmed` attestations.
- Prefer recording *why* over restating *what* the code already shows.

## At the end of a session

```sh
# persist the handoff packet for the successor (this arms the successor gate)
"$ANCHOR_BIN" handoff prepare --dir "$ANCHOR_WORKSPACE" --repo . --json

# optional: package the workspace + code bytes for another machine
"$ANCHOR_BIN" handoff export --dir "$ANCHOR_WORKSPACE" --repo . \
  --out /tmp/handoff.anchorpkg --include-bytes --include-code
```

Report the packet path to the operator. If you could not finish, record the
blocker as a task/question node first — the successor must see it.

## MCP tools (when registered)

If the Anchor MCP server is configured (`anchor mcp serve`), you can call:

- `anchor_status` — workspace id, projection/recovery status, handoff gate
- `anchor_tree` — nodes (goal/decision/task/…), evidence, sessions
- `anchor_evidence_verify` — verify one evidence blob
- `anchor_restore_plan` — read-only successor plan

Prefer these over guessing. They are read-only; use the CLI for mutations so the
gate, ownership fencing and event validation apply.

## Successor checklist

1. Read `ANCHOR_CONTEXT_FILE` / `anchor tree` / `anchor status --json`.
2. Inspect the pending `handoff restore` plan; acknowledge it, then verify it.
3. Compare the recorded code state (HEAD, diffs, untracked files) with the repo;
   if bytes were packaged, apply them with `anchor handoff apply-code --apply`.
4. Only then edit nodes/code; keep the tree updated as you work.

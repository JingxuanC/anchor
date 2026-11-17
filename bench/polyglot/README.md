# Aider-polyglot benchmark harness

`bench/polyglot` is a self-contained (Go standard library only) command that
runs Aider-polyglot-style coding exercises against an agent and writes a
machine-readable report. It is the polyglot-task counterpart of the A/B
continuity benchmark in [`../ab`](../ab/README.md).

Each observation (task × trial × arm) copies one exercise into a fresh
temporary git repository, runs the agent on it, records the patch and
token/cost usage, **restores the canonical test files** (anti-cheat), and
grades the result with the language's own acceptance command.

```bash
# offline harness self-test: deterministic mock agent, bundled fixtures
go run ./bench/polyglot --mock-agent --trials 1 --out /tmp/poly

# prove the anti-cheat path: the mock "cheater" overwrites the tests
go run ./bench/polyglot --mock-agent --mock-mode cheater --trials 1 --out /tmp/poly-cheat

# real run (the prompt is appended as the final argument of the command)
go run ./bench/polyglot --agent-cmd "claude -p --output-format json" \
    --agent-model claude-sonnet-4-5 --trials 3 --out bench/results/polyglot-claude
```

`--polyglot-dir` may point at a full Aider-polyglot checkout
(`<lang>/exercises/practice/<slug>/`) or any directory of exercises in the
simpler `<lang>/<slug>/` layout. Without it, the bundled fixture under
`bench/fixtures/polyglot` is used, so the command works out of the box.

## Tasks and arms

* **Task discovery** — a directory is an exercise when it contains
  `.docs/instructions.md` and either:
  * `python`: `<slug>.py` + `*_test.py` → `python3 -m pytest -q <tests>`;
  * `go`: `go.mod` + `*_test.go` → `go test -count=1 ./...`.

  Exercises of other languages are skipped. The default selection is capped at
  25 tasks; `--tasks` may name an explicit subset.
* **baseline arm** — copy the exercise, `git init`, one commit, run the agent
  directly in the project.
* **anchor arm** — additionally `anchor init --attach --dir <proj> --agents none`
  and an `anchor node create` goal/task pair, then
  `anchor wrap --dir <proj>/.anchor --node <task> -- <agent>`. If the binary or
  its `wrap` command is unavailable, the harness falls back to running the agent
  directly with `ANCHOR_*` environment variables and records that fact.

## Flags

```
--polyglot-dir DIR   task directory (default: the bundled --fixtures)
--fixtures DIR       bundled fixture root (default bench/fixtures/polyglot)
--lang LANG          only run tasks in this language (python, go, ...)
--tasks all|a,b      task selection by slug (default all, capped at 25)
--trials N           trials per (task, arm) (default 1)
--arm MODE           baseline | anchor | both (default both)
--agent-cmd CMD      real agent CLI; prompt appended as the final argv element
--agent-model MODEL  model name recorded in the report (optional)
--mock-agent         deterministic offline agent (cannot be combined with --agent-cmd)
--mock-mode MODE     normal | sabotage | cheater (default normal)
--timeout D          per agent/acceptance process timeout (default 10m)
--max-usd F          stop launching observations once total cost reaches F USD
--max-tokens N       stop launching observations once total tokens reach N
--out DIR            output dir (default bench/results/polyglot-<UTC timestamp>)
--json               print report.json on stdout instead of the summary table
--anchor-bin PATH    prebuilt anchor binary (default: build ./cmd/anchor into <out>/bin)
--keep-work          keep the per-trial project copies for debugging
```

Exit codes: `0` run completed, `1` harness error, `2` invalid flags,
`3` skipped (the real agent command was not found; a report is still written).

## Real run (2026-11-17)

First real-agent run on the two bundled exercises, Claude Code 2.1.252 backed by
DeepSeek `deepseek-v4-flash`, 1 trial per (task, arm), 300 s timeout:

| arm | tasks | pass@1 | pass^k | mean wall | mean tokens | mean cost |
|---|---|---|---|---|---|---|
| baseline | 2 | 1.00 | 1.00 | 28.6 s | 96.2k | $0.172 |
| anchor | 2 | 1.00 | 1.00 | 29.6 s | 97.1k | $0.176 |

Both arms solve both exercises with no test-file edits (`tests_modified = 0`).
Anchor costs ~+3.5% tokens / +$0.004 per task here — this is harness overhead on
two easy single-session exercises, not evidence that Anchor improves hard-task
performance; it does show the real-agent path (prompt, wrap, usage/cost parsing,
anti-cheat, artifacts) working end to end. Larger polyglot runs should use more
tasks/trials and report the same way.

## Anti-cheat

The prompt tells the agent not to modify the tests, but the harness does not
rely on that:

1. hashes the canonical test files before and after the agent;
2. records `tests_modified` and `cheat_flags`
   (`test_file_modified:<name>`, `test_file_deleted:<name>`,
   `unexpected_test_file:<name>`);
3. removes test infrastructure the agent added (`conftest.py`, `pytest.ini`, …)
   and restores every canonical test file from the pristine exercise;
4. grades only after restoration — a cheater therefore fails.

The unmodified agent output is kept in `logs/<task>-t<N>-<arm>/patch.diff`, and
the before/after hashes are in `test-integrity.txt`.

This is an integrity check for benchmark numbers, **not a security boundary**:
the agent runs with the invoking user's permissions and network access. There is
no container/Docker isolation or syscall sandboxing.

## Report

`report.json` (schema version 1) contains:

* `config` — polyglot dir, filters, trials, arms, agent command/model, mock
  mode, timeout, budget caps, output dir;
* `metadata` — Go version, repository commit/dirty flag, anchor version and
  wrap availability, python/pytest availability, task count and per-task
  metadata with a SHA-256 `hash` of each exercise;
* `trials[]` — one row per observation: `task`, `trial`, `arm`, `pass`,
  `tests_modified`, `cheat_flags`, `wall_ms`, `agent_exit`, `accept_exit`,
  `tokens`, `cost_usd`, usage details, patch/log paths, errors;
* `per_task[]` — per task and arm: trials, pass@1 + Wilson 95% CI, pass@k (all
  k trials pass), mean wall/tokens/cost, tests_modified;
* `aggregates` — per arm: tasks, trials, pass@1 + Wilson 95% CI, pass@k, mean
  wall/tokens/cost, tests_modified;
* `budget` — caps, totals, `budget_exceeded`, `truncated`,
  `truncation_reason`;
* `environment_notes` — honest caveats (mock mode is not evidence about real
  agents, no isolation, acceptance skipped if pytest is missing, …).

`report.md` is the human-readable equivalent with per-arm, per-task and
per-trial tables. Per-observation artifacts live under
`<out>/logs/<task>-t<N>-<arm>/`: `prompt.txt`, `patch.diff`, `agent.log`,
`acceptance.log`, `test-integrity.txt`, and (anchor arm) `anchor-setup.log` /
`anchor-context.md`.

Token and cost usage is parsed from a Claude-Code-style JSON result object in
the agent output (`usage.*` and `total_cost_usd`); agents that print no such
object simply report zero usage. A mock observation prints a synthetic usage
line so the budget logic is exercised offline.

## Fixtures

`bench/fixtures/polyglot` vendors two tiny, originally authored Python
exercises (`leap`, `two_fer`) plus `mock/correct` and `mock/cheat` content. They
are **not** copies of Exercism text. See
[`fixtures/polyglot/README.md`](../fixtures/polyglot/README.md).

## Tests

```bash
go test ./bench/polyglot -count=1
```

The tests are offline, use `t.TempDir()` only, and skip the grading assertions
when `python3`/`pytest` are unavailable. They cover loader discovery and
filters, mock normal passing, sabotage failing, the cheater being flagged and
restored (and failing), report JSON/aggregates/Wilson CI, budget truncation, and
the anchor direct-run fallback.

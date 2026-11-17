# Anchor multi-session continuity stress benchmark

Ordinary single-session coding tasks are **saturated**: on small tasks both
Anchor and a baseline pass, so they cannot tell us whether durable session state
helps. This suite is a stress benchmark instead: it makes the information a
later session needs **impossible to recover from the repository** and forces the
phase-1 process to lose its context, so continuity itself is the variable under
test.

```
go run ./bench/stress --mock-agent --trials 2 --out /tmp/stress
```

Mock mode is an **offline harness self-test**, not evidence about real agents. A
real run uses `--agent-cmd` (see [§6](#6-running-with-a-real-agent)).

---

## 1. Design

Each trial is two phases across a process boundary, on one of three small
authored Go tasks (`money`, `names`, `scores`):

| | phase 1 (fresh process) | phase 2 (fresh process, same repo) |
|---|---|---|
| what happens | implement the visible API; make `go test ./...` pass | add a new function that only the hidden test exercises |
| convention | the prompt **states** a randomly chosen hidden convention | the convention is **not** stated anywhere in the prompt |
| durability | the agent must record the convention itself | acceptance = visible tests + the hidden test for that variant |

The visible phase-1 tests deliberately do not pin the convention down: it only
affects the phase-2 function that does not exist yet. The three tasks use
different convention styles:

* **money** — how leftover cents are allocated: largest fractional remainder /
  earliest parts / drop the remainder.
* **names** — how a display name is normalized: collapse spaces and preserve
  case / lower + underscores / lower + hyphens.
* **scores** — how score ties are broken: name ascending / name descending /
  stable input order.

Each task has **>= 3 variants**. Before the run, the variant assignment per
(task, trial) is drawn from a seeded shuffled rotation, so every variant is
exercised about equally (and `--seed` reproduces an assignment exactly). The
chosen variant is stated in the phase-1 prompt, recorded in the report, and
checked against the variant's hidden test.

### Why non-seeding matters

The harness **never** creates decision nodes. For anchor-ish arms it creates
only a goal and two task nodes; the phase-1 agent (or the mock agent) must run
`anchor node create --type decision ...` itself. That is deliberate:

* if the harness seeded the convention, the benchmark would measure "does
  Anchor's seed survive?" instead of "does an agent leave enough durable state
  for a successor?";
* it keeps the measured quantity honest: `decisions_recorded` is true only when
  a decision node carrying the variant actually exists after phase 1, and
  `packet_decisions` / `variant_conveyed` are computed from the packet and the
  exact context handed to phase 2, not from what the harness intended to do;
* a real agent can fail to record the decision, and the report will say so
  instead of hiding it.

### Acceptance and anti-gaming

After each phase the harness compares the visible test files with the canonical
fixture bytes; a modified or deleted test is reported (`tests_modified`) and
**restored** before acceptance runs. Acceptance is real `go test -count=1
./...`; for phase 2 the hidden test file for the chosen variant is copied in
first. The cheater mock mode exercises this end to end: it tampers with visible
tests in phase 2, the tamper is detected and rolled back, and the hidden test
still fails, so cheating cannot buy a pass.

Anchored results also include the handoff gate: for the full arm the successor
must `anchor handoff restore --ack` and `--verify` the packet before phase 2
starts.

---

## 2. Arms (ablations)

The point of the arms is to separate *durable decisions* from *instructions*:

| arm | workspace | phase-1 wrap context | rules/skill in phase 2 | handoff packet | successor ack+verify |
|---|---|---|---|---|---|
| `baseline` | no | no | no | no | no |
| `rules` | yes | yes | yes | no | no |
| `packet` | yes | no | no | yes | no |
| `full` (= `anchor`) | yes | yes | yes | yes | yes |

* **baseline** — phase 2 gets only a generic "the previous session finished
  phase 1; continue" summary. No convention.
* **rules** — phase 2 gets Anchor's skill/rules text plus the wrap context file
  generated when phase 1 *started* (goal, tasks, constraints — no decisions),
  but no handoff packet.
* **packet** — phase 2 gets the handoff packet (which contains the decision the
  phase-1 agent recorded), but no rules/skill.
* **full** — rules/skill *and* the packet, and the harness runs
  `handoff restore --ack` then `--verify` before phase 2.

With the scripted mock agent the expected ordering is
`full >= packet >= rules >= baseline`; in practice baseline and rules are 0 and
packet/full are 1 for phase 2, because the mock only implements the convention
when the context it is *given* actually contains it. Mock results are a
mechanics self-test, not a claim about real agents.

### Anchor execution details for anchor-ish arms

For every anchor-ish trial the harness runs, inside a throw-away project copy:

1. `anchor init --attach --dir <project> --agents none`;
2. creates `goal-stress`, `task-phase1`, `task-phase2` via the CLI;
3. runs phase 1 under
   `anchor wrap --dir <project> --node task-phase1 --repo <project> [--prepare-handoff] -- <agent>`
   with `ANCHOR_BIN`/`ANCHOR_WORKSPACE`/`ANCHOR_CONTEXT_FILE` exported;
4. snapshots/commits the phase-1 result (the wrap packet is refreshed if the
   canonical test restore left the tree different from the packet); a missing
   packet falls back to `anchor handoff prepare`;
5. for `full`, acks and verifies the packet before the phase-2 process starts.

A failed wrap falls back to running the agent directly (`wrap_fallback` is
reported). A real agent that never records a decision produces
`decisions_recorded=false`, an empty `packet_decisions` count and
`variant_conveyed=false` — that is the measurement, not an error.

---

## 3. The mock agent (offline self-test)

`--mock-agent` re-executes the benchmark binary as a scripted agent process (a
genuine fresh process, not a goroutine). Its behaviour:

* **phase 1** writes the reference implementation (identical for every
  variant), records the convention as a decision node **through the real anchor
  CLI** when the arm has a workspace, and commits its work so the wrap packet
  has a clean code state;
* **phase 2** reads the prompt and every context file/packet it was actually
  given. It writes the variant-correct file **only if** the variant id or its
  distinctive statement phrase is present; otherwise it writes a naive
  implementation that is wrong for every variant.

Modes (`--mock-mode normal,sabotage,cheater`, cycled per trial):

* `normal` — honest behaviour as above;
* `sabotage` — phase 1 writes the stub and weakens the visible tests; detection
  restores the canonical tests, so phase 1 fails;
* `cheater` — phase 2 writes the naive file and tampers with the visible tests;
  detection restores them and the hidden test fails.

The executable's own test binary uses the same re-exec hook, so
`go test ./bench/stress` exercises the identical mechanism.

---

## 4. What is measured (report fields)

Every run writes `report.json`, `report.md` and a CLI summary. The report
contains:

* **config/metadata** — suite name/version, schema version, task names and
  SHA-256 hashes, arms, trials, variant seed, mock mode(s), agent command/model,
  anchor version and binary, Go version, OS/arch, timeout, output dir;
* **trial rows** — task, variant (+ statement), trial, arm, `phase1_pass`,
  `phase2_pass`, `decisions_recorded`, `decision_node_ids`, `packet_decisions`,
  `variant_conveyed`, `tests_modified` (plus per-phase variants), handoff
  prepared/refreshed/acked/verified, agent exit codes, phase1/phase2/wall
  milliseconds, tokens, cost, artifact paths, error/notes;
* **per-arm aggregates** — phase1/phase2 pass@1, Wilson 95% CI for both, phase2
  pass^k (`C(c,k)/C(n,k)`, k = trials per task), mean tokens/cost, compliance
  counts, variants used;
* **per-task** phase-2 pass counts per arm (discrimination is visible per task,
  not only averaged);
* **budget** — `--max-usd` / `--max-tokens` caps, spend, `truncated` and
  `budget_exceeded` flags plus a note when a run stops early;
* **artifacts** — per trial: phase-1/phase-2 prompts, agent logs, acceptance
  logs, patch, wrap context, handoff packet, hidden test, restore logs and mock
  plans (under `<out>/trials/<task>-tNN-<arm>/`);
* **limitations** — the honest limits below, repeated in every report.

`--json` prints the same report as JSON on stdout; it is also always written to
`<out>/report.json`.

---

## 5. Agent protocol (for `--agent-cmd` wrappers)

For each phase the prompt is written to a file and passed to the agent:

* the prompt text is appended as the **last argument** of `--agent-cmd`, unless
  the command contains `{prompt}` (inline text) or `{prompt_file}` (path);
* environment variables (in addition to `ANCHOR_*` set by `anchor wrap` in
  phase 1):
  `STRESS_PHASE`, `STRESS_TASK`, `STRESS_ARM`, `STRESS_PROJECT`,
  `STRESS_PROMPT_FILE`, `STRESS_VARIANT` (phase 1 only),
  `STRESS_ANCHOR_WORKSPACE`, `STRESS_CONTEXT_FILES` (JSON array),
  `STRESS_PACKET`, `STRESS_RULES_FILE`, `ANCHOR_BIN`.

The phase-2 prompt already embeds the arm's context (skill text, wrap context
and/or handoff packet JSON), so an agent that only reads the prompt still sees
exactly what the arm conveys. Token/cost usage is parsed from JSON result
objects on the agent's stdout (`usage`, `total_cost_usd`, `model`), like other
benchmark harnesses in this repository.

---

## 6. Running with a real agent

```sh
# build once (the harness also builds ./cmd/anchor itself by default)
go build -o /tmp/anchor ./cmd/anchor

# 3 trials per (task, arm), all four arms
go run ./bench/stress \
  --agent-cmd "claude -p --output-format json" \
  --agent-model deepseek-v4-flash \
  --trials 3 \
  --out bench/results/stress-claude

# a budget-capped run, ablations only
go run ./bench/stress \
  --agent-cmd "claude -p --output-format json" \
  --arms baseline,rules,packet,full \
  --trials 5 --max-usd 5 --out /tmp/stress-real

# inspect the report
cat bench/results/stress-claude/report.md
```

Useful flags: `--arms baseline,rules,packet,full` (`anchor` is accepted as an
alias for `full`), `--tasks money,names,scores`, `--trials N`, `--seed N`,
`--json`, `--keep-work`, `--timeout 10m`, `--anchor-bin PATH`,
`--max-usd`, `--max-tokens`.

Re-running with the same `--seed` reproduces the variant assignment exactly.

---

## 6b. First real run (2026-11-17) — and what it exposed

Claude Code 2.1.252 + DeepSeek `deepseek-v4-flash`, 3 tasks × 2 arms
(baseline/full) × 2 trials, 300 s/phase:

| arm | phase2 pass@1 | phase2 pass^k | phase2 95% CI | mean tokens | mean cost |
|---|---|---|---|---|---|
| baseline | 6/6 (100%) | 100% (k=2) | 61–100% | 357,926 | $0.585 |
| full | 6/6 (100%) | 100% (k=2) | 61–100% | 764,839 | $0.945 |

Total spend: 6.74M tokens / $9.19. Full recorded and conveyed the convention
6/6 (decisions/packet/context), baseline 0/6.

**The baseline still passed because phase 1 wrote the convention into the
repository**: the artifacts show a new `CONVENTIONS.md` (plus package comments)
containing the variant id and full rule, so phase 2 recovered it without any
session state. This is a genuine modeling flaw in this stress design: the suite
assumes the convention is not recoverable from the repo, but a capable agent can
*make* it recoverable. The harness now reports `variant_leaked_in_repo` per
trial and per arm (scanning files and commit messages for the variant id /
phrase), and the phase-1 prompt explicitly forbids writing the convention into
the repo. Even with that instruction this cannot be enforced, so a valid run
must either **invalidate leaked trials** or use a **leak-proof protocol** (e.g.
phase 2 starts from a harness-provided canonical phase-1 repository and only the
agent's durable decisions cross the boundary). Until then, treat these numbers
as: the harness works end-to-end with a real agent, and full is ~62% more
expensive per trial than baseline here — not as evidence about continuity.

## 7. Honest limits

* **Local isolation, not real continuity.** The two phases are separate
  processes on one machine. This removes context, but it does not model
  different machines, swapped agents, or multi-week gaps.
* **The mock agent is not evidence.** Mock mode proves the harness mechanics,
  the ablations and the acceptance tests discriminate; it says nothing about
  any real agent's willingness or ability to record decisions.
* **Compliance is measured, not assumed.** Real runs report
  `decisions_recorded`, `packet_decisions` and `variant_conveyed`; a low
  `variant_conveyed` rate means the arm's context did not actually carry the
  convention, whatever the arm was supposed to provide.
* **The repository is still visible in phase 2.** If a real phase-1 agent
  writes the convention into tracked files (a README, a comment, a note), every
  arm can recover it and the baseline rises. The mock deliberately does not do
  that; real results should be read with that possibility in mind.
* **The rules arm keeps a live workspace.** A real agent that queries
  `anchor tree` can recover the decision even without a packet, so a real rules
  arm may be as good as `full`; the mock only reads the context it is given, so
  the mock's rules result is a lower bound.
* **Budget caps depend on reported usage.** `--max-usd`/`--max-tokens` are
  checked against agent-reported usage (parsed from JSON result lines). An
  agent that reports nothing cannot trip the caps; the report's token/cost
  numbers only cover what was reported.
* **Small N.** Three authored tasks and a handful of trials show
  discrimination; they are not a capability leaderboard. Report CIs are wide
  on purpose.

## 8. Layout

```
bench/stress/                     harness (this package)
  main.go  runner.go  agent.go  mockagent.go  fixtures.go  stats.go  report.go
  suite.go  proc_unix.go  proc_windows.go  stress_test.go  README.md
bench/fixtures/stress/<task>/
  repo/                           nested Go module the agent works in (stub + visible tests)
  prompts/phase1.md, phase2.md    phase-1 prompt has {{CONVENTION_*}} placeholders; phase-2 is generic
  conventions.json                >= 3 variants: id, statement, phrase, hidden test, mock impl
  hidden/<variant>_test.go        hidden acceptance test per variant
  mock/phase1/                    reference phase-1 implementation (same for all variants)
  mock/phase2/<variant>/          variant-correct phase-2 implementation
  mock/phase2/wrong/              naive implementation used when the variant is not conveyed
```

Each fixture task directory and its `hidden/` directory are separate nested Go
modules so the repository's own `go test ./...` does not compile intentionally
failing or unresolved fixture code.

## 9. Verifying the harness

```sh
gofmt -l bench/stress        # must be empty
go vet ./bench/stress
go test ./bench/stress -count=1
go run ./bench/stress --mock-agent --trials 2 --out /tmp/stress
```

The test suite asserts, among other things: baseline phase-2 pass rate 0 vs full
1, ablation ordering packet >= rules >= baseline, variant coverage and
seed determinism, report shape/CI/pass^k/budget, test-edit detection and
canonical restore, the full-arm packet refresh path, and that visible tests and
phase-2 prompts do not leak any variant.

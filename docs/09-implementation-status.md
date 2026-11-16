# Implementation Status (audited)

**Audit date:** 2026-11-16
**Baseline:** audited at `1a6acc5`; since then the local segment, the test
coverage pass, the successor restore/gate and code-byte transport landed.
Current tree: 100 Go files / ~34,500 LOC, of which ~20,800 lines are tests
(451 top-level test functions, 1020 `=== RUN` entries).
The authoritative per-requirement status is the ledger in §5.
**Method:** code audit plus execution. `go build ./...`, `go test ./... -count=1`
and `go test -race ./...` pass for every package (`pathguard` now has tests);
`go vet` and `gofmt -l` are clean. Cross-compilation verified for
darwin/linux/windows on amd64 (arm64 also checked earlier).

**Why this file exists:** `README.md`, `acceptance/matrix.yaml` and
`docs/05-acceptance-mapping.md` describe a pre-implementation baseline. They are
stale: roughly half of the local core now exists as code with tests. This file
records the *actual* status so reviews do not have to re-derive it.

**This file is not a design change and authorizes no remote operation.**

## 0. Rubric

| Status | Meaning |
|---|---|
| `implemented` | Code path exists **and** a Go test asserts the behavior. |
| `partial` | Code exists but a major clause of the requirement is absent or untested. |
| `missing` | No code path at all. |
| `out-of-scope` | `docs/` explicitly defers it. |
| `by-absence` | Satisfied only because the feature does not exist (no network/telemetry/deps). Not positive evidence. |

Where `docs/` claims a capability but no code was found, the row is marked
`missing` rather than trusting design text.

## 1. Summary

| Domain | Implemented | Partial | Missing | Deferred | Total |
|---|---:|---:|---:|---:|---:|
| WS | 4 | 3 | 1 | 0 | 8 |
| ST | 5 | 5 | 3 | 0 | 13 |
| CAP | 4 | 5 | 0 | 0 | 9 |
| RST | 9 | 0 | 2 | 0 | 11 |
| AD | 4 | 7 | 2 | 0 | 13 |
| CM | 0 | 1 | 12 | 0 | 13 |
| SY | 2 | 6 | 4 | 0 | 12 |
| CFG | 8 | 0 | 0 | 0 | 8 |
| VT | 0 | 3 | 7 | 0 | 10 |
| UI | 1 | 5 | 1 | 0 | 7 |
| CONC | 2 | 3 | 0 | 0 | 5 |
| EVID | 1 | 4 | 1 | 0 | 6 |
| RT | 1 | 1 | 0 | 1 | 3 |
| SEC | 0 | 4 | 3 | 0 | 7 |
| NFR | 2 | 2 | 3 | 0 | 7 |
| OPS | 1 | 3 | 4 | 0 | 8 |
| **Total** | **44** | **52** | **43** | **1** | **140** |

Phase position (`docs/06` §4): **Phase 0 done, Phase 1 mostly done, Phase 2/3 partially done
(local-only). Phases 4–8 are unstarted.**

## 2. What is genuinely done

1. **Deterministic causal event store** — append-only DAG replay, event-ID tie-break,
   same-base-revision and concurrent-ownership isolation, revision/expected-revision
   checks, lease/fencing validation (`internal/core/replay.go`). Heavily tested.
2. **Crash-safe writes** — one manifested JSON batch per transaction, temp+fsync+rename,
   flock with crash release, all-or-nothing transactions, quarantine of corrupt event
   files, rejection of raw-append bypass (`internal/core/transaction.go`,
   `internal/core/lock_unix.go`).
3. **Evidence integrity chain** — content-addressed blobs, re-hash/size/symlink/secret
   verification during replay, `done` gate requiring node-bound verified evidence,
   read-only orphan/legacy audit (`internal/core/evidence.go`).
4. **Honest handoff snapshot + local export/import** — git code state, content-hashed
   packet, per-file hashed byte export, verified dry-run import that never executes or
   overwrites, explicit `incomplete`/`metadata_only` labels (`internal/handoff`,
   `internal/recovery`).
5. **Adapter capture skeleton** — static D-grade descriptors with explicit
   `not_claimed`, metadata-only normalization with hashed session/cwd, deterministic
   idempotency keys, session seen/ended state machine, golden fixtures
   (`internal/adapter`).
6. **Path/secret/lock hardening** — canonicalized paths, shared agent-config refusal,
   `O_NOFOLLOW` evidence reads, 0600/0700 modes, symlink/traversal rejection
   (`internal/pathguard`, `internal/core`).
7. **Task-structure model** — goal nodes carry north star, must/must-not
   constraints and success criteria; decision nodes carry decision, rationale,
   alternatives and consequences; every node can carry provenance + confidence,
   with replay rejecting goal fields on non-goals, decision fields on
   non-decisions, unknown provenance, out-of-range confidence, empty constraint
   entries, any `user_confirmed` claim, and any event carrying a self-declared
   `actor_attestation` (`internal/core`).
8. **Successor restore + gate** — `handoff restore` emits a read-only plan
   (code comparison, blocker list, actions), records `handoff_acked` /
   `handoff_verified` events, and refuses node mutations until the latest
   prepared handoff is verified. The gate is derived from `handoff_prepared`
   events (not the packet file), so deleting or moving a packet cannot disable
   it, and raw `event append` cannot forge ack/verify. Preparation is recorded by
   every path that creates a packet (including export-first). A successor whose
   code state exactly matches the packet can ack without `--force`; verification
   requires HEAD *and* the working tree to match. Restore itself never applies
   code: the plan states `code_apply: disabled`.
9. **Adapter detection + Claude hook installation** — `adapter detect` probes the
   real CLIs (`claude --version`, `codex --version`, …) with an argv-only,
   timeout-bounded call and never upgrades hook evidence; `adapter install`
   merges managed `PostToolUse`/`SessionEnd` capture hooks into Claude Code's
   `settings.json` (project or explicit user scope) with a plan by default,
   idempotent apply, backup, managed-state file and a drift-tolerant uninstall;
   the installed command writes one metadata-only capture file per hook into the
   workspace and prints nothing. Evidence remains contract-level: a real in-agent
   run is impossible here because Claude Code is installed but not logged in.
10. **Code byte transport** — `handoff export --include-code` bundles the prepared
   commit (verified with `git bundle list-heads`) and `handoff apply-code`
   restores it into a repository: fetch, backup ref, stash when forced, checkout,
   then applies the packaged staged/unstaged diffs and untracked bytes after
   hash and secret checks. Packaged code must match the packet's recorded hashes
   (validated before anything is mutated), incomplete packages are refused,
   writes cannot traverse symlinked directories, ignored local files that would
   be overwritten are detected (and stashed with `--all` only under `--force`),
   an existing local branch is never reset, handoff ids and branch names are
   validated, and a failure rolls the worktree back to the pre-apply commit.
   Code is never applied unless `--apply` is explicit.
11. **Restorable packages** — export always transports the workspace manifest and
   event log; import rebuilds the workspace in a fresh empty target, replays it,
   verifies evidence blobs and reports an honest `complete`/`incomplete` status.
   Legacy artifact-only packages still import their bytes but are reported as not
   rebuilding a workspace.
12. **Hookless fallback (`anchor wrap`)** — runs the agent CLI as a child with
    inherited stdio and exact argv (never through a shell), records
    `adapter_session_seen`/`adapter_session_ended` for adapter `wrap`, exports
    `ANCHOR_WORKSPACE`/`NODE`/`SESSION`/`CONTEXT_FILE`, writes a context file
    derived from the Session Tree (goal constraints, decisions, tasks, open
    questions), forwards SIGINT/SIGTERM/SIGHUP, propagates the child's exit code
    and optionally prepares the successor handoff at exit. Captured session
    evidence stays `hook_unverified` / `unverified_adapter`.
13. **Read-only MCP server + registration** — `anchor mcp serve` speaks
    newline-delimited JSON-RPC 2.0 over stdio with four read-only tools
    (`anchor_status`, `anchor_tree`, `anchor_evidence_verify`,
    `anchor_restore_plan`), and `adapter install --with-mcp` registers that server
    in the project's `.mcp.json` (merged, backed up, idempotent; uninstall removes
    only the recorded entry). No mutation tool is exposed over MCP.
14. **A/B benchmark harness** — `bench/` contains a small offline fixture project,
    a two-phase (implement → resume with a fresh process) protocol, hidden
    acceptance tests restored after the agent exits, and a runner
    (`go run ./bench/ab`) with baseline vs anchor arms, a deterministic mock
    agent for CI, token/cost accounting (parsed from the agent's result JSON)
    and JSON/markdown reports. A real Claude Code + `deepseek-v4-flash` run
    (3 trials/arm, 600 s/phase) completed: baseline phase-2 pass 0/3 vs anchor
    3/3, at +33% total tokens and +$0.087 mean cost; the baseline's failed
    phase-2 attempts still consumed ~184k tokens. Results and caveats are in
    `bench/results/2026-11-16-deepseek-v4-flash/` and `bench/README.md` §7.
15. **Tree view + local web UI + agent skill** — `anchor tree` renders the
    Session Tree as a terminal tree, stable JSON, Markdown (`TREE.md`) or a
    self-contained HTML file (escaped titles, no external resources, tombstones
    hidden by default); `anchor ui` serves the same view as a live, loopback-only,
    GET-only web page (no mutation surface, re-reads state per request);
    `anchor state` aliases `status`. `anchor skill print|install` embeds an
    opinionated agent playbook (read context → check tree → record
    decisions/evidence → prepare/verify handoff) and installs it to
    `<project>/.claude/skills/anchor/SKILL.md` with plan/backup/idempotent/
    uninstall semantics. `anchor mcp config` prints per-agent MCP registration
    snippets. `anchor config init/plan/apply/status/rollback` provides one
    source of truth for per-agent rules/MCP/skill/hook configuration: managed
    blocks with user content preserved, plan/diff, drift detection, secret
    *references* only (literals rejected), project-over-user precedence and
    pre-apply snapshots/rollback. Claude Code hooks are config-owned, including a
    SessionStart hook that injects size-capped, secret-scanned context (goal
    constraints, decisions, tasks, handoff) as `additionalContext`; Codex/Gemini
    receive the same playbook through their rules file.
16. **No `unsafe`, no third-party dependencies, no network or telemetry code.**

## 3. Requirement-by-requirement status

This section keeps the evidence notes gathered during the audit. Where a row
groups several requirements or was superseded by later work, the ledger in §5 is
authoritative.

### 3.1 WS — Workspace / event store

| Req | Status | Evidence | Gap |
|---|---|---|---|
| WS-001 | implemented | `internal/core/store.go:81` (in-place) + `StateDir`/`InitAttached` (attach); `TestCLIInitAttachInExistingProject`, `TestInitAttachedLeavesProjectFilesUntouched`, `TestStateDirResolvesAttachedWorkspace` | `init --attach` adopts an existing project with state in `<project>/.anchor`, ignores it in the project `.gitignore`, and fails closed on re-attach or half-initialized state. Remaining niceties: no interactive prompt, no multi-workspace-per-project. |
| WS-002 | partial | `cmd/anchor/tree.go`; `TestCLITreeMarkdownAndHTML`, `TestDeterministicReplayIndependentOfSliceOrder` | `anchor tree --md` writes a `TREE.md` projection and `status --json` a state projection; still no `branch` command and no Git-merge test, and event batches stay coarse one-line JSON. |
| WS-003 | partial | `internal/core/event_format_audit.go:83`, `store.go:73`; `TestCLIEventFormatDowngradeRejectedAndAudited` | Read-only audit only: no migrate, no backup, no dry-run plan, no rollback. |
| WS-004 | implemented | `internal/core/transaction.go:143,231`, `lock_unix.go:16`; `TestTransactionBatchIsAllOrNothing`, `TestWorkspaceLockReleasedOnProcessCrash` | Directory fsync is best-effort (`_ = d.Sync()`). |
| WS-005 | partial | `internal/adapter/capture.go:102`; `TestDirtyRepoIsIncompleteAndSecretNotInPacket` | No sync boundary to test; no workspace-wide assertion; `evidence --file` accepts absolute paths outside the workspace. |
| WS-006 | implemented | `internal/core/replay.go:42`, `evidence.go:229`; `TestDeterministicReplayIndependentOfSliceOrder` | Rebuild is in-memory only; no persisted projections. |
| WS-007 | implemented | `internal/recovery/recovery.go` (`Export`/`Import`); `TestCLIExportImportRebuildsWorkspace`, `TestImportLegacyPackageMirrorsBytesWithoutRebuilding` | Export carries the workspace manifest + event log; import rebuilds, replays and verifies. Still missing: config/rules snapshot, bundled commit bytes. |
| WS-008 | missing | no `uninstall` verb (`cmd/anchor/main.go:31`) | Entire behavior absent. |

### 3.2 ST — Session Tree

| Req | Status | Evidence | Gap |
|---|---|---|---|
| ST-001 | implemented | `internal/core/tree.go` (`Node.NorthStar/Must/MustNot/SuccessCriteria`), CLI flags; `TestGoalDetailFieldsAreProjected`, `TestCLINodeDetailFieldsAndPacket` | Fields recorded, validated, projected and carried in the packet. |
| ST-002 | implemented | `internal/core/tree.go`, replay validation; `TestDecisionDetailFieldsAreProjected` | Decision fields recorded, validated, projected and carried in the packet. |
| ST-003 | implemented | `internal/core/tree.go:20,102`; `TestLegalAndIllegalTransitions`, `TestCLIConsecutiveTransitions` | — |
| ST-004 | partial | `internal/core/tree.go:136` | Fork parent may be any node type; no hypothesis field; no merge command or event. |
| ST-005 | implemented | `core.Provenance`, `Node.Provenance/Confidence`, replay validation; `TestProvenanceAndConfidenceAreValidated`, `TestSelfDeclaredAttestationCannotGrantUserConfirmation` | Values are recorded and enforced. `user_confirmed` is rejected unconditionally and events carrying an unverifiable `actor_attestation` are rejected, because no attestation mechanism exists yet (ST-013). |
| ST-006 | partial | `internal/core/event.go:46,56,81` | `ActorKind` never set by any command; nodes store no actor/agent/session/time; no agent version field. |
| ST-007 | implemented | `internal/core/event.go:40`, `transaction.go:83,46`; `TestCLIConcurrentSameNodeOneWins` | CLI node mutations generate no idempotency key (only adapter ingest does). |
| ST-008 | partial | `internal/core/replay.go:548,609`; `TestP1TombstoneIsTerminal` | Edit-vs-delete relies on the generic same-base-revision rule; no dedicated conflict class or test. |
| ST-009 | partial | `internal/core/replay.go:516`, `tree.go:102`; `TestP1NonGoalRequiresParent` | No reparent, so cycle prevention is vacuous; no branch lineage/merge validation. |
| ST-010 | missing | grep `drift|constraint` → 0 | No drift checks, no advisory/block distinction. |
| ST-011 | partial | `cmd/anchor/main.go:529,542,554` | No merge, no reparent; "agent cannot self-confirm" is vacuous without provenance. |
| ST-012 | missing | no summary event type (`event.go:15`) | No summary storage or source-pointer rule. |
| ST-013 | missing | `user_override` rejected (`replay.go`, `main.go`); `actor_attestation` rejected in replay; `TestSelfDeclaredAttestationCannotGrantUserConfirmation` | No verified local attestation mechanism, so user confirmation is refused rather than faked. A TTY/UI/signature token still needs designing. |

### 3.3 CAP — Capture

| Req | Status | Evidence | Gap |
|---|---|---|---|
| CAP-001 | partial | `internal/adapter/capture.go:67`, `cmd/anchor/adapter.go:209` | No native hook installed/verified; all hook classes collapse to session seen/ended. |
| CAP-002 | partial | `cmd/anchor/adapter.go:250` | No in-agent capture path, so fail-open is unexercised. |
| CAP-003 | partial | `capture.go:72`, `adapter.go:15`; `TestCaptureRejectsSecrets` | No redaction (whole payload dropped); cap is a constant. |
| CAP-004 | implemented | `capture.go:99,105`; `TestCaptureGoldenAndContentOmission` | — |
| CAP-005 | implemented | `capture.go:16`, `event.go:81`; `TestCaptureGoldenAndContentOmission` | Only one event class is normalized. |
| CAP-006 | implemented | `capture.go:122`, `adapter.go:166`; `TestCaptureSourceIdentityAndIdempotency` | — |
| CAP-007 | implemented (by-absence) | zero deps in `go.mod`; no `net/http` import | No explicit offline test. |
| CAP-008 | partial | `capture.go:105`, `adapter.go:15` | Tool I/O never captured; no configurable limits; no deny-path list. |
| CAP-009 | partial | doctor checks manifest/events/recovery only (`main.go:710`) | No capture/adapter check in doctor; no log sink. |

### 3.4 RST — Restore / handoff

| Req | Status | Evidence | Gap |
|---|---|---|---|
| RST-001 | implemented | `handoff.Packet` (`goal`/`decisions`/`next_steps`/`open_questions`/`ownership`/`provenance_summary`); `TestPrepareCarriesTaskStructure` | Packet now carries the task structure a successor needs; packet hash covers the new blocks. |
| RST-002 | implemented | `cmd/anchor/adaptercontext.go`, `adapter.Install` SessionStart hook; `TestCLIAdapterContextHookJSON`, `TestCLIAdapterContextTruncates` | SessionStart hook emits Claude Code hook JSON with `additionalContext` built from the Session Tree and latest handoff; size-capped (`--max-bytes`), secret-scanned, and installed by both `adapter install` and `anchor config apply`. |
| RST-003 | implemented | `core.EvHandoffAcked/Verified`, `handoff.BuildPlan`/`Gate`; `TestHandoffAckAndVerifyAreProjected`, `TestCLIRestoreSuccessorGateFlow` | Ack/verify are recorded events; the gate blocks mutations until verification; `--force` is recorded. |
| RST-004 | implemented | `handoff.go:248`, `main.go:484`; `TestCLIWorkflowRecoveryAndRestoreBoundary` | Partially vacuous: no summary object exists. |
| RST-005 | missing | one static packet format only | No per-adapter cross-agent handoff. |
| RST-006 | missing | export/import now transports the workspace (`recovery.go`); no Git sync | Handoff across devices works only by manually moving a package; the requirement's Git-sync path does not exist. |
| RST-007 | implemented | `recovery.ExportWithCode`, `recovery.ApplyCode`; `TestApplyCodeRestoresUncommittedWorktree`, `TestCLIExportApplyCodeEndToEnd` | Bundle carries the commit; diffs and untracked bytes carry the dirty state; apply-code restores both with hash checks. |
| RST-008 | implemented | `handoff restore --ack/--verify`, plan actions; `TestCLIRestoreStaleCodeRequiresForce` | Restore is plan-first; restoring *code* is a separate explicit `handoff apply-code --apply`. A mismatched code state needs `--force` (recorded as forced). |
| RST-009 | implemented | `handoff.BuildPlan` blockers/actions; export/import warnings; `apply-code` blockers; `TestBuildPlanStaleCodeAndGateLifecycle`, `TestApplyCodeRefusesIncompleteCodePackage` | Restore plans enumerate stale code, dirty trees and missing evidence; export/import/apply-code enumerate missing bytes and refuse an incomplete package. |
| RST-010 | implemented | `Node.Provenance` + packet blocks; `TestPrepareCarriesTaskStructure` | Provenance is labeled per goal/decision/next step/question and summarized; `user_confirmed` is refused until a verified attestation exists. |
| RST-011 | implemented | `handoff.go:103`, `recovery.go:433`; `TestDirtyRepoIsIncompleteAndSecretNotInPacket` | Code apply disabled, so "unrecovered" is always true; no per-artifact list. |

### 3.5 AD — Adapters

| Req | Status | Evidence | Gap |
|---|---|---|---|
| AD-001 | implemented | `adapter.Detect`/`DetectAll`; `TestDetectRealClaudeWhenInstalled`, `TestCLIAdapterDetectReportsRealVersions` | Real, timeout-bounded `--version` detection; descriptors stay explicit and hook evidence stays `unverified`. |
| AD-002 | partial | `install.go`; `TestCLIAdapterInstallContractE2E`, `TestCLIAdapterInstallMCPRegistrationE2E` | Claude Code hooks + MCP server can be installed/uninstalled; rules-file (CLAUDE.md managed block) management is still missing. |
| AD-003 | partial | `render.go:59`; `TestRenderGolden` | Fragment only: no AGENTS.md, MCP, install, trust handling. |
| AD-004 | partial | `render.go:50`; `TestRenderGolden` | Fragment only: no GEMINI.md, extensions, MCP, install. |
| AD-005 | implemented | `cmd/anchor/wrap.go`; `TestCLIWrapRunsChildAndRecordsSession`, `TestCLIWrapForwardsTerminationSignals` | Hookless fallback: session seen/ended events, context file + env injection, exact argv, signal forwarding, exit-code propagation, optional handoff prepare. |
| AD-006 | implemented (claude-code) | `adapter.Install`; `TestInstallPlanThenApplyThenUninstall`, `TestCLIAdapterInstallContractE2E` | Plan-first install, idempotent apply, backup, managed state, uninstall, drift tolerance. Codex/Gemini installs are not implemented. |
| AD-007 | partial | `contract.go:13`, `render.go:35` | Descriptor has no version field; per-ID switch is a cross-agent assumption. |
| AD-008 | partial | `contract.go:30`; `TestCLIAdapterDryRunCaptureAndNoSharedInstall` | Fixtures carry no `source`/retrieval-date field; no UI labels. |
| AD-009 | partial | `install.go` (absolute exe, quoted shell command, timeout), `wrap.go` (argv); `TestInstallRefusesUnsafeTargets` | Hook commands are still shell strings (the native schema requires one), though they use an absolute, quoted executable and a timeout; no env allowlist or process-group handling. |
| AD-010 | implemented | `internal/mcp`; `TestServeProtocol`, `TestCLIMcpServeContract`, `TestCLIAdapterInstallMCPRegistrationE2E` | Read-only MCP stdio server (status/tree/evidence/restore-plan) plus project `.mcp.json` registration via `adapter install --with-mcp`; no mutation tools by design. |
| AD-011 | missing | no injection or limit constants | Entire behavior absent. |
| AD-012 | missing | no config management | No drift detection or explicit re-apply. |
| AD-013 | partial | `contract.go:37` | No enforcement-state field, no capture-failure log, no policy hook. |

### 3.6 CM — Causal-memory bridge

The causal-memory *engine* is a non-goal (NG-003, D-004/D-005); the Anchor-side
remote-MCP client is in scope (phase 4). **No MCP client code exists**; `mcp`
matches only capability strings.

| Req | Status | Evidence | Gap |
|---|---|---|---|
| CM-001 | partial | no SQLite/DB/engine code | Prohibition satisfied; positive clause (use remote endpoint) absent. |
| CM-002 – CM-013 | missing | — | No HTTPS client, vault token, tool discovery, causal link model, retry/jitter, offline queue, circuit breaker, schema validation, refs, retention, or cross-device causal write key. |

### 3.7 SY — Sync

| Req | Status | Evidence | Gap |
|---|---|---|---|
| SY-001 | missing | no `git push/pull/fetch` anywhere | Entire behavior absent. |
| SY-002 | missing | no remote operations | No divergence detection, no force-push guard. |
| SY-003 | partial | `replay.go:170`; `TestSameRevisionConcurrentMutationsBothIsolate` | Replay-level union only; no merge driver, no deletion-conflict surfacing. |
| SY-004 | partial | `replay.go:609`; `TestP1TombstoneIsTerminal` | Tombstone survival across devices untested (no sync). |
| SY-005 | implemented | `recovery.ExportWithCode`/`ApplyCode`; `TestApplyCodeRoundTripIntoFreshRepo` | Commit bundle + staged/unstaged/untracked bytes are hashed, size-limited and restorable via apply-code. |
| SY-006 | missing | no sync code | No opt-in, credential handling, or secret-free remotes. |
| SY-007 | missing | no queue | Offline queue absent. |
| SY-008 | partial | `store.go:133`, `replay.go:46`; `TestDeviceSeqMonotonic` | No last-sync state; no clock-skew handling. |
| SY-009 | partial | `docs/06` §3 O-001 | Documented default only; no config key or code boundary to choose topology. |
| SY-010 | partial | `replay.go:156`, `main.go:595`; `TestP0ConcurrentTakeoversConflict` | No lease expiry, no stale-owner detection, no audit query. |
| SY-011 | partial | `event.go:127`, `handoff.go:266`, `recovery.go:287` | No remote verification path; no signatures. |
| SY-012 | implemented | `recovery.go`; `TestCLIExportImportRebuildsWorkspace` | `complete` is reachable only when the rebuilt workspace replays cleanly and all referenced evidence verifies; imported code bytes are staged, never applied. |

### 3.8 CFG — Config / rules / skills

| Req | Status | Gap |
|---|---|---|
| CFG-001 | implemented | `cmd/anchor/config.go`; `TestCLIConfigOwnsHooks`, `TestCLIConfigApplyRendersManagedBlocks` | One config file drives rules blocks, MCP registration, the Claude skill **and** native hook installation (including SessionStart context injection); `adapter install` remains a shortcut for hook-only setups. |
| CFG-002 | implemented | `config.go` managed markers + merge; `TestCLIConfigApplyRendersManagedBlocks`, `TestCLIConfigStatusDetectsDriftAndFixesIt` | Per-agent rules files get a managed block; user content outside it is preserved; apply is idempotent. |
| CFG-003 | implemented | `config plan/apply/status`; `TestCLIConfigStatusDetectsDriftAndFixesIt` | Plan by default, `--diff` shows managed-block differences, `status` detects drift/missing/never-applied. |
| CFG-004 | implemented | `validateSecretRefs`; `TestCLIConfigSecretRefsOnly` | Config accepts only `env:`/`keychain:`/`op://`/`file:` references; literal values are rejected and references are never rendered. |
| CFG-005 | implemented | project + user config merge; `TestCLIConfigScopePrecedence` | Project-scope config overrides user-scope config; precedence documented in README/docs/10. |
| CFG-006 | implemented | snapshots under `config/snapshots/`; `TestCLIConfigRollbackRestoresSnapshot` | Every apply snapshots the files it will touch; `config rollback [--to]` restores a snapshot (and its render state). |
| CFG-007 | implemented | `renderRulesBlock`; `TestCLIConfigApplyRendersManagedBlocks` | Rendered rules instruct agents to use Anchor at start, during branch/decision changes and at completion; includes MCP tool list. |
| CFG-008 | implemented | `internal/adapter/skill.go`; `TestCLISkillInstallPlanApplyUninstall`, `TestCLIConfigRendersSkillPlaybookForCodexAndGemini` | Claude Code gets the managed `.claude/skills/anchor/SKILL.md`; Codex/Gemini get the same playbook rendered into their rules file (their activation mechanism), with the adapter-specific semantics tested and documented instead of assuming a skill directory. |

### 3.9 VT — Vault / secrets

| Req | Status | Evidence | Gap |
|---|---|---|---|
| VT-001/002/003/005/006/007/008 | missing | no keychain/vault code | No backends, refs, injection, rotation, scoping, argv-safe secret path, or access audit. |
| VT-004 | partial | `secret_scan.go:27`; `TestCaptureRejectsSecrets` | Rejects rather than redacts; no logs/MCP/UI surface. |
| VT-009 | partial | scanning in capture/evidence/export; doctor does not scan (`main.go:710`) | No doctor/pre-commit/CI integration (no CI). |
| VT-010 | partial | synthetic tokens (`cli_test.go:620`, `adapter_test.go:80`) | Fixtures not labeled as fake; no real-key CI guard. |

### 3.10 UI — CLI / web UI

| Req | Status | Evidence | Gap |
|---|---|---|---|
| UI-001 | partial | `cmd/anchor/main.go`; `TestCLITreeTextAndJSON`, `TestCLISkillInstallPlanApplyUninstall` | `tree`, `state` and `skill` verbs exist (plus attach/wrap/wrap-related); still missing branch, claim/release/heartbeat, top-level capture/restore, sync, config, vault, memory, web, install, uninstall, migrate, version, ack. No local web UI yet (static HTML tree only). |
| UI-002 | partial | `--json` on 5 command families | No JSON for init/node/evidence add/handoff prepare; exit codes only 0/1/2. |
| UI-003 | partial | plain `fmt.Printf` (`main.go:483`) | No TTY detection, color, or structured human layout. |
| UI-004 | partial | `cmd/anchor/ui.go`; `TestUIHandlerServesTreeAPIAndPage` | Read-only loopback web view of the Session Tree (`anchor ui`); no evidence/sync/ownership views yet. |
| UI-005 | partial | `ui.go` (`validateUIAddr`, `isLoopbackAddr`); `TestValidateUIAddr` | Binds loopback only and rejects non-loopback `--addr`; no per-session token/auth (loopback-only deployment). |
| UI-006 | missing | no Origin/CSRF checks | Server is GET/HEAD-only and read-only, which limits CSRF impact, but no explicit Origin/CSRF validation exists. |
| UI-007 | implemented | `ui.go` 405 guard; `TestUIHandlerServesTreeAPIAndPage` | Read-only mode: no mutation routes; non-GET/HEAD requests get 405 with `Allow: GET, HEAD`. |

### 3.11 CONC — Ownership / concurrency

| Req | Status | Evidence | Gap |
|---|---|---|---|
| CONC-001 | partial | `main.go:279,571`, `event.go:101` | No claim/release/heartbeat commands; heartbeat event never emitted; `ExpiresAt` never enforced. |
| CONC-002 | partial | `main.go:595`, `replay.go:259`; `TestP0LatestOwnershipUsesCausalityNotDeviceSeq` | Takeover allowed at any time (no expiry); no audit query. |
| CONC-003 | implemented | `replay.go:556`; `TestExpectedRevisionMismatchIsConflict`, `TestCLIConcurrentSameNodeOneWins` | — |
| CONC-004 | partial | `tree.go:38`, `main.go:489`, `handoff.go:217` | No UI; packet has no explicit ownership block. |
| CONC-005 | implemented | `replay.go:46`; `TestDeterministicReplayIndependentOfSliceOrder` | — (ULIDs not used; IDs are random with timestamp prefix). |

### 3.12 EVID — Evidence

| Req | Status | Evidence | Gap |
|---|---|---|---|
| EVID-001 | partial | `tree.go:46`; `TestEvidenceArtifactRehashAndPathSafety` | Missing source, command/argv, exit code, timestamp, actor, verification state, redactions. |
| EVID-002 | partial | `replay.go:574`, `main.go:346`; `TestCLIDoneRequiresEvidenceOrLocalOverride` | Override hard-disabled (`main.go:315`), so the "or explicit override" branch cannot work. |
| EVID-003 | implemented | `core.EvHandoffVerified` projection; `TestHandoffAckAndVerifyAreProjected`, `TestCLIRestoreVerifyValidatesEvidence` | Verification is an event with the observed code head and re-verified evidence ids; mutations stay gated until it exists. |
| EVID-004 | missing | no causal link model | Causal links requiring evidence/confirmation absent. |
| EVID-005 | partial | `replay.go:403`, `evidence.go:92`; `TestEvidenceAuditReportsOrphansWithoutDeleting` | No correction event referencing old evidence IDs. |
| EVID-006 | partial | `handoff.go:248` (disclaimer) | Vacuous: nothing stores summaries, so the boundary cannot be enforced. |

### 3.13 RT — Roost boundary

| Req | Status | Evidence | Gap |
|---|---|---|---|
| RT-001 | out-of-scope | `docs/01-architecture.md:489` defers runtime scope; interface sketched at `:236` | No `internal/runtime` package or stub. |
| RT-002 | implemented (by-absence) | zero deps; no reference to roostbot/LangAlpha/market | No CI guard enforces the boundary. |
| RT-003 | partial | core has no Roost/network dependency | No feature flag or provider hook, so "opt-in" is unexercised. |

### 3.14 SEC — Security

| Req | Status | Evidence | Gap |
|---|---|---|---|
| SEC-001 | partial | `pathguard.go:19`, `store.go:156`, `handoff.go:103`, `evidence.go:67`; `TestP0EventIDTraversalRejected`, `TestInspectRejectsUnsafePathsSymlinksExtraAndOversize` | Not one unified validator; Windows drive/ADS/control-char canonicalization missing; `evidence --file` accepts absolute paths by design. |
| SEC-002 | partial | git invocations use argv without shell (`handoff.go:91`) | No absolute binary resolution, env allowlist, timeouts, or process-group kill; hook command is a shell string with bare `anchor` (`render.go:31`). |
| SEC-003 | partial | `capture.go:72`, `evidence.go:96`, `recovery.go:121`; secret untracked aborts export | No bundle-history, sync, commit/push scan; no doctor/CI hook. |
| SEC-004 | partial | ingest never executes hook input (`TestCLIAdapterIngestNeverExecutesHookInput`) | No policy engine, provenance labels, or MCP. |
| SEC-005/006/007 | missing | no network, sync, or web code | Entire behavior absent. |

### 3.15 NFR — Non-functional

| Req | Status | Evidence | Gap |
|---|---|---|---|
| NFR-001 | partial | `lock_windows.go:16`, `evidence_open_windows.go:13` | Only macOS exercised; no Windows/Linux CI, install, keychain, or e2e. |
| NFR-002 | missing | no benchmark or latency test | Hook p95/packet/UI budgets unmeasured. |
| NFR-003 | implemented (by-absence) | zero deps, no network imports | No explicit offline-mode test; "degrade visibly" has nothing to degrade from. |
| NFR-004 | partial | no telemetry/analytics/network code | No logging layer at all, so "local and redacted logs" is unimplemented. |
| NFR-005 | missing | no retention/deletion commands | Export/delete of transcripts/artifacts/queue absent. |
| NFR-006 | implemented | package split, no `unsafe`; `TestProductionBuildHasNoTestPauseHook` | — |
| NFR-007 | missing | no UI | Accessibility absent. |

### 3.16 OPS — Operations

| Req | Status | Evidence | Gap |
|---|---|---|---|
| OPS-001 | missing | no packaging/release files | No install packages, checksums, signatures. |
| OPS-002 | missing | only a version gate (`store.go:73`) | No migrate/backup/rollback/downgrade docs. |
| OPS-003 | missing | no uninstall | Entire behavior absent. |
| OPS-004 | missing | no `.github/`, no Makefile/lint config | No CI gates of any kind. |
| OPS-005 | partial | `docs/00`–`09` + README | Missing user guide, migration doc, uninstall doc, coverage report (this file adds status). |
| OPS-006 | partial | zero deps (`go.mod`), no postinstall | No SBOM, reproducible-build flags, or release provenance. |
| OPS-007 | implemented (by-absence) | no payment/network code; synthetic tokens only | No automated real-key scan (no CI). |
| OPS-008 | partial | `docs/01-architecture.md:236,489` | Docs-only; no `internal/runtime` interface stub or feature flag. |

## 4. Top gaps to close next

Ranked by "what blocks a usable local segment":

1. ~~**Make restore actually restore** (RST-003/008, EVID-003).~~ **Done** —
   `handoff restore` emits a plan, records `handoff_acked`/`handoff_verified` and
   gates node mutations until verification; code bytes are transported with
   `--include-code` + `handoff apply-code`.
2. ~~**Carry the workspace inside the export package** (WS-007).~~ **Done** — the
   package always carries the workspace manifest and event log; import rebuilds,
   replays and verifies. Remaining: config/rules snapshot and bundled commit bytes.
3. ~~**Give the packet the task structure a successor needs** (RST-001/010,
   ST-001/002/005).~~ **Done** — goal constraints, decision trade-offs, provenance,
   next steps, open questions and ownership are in the model and the packet.
4. ~~**Context injection + managed rules files** (RST-002, AD-002/003/004/006,
   CFG-002/007).~~ **Done** — `anchor config` renders managed blocks into
   `CLAUDE.md`/`AGENTS.md`/`GEMINI.md` with plan/diff, drift detection,
   snapshots/rollback and secret references, and installs a SessionStart hook
   that injects size-capped, secret-scanned context as `additionalContext`.
5. **Provenance + local user attestation** (ST-005/013, RST-010, EVID-002). Node
   provenance is enforced; event-level attestation and `override_recorded` are not.
6. ~~**Wire real capture** (CAP-001, AD-001/005/009).~~ **Done except live
   validation** — detection, Claude hook install/uninstall, `--capture-dir`, a
   doctor check and `anchor wrap` fallback all landed. Remaining: exercise the
   hook inside a logged-in agent (impossible here: Claude Code is not logged in).
7. ~~**Fix adoption: `init --attach`** (WS-001).~~ **Done** — state lives in
   `<project>/.anchor`, user files are untouched, and every command resolves the
   attached state root from the project directory.
8. **Human-readable projections + `tree` view + web UI** (WS-002, UI-001/004–007).
   **Partially done** — terminal/JSON/Markdown/HTML views, a read-only loopback
   web page (`anchor ui`) and `anchor state` exist; still missing a `branch`
   command, a `state.json` file projection, a Git-merge test and Origin/CSRF
   checks on the web server.
9. ~~**Expose Anchor tools over MCP** (AD-010) before the memory bridge.~~ **Done**
   — stdio server with four read-only tools, registered by `adapter install
   --with-mcp`. Deliberately no mutation tools yet.
10. ~~**Make dirty code recoverable** (RST-007/008, SY-005).~~ **Done** — git
    bundle + hashed diffs/untracked bytes, verified against the packet, applied
    with backup refs, stash and rollback.
11. **Complete the ownership lifecycle** (CONC-001/002). Lease expiry in replay,
    claim/release/heartbeat commands, stale-owner takeover only after expiry,
    ownership audit query. (Fencing and isolation already work; expiry is not
    enforced and heartbeat/release commands are missing.)
12. **Make it shippable** (WS-003/008, OPS-001..004, NFR-001). `migrate` with
    dry-run/backup/rollback, `uninstall --purge` confirmation, CI (gofmt/vet/race on
    3 OSes), checksummed release builds.
13. **Expand the A/B evidence** (benchmark). A real Claude Code +
    `deepseek-v4-flash` run now exists (3 trials/arm: resume 0/3 vs 3/3, +33%
    tokens, +$0.087 mean cost; `bench/results/2026-11-16-deepseek-v4-flash/`).
    Still needed for a defensible claim: N ≥ 5-10 per arm, multiple tasks, and
    adapting public benchmarks (SWE-bench Verified/Terminal-Bench/Aider
    polyglot; see `bench/README.md` §3) instead of one custom fixture.

## 5. Status ledger (authoritative)

Every requirement in `acceptance/matrix.yaml` appears exactly once.

### WS

| Req | Status | Note |
|---|---|---|
| WS-001 | implemented | `init --attach` adopts an existing project; state in `<project>/.anchor` |
| WS-002 | partial | TREE.md via `tree --md` + state JSON; no branch/merge test |
| WS-003 | partial |  |
| WS-004 | implemented |  |
| WS-005 | partial |  |
| WS-006 | implemented |  |
| WS-007 | implemented | export always carries workspace manifest + event log; import rebuilds and verifies |
| WS-008 | missing | no uninstall verb |

### ST

| Req | Status | Note |
|---|---|---|
| ST-001 | implemented | goal north star, must/must-not, success criteria |
| ST-002 | implemented | decision, rationale, alternatives, consequences |
| ST-003 | implemented |  |
| ST-004 | partial |  |
| ST-005 | implemented | provenance + confidence; user_confirmed still requires attestation (ST-013) |
| ST-006 | partial |  |
| ST-007 | implemented |  |
| ST-008 | partial |  |
| ST-009 | partial |  |
| ST-010 | missing | no drift/constraint evaluation |
| ST-011 | partial |  |
| ST-012 | missing | no summary representation |
| ST-013 | missing | `user_override` / attestation hard-disabled |

### CAP

| Req | Status | Note |
|---|---|---|
| CAP-001 | partial |  |
| CAP-002 | partial |  |
| CAP-003 | partial |  |
| CAP-004 | implemented |  |
| CAP-005 | implemented |  |
| CAP-006 | implemented |  |
| CAP-007 | implemented | by-absence: satisfied only by absence of network code (no positive test) |
| CAP-008 | partial |  |
| CAP-009 | partial |  |

### RST

| Req | Status | Note |
|---|---|---|
| RST-001 | implemented | packet carries goal, decisions, next steps, open questions, ownership, provenance |
| RST-002 | implemented | SessionStart hook injects size-capped, secret-scanned context |
| RST-003 | implemented | successor ack/verify events + mutation gate |
| RST-004 | implemented |  |
| RST-005 | missing | no cross-agent restore |
| RST-006 | missing | no Git sync; manual export/import only |
| RST-007 | implemented | git bundle + diffs + untracked bytes, restorable via apply-code |
| RST-008 | implemented | plan-first restore, forced override recorded |
| RST-009 | implemented | plan blockers/actions + export/import warnings |
| RST-010 | implemented | packet labels provenance on goal/decisions/next steps/questions |
| RST-011 | implemented |  |

### AD

| Req | Status | Note |
|---|---|---|
| AD-001 | implemented | real CLI version detection |
| AD-002 | partial | claude hooks + MCP server installed; CLAUDE.md rules block still missing |
| AD-003 | partial |  |
| AD-004 | partial |  |
| AD-005 | implemented | `anchor wrap` fallback (session events, context file, signal/exit propagation) |
| AD-006 | implemented | claude-code hook install/uninstall (plan-first, backed up) |
| AD-007 | partial |  |
| AD-008 | partial |  |
| AD-009 | partial | absolute quoted exe + timeout; hook command remains a shell string |
| AD-010 | implemented | read-only MCP stdio server + `.mcp.json` registration |
| AD-011 | missing |  |
| AD-012 | missing |  |
| AD-013 | partial |  |

### CM

| Req | Status | Note |
|---|---|---|
| CM-001 | partial | prohibition satisfied; no remote-MCP client |
| CM-002 | missing |  |
| CM-003 | missing |  |
| CM-004 | missing |  |
| CM-005 | missing |  |
| CM-006 | missing |  |
| CM-007 | missing |  |
| CM-008 | missing |  |
| CM-009 | missing |  |
| CM-010 | missing |  |
| CM-011 | missing |  |
| CM-012 | missing |  |
| CM-013 | missing |  |

### SY

| Req | Status | Note |
|---|---|---|
| SY-001 | missing |  |
| SY-002 | missing |  |
| SY-003 | partial |  |
| SY-004 | partial |  |
| SY-005 | implemented | code bytes hashed and restorable |
| SY-006 | missing |  |
| SY-007 | missing |  |
| SY-008 | partial |  |
| SY-009 | partial |  |
| SY-010 | partial |  |
| SY-011 | partial |  |
| SY-012 | implemented | `complete` is now reachable when state and selected bytes verify |

### CFG

| Req | Status | Note |
|---|---|---|
| CFG-001 | implemented | config is source of truth for rules/MCP/skill/hooks |
| CFG-002 | implemented | managed blocks; user content preserved; idempotent |
| CFG-003 | implemented | plan/diff/status drift detection |
| CFG-004 | implemented | secret refs only, literal values rejected |
| CFG-005 | implemented | project overrides user scope, tested |
| CFG-006 | implemented | snapshots + rollback |
| CFG-007 | implemented | rules instruct start/branch/completion |
| CFG-008 | implemented | Claude skill file; Codex/Gemini playbook via rules file |

### VT

| Req | Status | Note |
|---|---|---|
| VT-001 | missing |  |
| VT-002 | missing |  |
| VT-003 | missing |  |
| VT-004 | partial |  |
| VT-005 | missing |  |
| VT-006 | missing |  |
| VT-007 | missing |  |
| VT-008 | missing |  |
| VT-009 | partial |  |
| VT-010 | partial |  |

### UI

| Req | Status | Note |
|---|---|---|
| UI-001 | partial | tree/state/skill verbs; no branch/ownership verbs/web UI |
| UI-002 | partial |  |
| UI-003 | partial |  |
| UI-004 | partial | read-only loopback tree web view (`anchor ui`) |
| UI-005 | partial | loopback-only binding; no per-session token |
| UI-006 | missing | no Origin/CSRF checks (GET-only read-only server) |
| UI-007 | implemented | GET/HEAD-only server, 405 on mutations |

### CONC

| Req | Status | Note |
|---|---|---|
| CONC-001 | partial |  |
| CONC-002 | partial |  |
| CONC-003 | implemented |  |
| CONC-004 | partial |  |
| CONC-005 | implemented |  |

### EVID

| Req | Status | Note |
|---|---|---|
| EVID-001 | partial |  |
| EVID-002 | partial |  |
| EVID-003 | implemented | handoff_verified event with code head + evidence ids |
| EVID-004 | missing |  |
| EVID-005 | partial |  |
| EVID-006 | partial |  |

### RT

| Req | Status | Note |
|---|---|---|
| RT-001 | out-of-scope | docs defer Roost runtime scope |
| RT-002 | implemented | by-absence: satisfied only by absence of dependencies (no CI guard) |
| RT-003 | partial |  |

### SEC

| Req | Status | Note |
|---|---|---|
| SEC-001 | partial |  |
| SEC-002 | partial |  |
| SEC-003 | partial |  |
| SEC-004 | partial |  |
| SEC-005 | missing | no network code |
| SEC-006 | missing | no sync code |
| SEC-007 | missing | no web server |

### NFR

| Req | Status | Note |
|---|---|---|
| NFR-001 | partial |  |
| NFR-002 | missing | no latency benchmarks |
| NFR-003 | implemented | by-absence: satisfied only by absence of network code (no offline test) |
| NFR-004 | partial |  |
| NFR-005 | missing | no retention/delete commands |
| NFR-006 | implemented |  |
| NFR-007 | missing | no UI |

### OPS

| Req | Status | Note |
|---|---|---|
| OPS-001 | missing | no packaging |
| OPS-002 | missing | no migrate/rollback |
| OPS-003 | missing | no uninstall |
| OPS-004 | missing | no CI |
| OPS-005 | partial |  |
| OPS-006 | partial |  |
| OPS-007 | implemented | by-absence: satisfied only by absence of payments/network code |
| OPS-008 | partial |  |

## 5b. Test coverage

Measured with `go test ./... -cover`. `cmd/anchor` reports 0% because its tests
drive the compiled binary out of process; its flows are covered by 60+
end-to-end CLI tests instead (exit codes, JSON contracts, attach mode, error
matrices, full successor flow). The recovery package has a deliberately slow
sparse-file limit test (~15 s); `-race` takes ~5 min for the whole suite.

| Package | Before this pass | Now | Notes |
|---|---:|---:|---|
| `internal/pathguard` | 0.0% | 91.7% | Uncovered lines are unreachable defensive branches (root loop, non-ENOENT ancestor error). |
| `internal/core` | 69.4% | 92.9% | Remaining gaps are OS-error paths (rename/sync failures) and the test-only tx hook shim. |
| `internal/adapter` | 75.8% | 82.6% | Remaining gaps are `json.Marshal`/`Write` error branches. |
| `internal/handoff` | 78.1% | 93.3% | Goal ranking, packet blocks, stale/detached eval covered. |
| `internal/recovery` | 51.9% | 85.4% | Export/import branches, staging atomicity, limits, legacy packages covered. |
| `internal/mcp` | new | 83.5% | Protocol/JSON-RPC edge cases and all four read-only tools. |
| `bench/ab` | new | 68.8% | Mock-mode harness runs baseline+anchor and asserts the report contract. |
| `cmd/anchor` | binary-driven | binary-driven | Error matrices, JSON contracts, full successor flow. |

Test suite: 451 top-level test functions / 1020 run entries, ~20,800 test lines.
`go test -race ./...`, `go vet ./...` and `gofmt -l` are clean; the suite is
deterministic and uses only `t.TempDir`/`t.Setenv` (never the real HOME).

## 6. Accuracy caveats

- **`by-absence` rows** (CAP-007, NFR-003, RT-002, OPS-007, plus NFR-004 partly) are
  satisfied only because the feature does not exist. They must not be counted as
  positive delivery evidence in a release review.
- **`evidence --file`** intentionally accepts absolute paths outside the workspace, so
  SEC-001 has no single unified path validator. `docs/04` should say so explicitly.
- **`handoff import`** reports `complete` only after replaying the rebuilt
  workspace and verifying evidence blobs, and only when the package carries
  complete code bytes (a dirty tree needs `--include-code` plus
  `--include-bytes`). Import never applies code: `handoff apply-code --apply` is
  the explicit code step.
- **Legacy artifact-only packages** (no workspace payload) still import their
  verified bytes but never report a rebuilt workspace.
- **Adapter hooks stay contract evidence.** `adapter detect` runs the real CLIs,
  but the installed Claude hook has only been exercised by feeding hook JSON to
  the installed command (contract test), not inside a logged-in Claude Code
  session, so the capability grade stays `D` and `end_to_end_verified` stays
  false. Installation manages only Claude Code in this build, rewrites the
  settings file (formatting changes, unknown keys preserved) and backs it up
  first; the user scope requires an explicit `--scope user`.
- **`anchor wrap` is a fallback, not a hook.** It records session start/end
  events (adapter `wrap`, source `hook_unverified`) plus a context file and
  `ANCHOR_*` env vars; it does not capture tool calls (hooks do), and it
  forwards signals/exit codes to the child. Its evidence is deliberately
  labelled unverified.
- **MCP tools are read-only.** `anchor mcp serve` proxies read-only inspection
  (status/tree/evidence verify/restore plan) and never mutates the workspace or
  records ack/verify. Registration happens only with
  `adapter install --with-mcp` (project scope, `.mcp.json`, merged/backed up/
  uninstallable). Protocol and CLI contract tests pass, but no live agent has
  called the server from inside a real session in this environment.
- **The A/B benchmark has small-N real numbers, not a leaderboard.** A 2026-11-16
  run with Claude Code 2.1.252 + DeepSeek `deepseek-v4-flash` (3 trials/arm,
  600 s/phase) measured resume completion 0/3 (baseline) vs 3/3 (anchor), with
  anchor using ~33% more total tokens (+$0.087 mean cost per trial) because it
  actually finishes phase 2; the baseline still spent ~184k tokens on each
  failed resume. Mock-mode pass rates remain harness self-tests. Cite the model,
  N and timeout, and treat one custom task with N=3 as directional evidence only;
  `bench/README.md` §3 lists public benchmarks (SWE-bench Verified,
  Terminal-Bench, Aider polyglot, τ-bench pass^k, HAL, Inspect Evals) to borrow
  tasks and metrics from.
- **Secret scanning covers canonical state too.** Export scans event batches, so
  a note or title containing a credential-shaped string (for example
  `password=...`) blocks export. This is fail-closed on purpose (SEC-003) and the
  diagnostic names the rejected batch, but events are immutable and there is no
  redaction tool yet: the only remedies today are rebuilding the workspace or
  deleting the offending batch.
- **Handoff progress cannot be forged through the raw event surface.** Only
  `handoff restore`/`handoff prepare` record prepared/acked/verified events; raw
  `event append` rejects them. The packet file may be deleted, but the gate stays
  active and both `anchor status --json` and `handoff status` report
  `packet_missing`, prompting a fresh preparation; a packet from another
  workspace is refused.
- **`handoff prepare` (and any export that creates a packet) freezes editing
  until the handoff is verified.** A successor whose code state exactly matches
  the packet can acknowledge it without `--force`; if the code differs,
  `--force` is required and recorded. Verification requires HEAD *and* the
  working tree (staged/unstaged diffs) to match the packet.
- **Imported verification is inherited by design.** Handoff ack/verify are
  workspace events, so an imported workspace whose handoff was verified on the
  source device is not asked to verify it again (`TestCLIImportInheritsVerification`
  pins this). Devices that must re-verify should prepare and export a fresh
  handoff instead of reusing a verified one.
- **The successor gate fails closed.** If the projection cannot be replayed (for
  example a referenced evidence blob disappeared), the gate reports the handoff
  as pending instead of assuming progress; the workspace then refuses writes
  until the projection is repaired.
- **Code transport is explicit and capped.** `--include-code` bundles the
  prepared commit; the bundle must fit the 64 MiB package file limit and is not
  signed, so verify provenance out of band. `apply-code --apply` is the only
  command that writes code: it stashes dirty changes (when forced), creates a
  `refs/anchor/backup/*` ref, then checks out and applies the packaged bytes.
- **Package limits are symmetric.** Export refuses a workspace that would exceed
  the import limits (1000 files / 256 MiB), and the git bundle is excluded from
  the *text* secret scanner on both sides, so an export can always be imported.
  Committed history inside a bundle is therefore not text-scanned.
- **Import is atomic.** A failed import removes its staging tree and never leaves
  a half-built workspace; the target parent directory must already exist.
- **`acceptance/matrix.yaml`** still lists all 140 requirements as `planned` with
  test IDs (`UT-*`, `IT-*`, `SEC-*`, `E2E-*`) that do not exist in the repo. The
  matrix is a design artifact, not evidence. `docs/05` §5 promises an
  `internal/acceptance/` coverage test; it does not exist.

## 7. Keeping this file current

Update this file when a commit closes any row above. Keep the rubric honest:
`implemented` requires both code and an asserting test. When the matrix is next
regenerated, its `status` column should mirror this file instead of `planned`.

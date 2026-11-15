# Implementation Status (audited)

**Audit date:** 2026-11-16
**Commit:** audited at `1a6acc5`; since then WS-001 (`init --attach`), the packet
content work (ST-001/002/005, RST-001/010) and workspace-carrying export/import
(WS-007) landed. 36 Go files / ~9,900 LOC / 102 Go test functions.
The authoritative per-requirement status is the ledger in §5.
**Method:** read-only code audit. `go build ./...` OK; `go test ./... -count=1` and
`go test -race ./...` pass for `cmd/anchor`, `internal/{core,adapter,handoff,recovery}`
(`internal/pathguard` has no test files). Cross-compilation verified for
darwin/linux/windows on arm64 and amd64.

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
| RST | 4 | 4 | 3 | 0 | 11 |
| AD | 0 | 10 | 3 | 0 | 13 |
| CM | 0 | 1 | 12 | 0 | 13 |
| SY | 1 | 7 | 4 | 0 | 12 |
| CFG | 0 | 0 | 8 | 0 | 8 |
| VT | 0 | 3 | 7 | 0 | 10 |
| UI | 0 | 3 | 4 | 0 | 7 |
| CONC | 2 | 3 | 0 | 0 | 5 |
| EVID | 0 | 5 | 1 | 0 | 6 |
| RT | 1 | 1 | 0 | 1 | 3 |
| SEC | 0 | 4 | 3 | 0 | 7 |
| NFR | 2 | 2 | 3 | 0 | 7 |
| OPS | 1 | 3 | 4 | 0 | 8 |
| **Total** | **24** | **59** | **56** | **1** | **140** |

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
   non-decisions, unknown provenance, out-of-range confidence, and any
   `user_confirmed` claim without a local attestation (`internal/core`).
8. **Restorable packages** — export always transports the workspace manifest and
   event log; import rebuilds the workspace in a fresh empty target, replays it,
   verifies evidence blobs and reports an honest `complete`/`incomplete` status.
   Legacy artifact-only packages still import their bytes but are reported as not
   rebuilding a workspace.
9. **No `unsafe`, no third-party dependencies, no network or telemetry code.**

## 3. Requirement-by-requirement status

This section keeps the evidence notes gathered during the audit. Where a row
groups several requirements or was superseded by later work, the ledger in §5 is
authoritative.

### 3.1 WS — Workspace / event store

| Req | Status | Evidence | Gap |
|---|---|---|---|
| WS-001 | implemented | `internal/core/store.go:81` (in-place) + `StateDir`/`InitAttached` (attach); `TestCLIInitAttachInExistingProject`, `TestInitAttachedLeavesProjectFilesUntouched`, `TestStateDirResolvesAttachedWorkspace` | `init --attach` adopts an existing project with state in `<project>/.anchor`, ignores it in the project `.gitignore`, and fails closed on re-attach or half-initialized state. Remaining niceties: no interactive prompt, no multi-workspace-per-project. |
| WS-002 | partial | `internal/core/transaction.go:231`, `replay.go:42`; `TestDeterministicReplayIndependentOfSliceOrder` | No `TREE.md`/`state.json` projections; event batches are random-named one-line JSON, so diffs are coarse. No Git-merge test. |
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
| ST-005 | implemented | `core.Provenance`, `Node.Provenance/Confidence`, replay validation; `TestProvenanceAndConfidenceAreValidated` | Values are recorded and enforced; `user_confirmed` still needs an attestation mechanism (ST-013), so that path is currently unreachable. |
| ST-006 | partial | `internal/core/event.go:46,56,81` | `ActorKind` never set by any command; nodes store no actor/agent/session/time; no agent version field. |
| ST-007 | implemented | `internal/core/event.go:40`, `transaction.go:83,46`; `TestCLIConcurrentSameNodeOneWins` | CLI node mutations generate no idempotency key (only adapter ingest does). |
| ST-008 | partial | `internal/core/replay.go:548,609`; `TestP1TombstoneIsTerminal` | Edit-vs-delete relies on the generic same-base-revision rule; no dedicated conflict class or test. |
| ST-009 | partial | `internal/core/replay.go:516`, `tree.go:102`; `TestP1NonGoalRequiresParent` | No reparent, so cycle prevention is vacuous; no branch lineage/merge validation. |
| ST-010 | missing | grep `drift|constraint` → 0 | No drift checks, no advisory/block distinction. |
| ST-011 | partial | `cmd/anchor/main.go:529,542,554` | No merge, no reparent; "agent cannot self-confirm" is vacuous without provenance. |
| ST-012 | missing | no summary event type (`event.go:15`) | No summary storage or source-pointer rule. |
| ST-013 | missing | `ActorAttestation` never read (`event.go:57`); `user_override` rejected (`replay.go:578`, `main.go:315`) | No attestation mechanism; requirement dodged by disabling the feature. |

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
| RST-002 | missing | render emits unverified fragments (`internal/adapter/render.go:69`) | No injection path, no size/token caps. |
| RST-003 | partial | `handoff.go:323`; `TestEvaluateDetectsStaleCodeWithoutOverwriting` | No `handoff_acked`/`handoff_verified` events; no gate before editing. |
| RST-004 | implemented | `handoff.go:248`, `main.go:484`; `TestCLIWorkflowRecoveryAndRestoreBoundary` | Partially vacuous: no summary object exists. |
| RST-005 | missing | one static packet format only | No per-adapter cross-agent handoff. |
| RST-006 | missing | export carries no events (`recovery.go:163`) | No cross-device handoff. |
| RST-007 | partial | `handoff.go:34,116`; `TestDirtyRepoIsIncompleteAndSecretNotInPacket` | No bundle/patch artifact, no restore instructions, no base commit. |
| RST-008 | partial | restore always evaluates, `apply: disabled` (`main.go:644`) | No apply path, so "explicit confirmation" is unimplemented. |
| RST-009 | partial | `handoff.go:358`, `recovery.go:203` | Does not enumerate missing artifacts/unreachable evidence; returns a status only. |
| RST-010 | implemented | `Node.Provenance` + packet blocks; `TestPrepareCarriesTaskStructure` | Provenance is labeled per goal/decision/next step/question and summarized; `user_confirmed` requires attestation. |
| RST-011 | implemented | `handoff.go:103`, `recovery.go:433`; `TestDirtyRepoIsIncompleteAndSecretNotInPacket` | Code apply disabled, so "unrecovered" is always true; no per-artifact list. |

### 3.5 AD — Adapters

| Req | Status | Evidence | Gap |
|---|---|---|---|
| AD-001 | partial | `internal/adapter/contract.go:27` | No CLI/version detection or probe; capability list hard-coded. |
| AD-002 | partial | `render.go:38`; `TestRenderGolden` | Only a `PostToolUse` fragment: no MCP registration, CLAUDE.md/rules, install/uninstall. |
| AD-003 | partial | `render.go:59`; `TestRenderGolden` | Fragment only: no AGENTS.md, MCP, install, trust handling. |
| AD-004 | partial | `render.go:50`; `TestRenderGolden` | Fragment only: no GEMINI.md, extensions, MCP, install. |
| AD-005 | partial | `cmd/anchor/adapter.go:124` | No `anchor wrap`; no restore path. |
| AD-006 | partial | `render.go:97`; `TestWriteRefusesSharedConfigAndOverwrite` | No install/backup/managed merge; a second render errors instead of being idempotent. |
| AD-007 | partial | `contract.go:13`, `render.go:35` | Descriptor has no version field; per-ID switch is a cross-agent assumption. |
| AD-008 | partial | `contract.go:30`; `TestCLIAdapterDryRunCaptureAndNoSharedInstall` | Fixtures carry no `source`/retrieval-date field; no UI labels. |
| AD-009 | partial | `render.go:31` (shell string, bare `anchor`) | No argv array, absolute exe, env allowlist, timeout, or process-group handling. |
| AD-010 | missing | `mcp` appears only in `not_claimed` strings | No MCP server, no registration. |
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
| SY-005 | partial | `handoff.go:178`, `recovery.go:117` | Bytes never restored to a repo; no bundle. |
| SY-006 | missing | no sync code | No opt-in, credential handling, or secret-free remotes. |
| SY-007 | missing | no queue | Offline queue absent. |
| SY-008 | partial | `store.go:133`, `replay.go:46`; `TestDeviceSeqMonotonic` | No last-sync state; no clock-skew handling. |
| SY-009 | partial | `docs/06` §3 O-001 | Documented default only; no config key or code boundary to choose topology. |
| SY-010 | partial | `replay.go:156`, `main.go:595`; `TestP0ConcurrentTakeoversConflict` | No lease expiry, no stale-owner detection, no audit query. |
| SY-011 | partial | `event.go:127`, `handoff.go:266`, `recovery.go:287` | No remote verification path; no signatures. |
| SY-012 | implemented | `recovery.go:194,433`; `TestCLIHandoffExportImportDryRunAndBytes` | Over-conservative: `complete` is unreachable even when all bytes verify. |

### 3.8 CFG — Config / rules / skills

| Req | Status | Gap |
|---|---|---|
| CFG-001 – CFG-008 | missing | No config package/command; no managed blocks, plan/diff, secret refs, scopes, rollback, rules rendering, or skills install. `adapter render` writes a fragment + manifest only (`render.go:88`). |

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
| UI-001 | partial | `cmd/anchor/main.go:31,60`; `TestCLIWorkflowRecoveryAndRestoreBoundary` | ~21 verbs; missing attach, tree, branch, claim/release/heartbeat, top-level capture/restore, sync, config, vault, memory, web, install, uninstall, migrate, version, wrap, ack. |
| UI-002 | partial | `--json` on 5 command families | No JSON for init/node/evidence add/handoff prepare; exit codes only 0/1/2. |
| UI-003 | partial | plain `fmt.Printf` (`main.go:483`) | No TTY detection, color, or structured human layout. |
| UI-004/005/006/007 | missing | no `http`/web code | No web UI, loopback auth, CSRF/Origin checks, or read-only mode. |

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
| EVID-003 | partial | `handoff.go:323` | No `handoff_verified` event; no gate before editing. |
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

1. **Make restore actually restore** (RST-003/008, EVID-003). Today `handoff restore`
   only evaluates and prints `apply: disabled`. Add `--plan` (commit match, dirty
   status, files to overwrite), `--apply` requiring a clean tree or `--force` +
   backup ref, and `handoff_acked`/`handoff_verified` events plus a successor gate.
2. ~~**Carry the workspace inside the export package** (WS-007).~~ **Done** — the
   package always carries the workspace manifest and event log; import rebuilds,
   replays and verifies. Remaining: config/rules snapshot and bundled commit bytes.
3. ~~**Give the packet the task structure a successor needs** (RST-001/010,
   ST-001/002/005).~~ **Done** — goal constraints, decision trade-offs, provenance,
   next steps, open questions and ownership are in the model and the packet.
4. **Context injection + managed rules files** (RST-002, AD-002/003/004/006,
   CFG-002/007). Managed-block renderer for `CLAUDE.md`/`AGENTS.md`/`GEMINI.md` with
   dry-run/diff, backup, idempotent re-apply, and size-capped SessionStart injection.
5. **Provenance + local user attestation** (ST-005/013, RST-010, EVID-002). Provenance
   enum + confidence on nodes/events; local attestation token validated in replay; an
   `override_recorded` event with reason.
6. **Wire real capture** (CAP-001, AD-001/005/009). Detect installed CLI/version,
   install/merge a real Claude hook (backup + trust instructions), surface capture
   failures in `doctor`, add `anchor wrap` fallback.
7. ~~**Fix adoption: `init --attach`** (WS-001).~~ **Done** — state lives in
   `<project>/.anchor`, user files are untouched, and every command resolves the
   attached state root from the project directory.
8. **Human-readable projections + `tree` view** (WS-002, UI-001). Deterministic
   `TREE.md` + `state.json`, `anchor tree [--json]`, and a `branch` command.
9. **Expose Anchor tools over MCP** (AD-010) before the memory bridge. Minimal stdio
   server: `anchor_status`, `anchor_tree`, `anchor_verify_evidence`.
10. **Make dirty code recoverable** (RST-007/008, SY-005). Emit a git bundle/patch
    artifact plus a restore plan (`git bundle verify`, backup ref, `--force` on dirty).
11. **Complete the ownership lifecycle** (CONC-001/002). Lease expiry in replay,
    claim/release/heartbeat commands, stale-owner takeover only after expiry,
    ownership audit query.
12. **Make it shippable** (WS-003/008, OPS-001..004, NFR-001). `migrate` with
    dry-run/backup/rollback, `uninstall --purge` confirmation, CI (gofmt/vet/race on
    3 OSes), checksummed release builds.

## 5. Status ledger (authoritative)

Every requirement in `acceptance/matrix.yaml` appears exactly once.

### WS

| Req | Status | Note |
|---|---|---|
| WS-001 | implemented | `init --attach` adopts an existing project; state in `<project>/.anchor` |
| WS-002 | partial |  |
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
| RST-002 | missing | no context injection path |
| RST-003 | partial |  |
| RST-004 | implemented |  |
| RST-005 | missing | no cross-agent restore |
| RST-006 | missing | no Git sync; manual export/import only |
| RST-007 | partial |  |
| RST-008 | partial |  |
| RST-009 | partial |  |
| RST-010 | implemented | packet labels provenance on goal/decisions/next steps/questions |
| RST-011 | implemented |  |

### AD

| Req | Status | Note |
|---|---|---|
| AD-001 | partial |  |
| AD-002 | partial |  |
| AD-003 | partial |  |
| AD-004 | partial |  |
| AD-005 | partial |  |
| AD-006 | partial |  |
| AD-007 | partial |  |
| AD-008 | partial |  |
| AD-009 | partial |  |
| AD-010 | missing | no MCP server |
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
| SY-005 | partial |  |
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
| CFG-001 | missing |  |
| CFG-002 | missing |  |
| CFG-003 | missing |  |
| CFG-004 | missing |  |
| CFG-005 | missing |  |
| CFG-006 | missing |  |
| CFG-007 | missing |  |
| CFG-008 | missing |  |

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
| UI-001 | partial |  |
| UI-002 | partial |  |
| UI-003 | partial |  |
| UI-004 | missing |  |
| UI-005 | missing |  |
| UI-006 | missing |  |
| UI-007 | missing |  |

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
| EVID-003 | partial |  |
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

## 6. Accuracy caveats

- **`by-absence` rows** (CAP-007, NFR-003, RT-002, OPS-007, plus NFR-004 partly) are
  satisfied only because the feature does not exist. They must not be counted as
  positive delivery evidence in a release review.
- **`evidence --file`** intentionally accepts absolute paths outside the workspace, so
  SEC-001 has no single unified path validator. `docs/04` should say so explicitly.
- **`handoff import`** now reports `complete` only after replaying the rebuilt
  workspace, verifying evidence blobs and confirming the exported code state was
  clean. Code is never applied: imported untracked bytes are staged under
  `local/imported/`, and the successor must check out the recorded commit.
- **Legacy artifact-only packages** (no workspace payload) still import their
  verified bytes but never report a rebuilt workspace.
- **Secret scanning covers canonical state too.** Export scans event batches, so
  a note or title containing a credential-shaped string (for example
  `password=...`) blocks export until it is redacted. This is fail-closed on
  purpose (SEC-003), and the diagnostic names the rejected batch.
- **Package limits are symmetric.** Export refuses a workspace that would exceed
  the import limits (1000 files / 256 MiB), so an export can never produce a
  package that import rejects.
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

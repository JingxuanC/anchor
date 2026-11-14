# Requirement → Test Acceptance Mapping

**Status:** planned acceptance baseline. The implementation will encode this in
`acceptance/matrix.yaml`; CI will fail if a baseline requirement has no mapped
test or an explicit, approved limitation.

## 1. Test layers and labels

| Layer | What it proves | Environment |
|---|---|---|
| `unit` | Pure logic: replay, invariants, validation, redaction, renderers. | Go test / Vitest, no external services. |
| `integration` | Filesystem, event store, real Git, config files, local HTTP, mock MCP server. | Temp workspace, local bare Git, wiremock-like MCP mock. |
| `contract` | Adapter hook/config shapes against official documented schemas and recorded golden files. | No agent required; fixture `source=recorded`. |
| `e2e` | Real agent CLI + hook + MCP + restore round-trip. | Real installed agent in a temp project; `source=real`. |
| `security` | Path/command/secret/injection/CSRF/Git/ownership attacks. | Adversarial fixtures and temp workspaces. |
| `perf` | Hook latency, packet size, UI load. | Benchmark harness; budgets reported, not hidden. |
| `packaging` | Install, upgrade, migrate, uninstall, checksums, signatures. | CI matrix; container/VM where possible. |
| `docs` | Requirement coverage, known limitations, examples valid. | Repo checks. |

Labels are mandatory in test metadata:

- `real`: exercised the actual component (agent CLI, Git, OS keychain, remote
  service).
- `recorded`: fixture captured from official docs/real output but not live.
- `fake`: mock used for deterministic tests; never reported as a live adapter.

A mock adapter may not satisfy a real-adapter acceptance row. Codex/Gemini e2e
rows are `SKIP` with a reason until the CLIs are installed and authenticated.

## 2. CI gates

1. `go fmt`, `go vet`, `staticcheck` (or equivalent), `npm ci --ignore-scripts`,
   `npm run lint`, `npm run typecheck`.
2. Unit + integration + contract + security tests; race detector for Go.
3. Real Claude Code e2e in a temp workspace (installed in CI image or gated).
4. Web e2e with a local server (Playwright) and accessibility smoke.
5. Packaging smoke: build all target OS/arch, install/uninstall in a container.
6. `acceptance/matrix.yaml` coverage check: every `REQ-*` has ≥1 test ID and
   an acceptance criterion.
7. No-real-secret scan and dependency/SBOM checks.
8. Known-limitations document must exist and be non-empty; unverified features
   must be labeled.

## 3. Mapping

Rows group adjacent requirements only when they share a test suite; the listed
IDs are all covered, not a sample.

### 3.1 Workspace core

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-WS-001..004 | `UT-WS-001..004`, `IT-WS-001`, `SEC-WS-001` | Init/attach works in a temp project; manifest round-trips; migration dry-run/rollback; atomic writes survive simulated crash; lock excludes a second writer. | real |
| REQ-WS-005..008 | `UT-WS-005..008`, `IT-WS-002`, `PKG-WS-001` | Synced files contain no device paths; projections rebuild identically; export/import round-trip; uninstall preserves data and `--purge` requires confirmation. | real |

### 3.2 Session Tree

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-ST-001..004 | `UT-ST-001..004`, `IT-ST-001`, `E2E-ST-001` | Goal/constraints/decision/task/branch creation and transitions pass; branch fork/merge/abandon preserved; e2e tree reflects a real Claude session. | real for Claude; contract elsewhere |
| REQ-ST-005..008 | `UT-ST-005..008`, `SEC-ST-001`, `IT-ST-002` | Provenance cannot be forged as user-confirmed; append-only replay; tombstone and edit-vs-delete conflict surfaced, not resurrected. | real |
| REQ-ST-009..012 | `UT-ST-009..012`, `SEC-INJ-001`, `E2E-ST-002` | Cycle/parent/status invariants enforced; drift warning + policy block behavior; successor sees provenance; summaries never become canonical history. | real |

### 3.3 Capture / Collector

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-CAP-001..005 | `CT-AD-CLAUDE-CAP`, `CT-AD-CODEX-CAP`, `CT-AD-GEMINI-CAP`, `IT-CAP-001`, `E2E-CAP-CLAUDE` | Documented hook payloads normalize to the event schema; real Claude hooks produce idempotent events; Codex/Gemini contract fixtures parse. | Claude real; others recorded |
| REQ-CAP-006..009 | `UT-CAP-006..009`, `CHAOS-CAP-001`, `SEC-CAP-001` | Replay dedupes; capture works offline; failures visible in doctor; redaction/size caps drop secrets and oversized payloads. | fake/real mix; clearly labeled |

### 3.4 Restore / handoff

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-RST-001..004 | `UT-RST-001..004`, `E2E-RST-CLAUDE`, `CT-RST-001` | Packet includes goal/constraints/active node/branches/decisions/evidence/code state/warnings; injection respects 10k-char cap; acknowledgment/verification event required; summary-only is rejected as full recovery. | real for Claude; contract for others |
| REQ-RST-005..010 | `E2E-RST-MATRIX`, `SEC-RST-001..003`, `IT-RST-002` | Cross-agent matrix passes or is explicitly skipped; dirty-tree restore is dry-run/backup/force-confirmed; missing artifacts reported; provenance labels preserved. | real where CLIs exist; otherwise skip with reason |

### 3.5 Adapters

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-AD-001..004 | `CT-AD-*`, `IT-AD-DETECT`, `E2E-AD-CLAUDE` | Capability descriptors match installed versions; install/render for all three; real Claude e2e passes. | real Claude; recorded others |
| REQ-AD-005..008 | `IT-AD-FALLBACK`, `IT-AD-INSTALL`, `UT-AD-008` | Missing hooks use wrapper/manual path; install is idempotent/backed up; adapters isolated; labels real/recorded/fake. | real/fake mix labeled |
| REQ-AD-009..012 | `SEC-AD-001..002`, `IT-AD-MCP-001`, `UT-AD-011..012` | Hook argv/no-shell/no-secret tests pass; MCP registration works for each config format; context caps enforced; drift detected. | real config files |

### 3.6 Causal-memory bridge

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-CM-001..004 | `UT-CM-001`, `IT-CM-MOCK-001`, `SEC-CM-001` | No local DB/causal file; MCP mock contract passes; search results injected with provenance; completion creates `proposed`, never `confirmed`. | fake MCP in CI; live opt-in |
| REQ-CM-005..008 | `UT-CM-005..008`, `IT-CM-RETRY`, `CHAOS-CM-001` | Evidence/confirmation gates; stable idempotency key; bounded retry/jitter; offline queue/circuit breaker/stale cache degrade visibly. | fake MCP; live opt-in |
| REQ-CM-009..012 | `SEC-CM-002`, `UT-CM-010..012`, `IT-CM-DISCOVERY` | Injected instructions ignored; schema/size validation; tools discovered, not hard-coded; no memory DB in Git; queue retention/delete works. | fake MCP |

### 3.7 Sync

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-SY-001..004 | `IT-SY-001..004` (real local bare Git), `SEC-SY-001` | Event union merge; no force-push; structural/delete conflicts surfaced; tombstone survives; no `causal.db`. | real Git |
| REQ-SY-005..008 | `IT-SY-005..008`, `SEC-SY-002..003` | Bundle/patch artifact restore with hash; dirty-tree guard; credential redaction; offline queue; device metadata. | real Git; fake remote where needed |
| REQ-SY-009..011 | `OQ-SY-009` (decision gate), `IT-SY-010`, `UT-SY-011` | Topology decision recorded before implementation; ownership not merged; checksum/signature verification. | real Git |

### 3.8 Config / rules / skills

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-CFG-001..004 | `UT-CFG-001..004`, `IT-CFG-001`, `SEC-CFG-001` | One source renders all adapters; managed blocks preserve user content; drift detected; no secret values rendered. | real files |
| REQ-CFG-005..007 | `IT-CFG-002`, `UT-CFG-005..007`, `E2E-CFG-001` | Scope/precedence documented and tested; rollback works; rendered rules trigger Anchor tools in a real Claude session. | real Claude; contract others |

### 3.9 Vault / secrets

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-VT-001..004 | `UT-VT-001..004`, `IT-VT-KEYCHAIN`, `SEC-VT-001` | Keychain backends selected correctly; refs only in workspace; injection never in context/log; redaction catches fake secrets. | real keychain where available; fake labeled |
| REQ-VT-005..010 | `UT-VT-005..010`, `SEC-VT-002..003`, `SCAN-VT-001` | Rotation/revocation; per-scope access; no secret in argv; audit has no value; secret scan catches fixtures; no real keys. | real/fake mix labeled |

### 3.10 CLI / web UI

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-UI-001..003 | `IT-CLI-001..003`, `UT-CLI-JSON`, `CT-CLI-EXIT` | Commands exist and are scriptable; JSON/exit codes stable; human output actionable. | real |
| REQ-UI-004..007 | `E2E-UI-001..004`, `SEC-UI-001..003`, `A11Y-UI-001` | Tree/graph/evidence/ownership/sync views render; loopback/CSRF/Origin tests pass; no secrets in DOM; read-only mode works; keyboard smoke. | real Playwright |

### 3.11 Concurrency / evidence

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-CONC-001..004 | `IT-CONC-001..004`, `SEC-CONC-001` | Two-process lease race has one winner; fencing rejects stale writes; revision conflict returns conflict; ownership visible. | real processes |
| REQ-EVID-001..006 | `UT-EVID-001..006`, `E2E-EVID-CLAUDE`, `SEC-EVID-001` | Evidence schema/hash/verification; done gate; successor verification event; causal link gate; immutability; summary is not evidence. | real Claude |

### 3.12 Roost boundary / NFR / operations

| Requirements | Tests (planned) | Acceptance criterion | Label |
|---|---|---|---|
| REQ-RT-001..003 | `UT-RT-001..003`, `CI-IMPORT-BOUNDARY` | Interface compiles; no imports/network to Roost repos; core works with feature disabled. | fake/no network |
| REQ-NFR-001..007 | `PKG-NFR-001`, `PERF-NFR-002`, `IT-NFR-003`, `SCAN-NFR-004..005`, `STATIC-NFR-006`, `A11Y-UI-001` | Platform builds; budgets reported; offline core; no telemetry; retention; static analysis; accessibility. | real/fake mix labeled |
| REQ-OPS-001..008 | `PKG-OPS-001..003`, `CI-OPS-004`, `DOC-OPS-005`, `SBOM-OPS-006`, `SCAN-OPS-007`, `UT-RT-001` | Checksums/signatures; migrate/rollback/uninstall; CI gates; docs; SBOM; no payments/keys; Roost boundary. | real packaging where possible |


## 4.1 Design-review addendum (P0/P1 mapping)

These rows respond to the architect review. They are planned tests, not
implemented tests.

| Review item | Requirements | Planned tests | Acceptance criterion |
|---|---|---|---|
| Deterministic replay + same-revision conflicts | REQ-CONC-005, REQ-ST-007..009 | `UT-REPLAY-001`, `IT-CONC-005`, `SEC-REPLAY-001` | DAG/topological replay is deterministic; concurrent same-revision events produce a conflict, not a winner; ULID/clock is not authoritative. |
| Fencing-token conflict | REQ-CONC-001..005 | `IT-CONC-002`, `SEC-CONC-002` | Concurrent takeovers conflict; stale tokens rejected; no highest-token auto-win. |
| Mutable authority merge | REQ-WS-002..003, REQ-CFG-001..006, REQ-VT-002..006 | `IT-MERGE-001..003` | Manifest/config/vault refs/session/handoff have declared merge/conflict behavior; event union alone is insufficient. |
| Artifact byte transport/missing bytes | REQ-SY-012, REQ-RST-007..009, O-014 | `IT-ART-001..002`, `E2E-HANDOFF-MISSING` | A manifest without verified bytes yields `recovery_status=incomplete` and is never called full recovery. |
| Capture fail-open vs write-gate enforcement | REQ-AD-013, REQ-RST-003 | `IT-ENF-001..002`, `CT-ENF-001` | Capture failure does not block; write gate is `enforced` only with blocking capability, otherwise labeled advisory/unavailable. |
| SessionEnd lightweight vs handoff prep | REQ-RST-001..007, REQ-AD-011 | `E2E-SESSIONEND-001`, `PERF-SESSIONEND-001` | SessionEnd only writes a marker within adapter budget; bundle/network work is observable and timeout never marks complete. |
| Artifact secret scanning | REQ-SEC-003, REQ-VT-009 | `SEC-SCAN-ART-001..003`, `SEC-BUNDLE-001` | Fake secrets in worktree, untracked files, bundle reachable history or export are caught and never appear in artifacts/sync/logs. |
| State transitions + parent example | REQ-ST-001..009 | `UT-ST-TRANS-001`, `UT-ST-PARENT-001` | Type-specific transition table matches examples; every non-goal has a parent; goal starts in_progress without violating rules. |
| Causal tool discovery | REQ-CM-001..010 | `IT-CM-DISCOVERY-001`, `CT-CM-TOOLS-001` | Tool names come from live `tools/list`; missing/renamed tools degrade explicitly; no guessed `search_memory`/`record_decision` contract. |
| Anchor MCP is not a mandatory memory proxy | REQ-CM-010, REQ-AD-010 | `IT-CM-DIRECT-001` | An agent can use causal-memory MCP directly; Anchor MCP memory tools are optional convenience. |
| Adapter evidence grading | REQ-AD-001..012 | `CT-AD-EVIDENCE-001` | Every capability row has a grade (`D`/`V`/`S`); timeout/context claims are not `V` without a spike. |
| User attestation | REQ-ST-013, REQ-EVID-002..004 | `SEC-ST-USER-001`, `IT-ST-ATTEST-001` | Agent cannot self-report `user_confirmed`; TTY/UI/signed local attestation required and audited. |
| Cross-device causal idempotency | REQ-CM-006, REQ-CM-013 | `IT-CM-IDEMP-001` | Same decision/outcome/evidence set produces the same `causal_write_id` on two devices; duplicate remote write is deduped. |
| No original absolute paths | REQ-RST-011, REQ-WS-005 | `IT-RST-PATH-001` | Restore works from another device without original absolute paths; missing transcript/artifact is reported, not guessed. |
| Skills install/activation | REQ-CFG-008, REQ-CFG-001..002 | `IT-SKILLS-001..003`, `CT-SKILLS-001` | Per-adapter install is idempotent; activation mode is recorded; unsupported activation is not claimed. |
| Invalid REQ-SEC reference | REQ-SEC-001..007 | `SEC-SEC-001..007`, coverage check | Every security requirement has tests; no dangling `REQ-SEC` reference remains. |
| Windows full acceptance | REQ-NFR-001, O-002/O-018/O-020 | `PKG-WIN-001..003`, `E2E-WIN-001` | Windows build, install/uninstall, keychain and real e2e are release gates; missing runner blocks release, not scope. |

## 4. Test data policy

- No real user, customer or production data.
- Fake secrets are visibly fake (`ANCHOR_TEST_*`) and matched by scanners.
- Transcripts used in tests are synthetic or explicitly redacted fixtures.
- A fixture with `source=recorded` must include the official doc URL and date.
- Live service tests are opt-in, use dedicated test accounts/tokens, and are
  never required for the default CI result.
- **CI mocks do not replace real adapter validation.** The release gate requires
  at least real Claude Code adapter e2e plus the documented Codex/Gemini
  contract-vs-real status; a mock-only result cannot be presented as a product
  validation.
- Performance numbers are reported with environment details; no fabricated
  “fast” claims.


## 4.2 Second design-review addendum (a901541)

| Review item | Requirements | Planned tests | Acceptance criterion |
|---|---|---|---|
| Mutation vs unordered ownership change | REQ-CONC-001..005 | `IT-CONC-006`, `SEC-CONC-003` | A mutation concurrent with any ownership event on the same node is isolated as a whole; no double-apply. Missing `causal_parents` means quarantined/conflicted, not replayed by guess. |
| Unified recovery enum | REQ-RST-001..011, REQ-SY-012 | `IT-RST-ENUM-001`, `E2E-HANDOFF-NOTPREPARED` | Enum is exactly `complete|partial|incomplete|stale_code`; no prepare before next start → `incomplete`; missing bytes → `incomplete`, never complete. |
| Enforcement guarantee boundary | REQ-AD-013, REQ-RST-003 | `IT-ENF-003..004`, `CT-ENF-002` | Hook timeout/malformed output never reported as guaranteed block; `enforced_within_wrapper` only for wrapped/Anchor-mediated writes; `anchor wrap` fallback documented/tested; no MCP proxy claim. |
| Legal/unsupported state transitions | REQ-ST-001..013 | `UT-ST-TRANS-002..003` | Legal edge table matches examples; unsupported edges return conflict/usage; `goal→done`/`goal→todo` rejected. |
| causal_write_id node scoping | REQ-CM-013 | `IT-CM-IDEMP-002` | ID includes `decision_node_id` (and outcome), so identical payloads on different decisions do not collide. |
| Projection sync default / O-013 | REQ-WS-002, REQ-SY-001 | `IT-PROJ-001` | `TREE.md`/`state.json` are local-only by default, not synced/committed; events/config/manifests are the synced set. O-013 no longer contradictory. |
| Traceability covers new IDs | All new REQ-SEC/CONC/CFG/SY/RST/AD/CM/ST IDs | `CI-TRACE-001` | Traceability table + matrix cover every new ID; no dangling reference. |
| CI mock vs real product validation | REQ-AD-001..013, REQ-OPS-004 | `CI-REAL-GATE-001`, `E2E-AD-REAL` | CI mocks never substitute for real adapter validation; release gate requires real adapter/final e2e or an explicit approved limitation. |
| 07 repository status current | DEL-008 / repo bootstrap | `DOC-07-001` | 07 says repository/README init succeeded and verified; no stale “repo not created” instruction; push/PR still blocked. |

## 5. Coverage enforcement

The implementation will include:

```text
acceptance/matrix.yaml   # requirement id → test ids + acceptance + status
internal/acceptance/     # Go test that parses the baseline + matrix
```

The test fails when:

- a `REQ-*` in the requirements baseline has no matrix entry;
- a matrix entry references a nonexistent test ID;
- a real-adapter requirement is satisfied only by a `fake`/`recorded` test;
- a known limitation lacks an owner, reason and review date.

The release report will include the generated coverage table and explicitly
list any `SKIP` real e2e row with the reason (for example, Codex/Gemini CLI not
installed in this environment).

## 6. Open acceptance questions

| ID | Question | Default |
|---|---|---|
| O-016 | Exact hook latency budget on each platform. | Measure first; target p95 < 150 ms local capture. |
| O-017 | Minimum agent CLI versions. | Determined by adapter spike; recorded in capability descriptors. |
| O-018 | Which OS keychain backends are release-blocking. | macOS, Linux Secret Service and Windows Credential Manager are all release-blocking full-acceptance targets; runner provisioning is a delivery task, not a scope option. |
| O-019 | Whether live causal-memory tests run in CI. | No; opt-in with a dedicated test token. |
| O-020 | How to provision the Windows runner for required e2e. | Windows build + install/uninstall + keychain e2e are required; a missing runner blocks release, it does not downgrade scope. |

# Design Review Response

**Review input:** architect review of docs/00–05, 2026-11-15.  
**Status:** design revision only. This is **not** user approval, does not change
product scope, and does not authorize repository creation, push, PR, or a
default-branch write. No product code or product test was written or executed.

## 1. P0 findings

| # | Finding | Disposition | Revised files |
|---|---|---|---|
| P0-1 | Same-revision offline events, deterministic replay, conflict isolation and fencing-token conflicts were undefined; ULID ordering was treated as authority. | Added causal DAG replay (`device_seq`, `causal_parents`, `base_revision`, `event_hash`), deterministic topological order with event-ID tie-break, explicit same-revision sibling conflicts, and fencing rules where concurrent takeovers are conflicts, not highest-token wins. ULID is identity only. | `docs/01-architecture.md` §6; `docs/02-data-model.md` §3, §5; `docs/05-acceptance-mapping.md` §4.1 |
| P0-2 | Mutable authorities (`manifest`, config, vault refs, sessions, handoff) had no merge strategy; event union is insufficient. | Declared canonical event sources and a merge/conflict matrix for each authority. Config uses key-level three-way merge; vault refs merge by name/provider/key; handoff/evidence are immutable; projections are regenerated. | `docs/01-architecture.md` §6.5; `docs/02-data-model.md` §5.4 |
| P0-3 | Sync topology and artifact byte transport remain open; a manifest without bytes must not count as complete. | Kept O-001/O-004/O-007/O-014 open. Added `recovery_status` and `artifact_availability` (`bytes_present|metadata_only|missing`); missing bytes → `incomplete`. Presented options/tradeoffs; did **not** select an independent state repo. | `docs/02-data-model.md` §7, §14; `docs/00-requirements-baseline.md` O-014; `docs/06-decisions-and-open-questions.md` P-005 |
| P0-4 | “Verify before write” conflicted with hook fail-open; adapters vary in blocking ability. | Split capture (fail-open, logged gap) from mutation policy (fail-closed only where the adapter can block; otherwise `advisory`/`unavailable`). Added capability descriptors and enforcement states. | `docs/01-architecture.md` §4.3, §7; `docs/03-adapter-matrix.md` §1.2; `docs/04-security-model.md` §2; `docs/05-acceptance-mapping.md` §4.1 |
| P0-5 | SessionEnd could require large bundle/sync work and a timeout could be marked complete. | SessionEnd now writes only a lightweight local marker/checkpoint. Handoff preparation is explicit/observable with `pending|running|complete|failed|timed_out`; timeout never completes. | `docs/01-architecture.md` §5.0; `docs/02-data-model.md` §7; `docs/03-adapter-matrix.md`; `docs/05-acceptance-mapping.md` §4.1 |
| P0-6 | Secret scanning covered commit/push only, not artifact generation/export/sync, bundle history or untracked files. | Added scanning points and a fail/abort policy for capture, handoff bundles/patches, untracked manifests, export, sync, restore/import; bundle reachable history must be scanned. Added fake-secret non-leak tests. | `docs/04-security-model.md` §4.1, §13; `docs/05-acceptance-mapping.md` §4.1 |

## 2. P1 findings

| # | Finding | Disposition | Revised files |
|---|---|---|---|
| P1-1 | State-transition table contradicted goal/parent example. | Fixed decision example parent to the goal; added type-specific transitions. Goals may start `in_progress`; `done` is decision/task/branch-specific. | `docs/02-data-model.md` §4.2–4.3 |
| P1-2 | causal tool names were guessed; Anchor MCP must not be a mandatory memory proxy. | Tool names are discovered via `tools/list`; a role mapping (`search`/`write`) is validated; `search_memory`/`record_decision` are candidates only. Anchor memory tools are optional; direct causal-memory MCP use is supported. | `docs/01-architecture.md` §4.5; `docs/00-requirements-baseline.md` REQ-CM-010/013; `docs/05-acceptance-mapping.md` §4.1 |
| P1-3 | Adapter claims were not evidence-graded; hook timeouts were treated as established. | Added `D` (documented), `V` (runtime-verified), `S` (needs spike) grades. All hook timeouts/context limits are `D`; no runtime verification claimed. Codex/Gemini are contract-only until installed. | `docs/03-adapter-matrix.md` §1.1, matrix notes; `docs/05-acceptance-mapping.md` |
| P1-4 | `actor=user` confirmation could be agent self-reported. | Requires local user attestation (`tty_confirm|ui_session|signed_cli`); replay validates attestation and local session/signature; agent events cannot set `user_confirmed`. | `docs/02-data-model.md` §4.3; `docs/00-requirements-baseline.md` REQ-ST-013 |
| P1-5 | Cross-device causal writes lacked idempotency. | Added deterministic `causal_write_id` from workspace/decision/outcome/relation/sorted evidence; remote idempotency key and local dedupe. | `docs/02-data-model.md` §9; REQ-CM-013 |
| P1-6 | Recovery could depend on original machine absolute paths. | Transcript references are opaque/device-local; handoff contains no original absolute paths; missing local transcript/artifact is reported, not guessed. | `docs/02-data-model.md` §7–8; REQ-RST-011 |
| P1-7 | Skills install/activation design was missing. | Added source/skills layout, per-adapter rendering, adapter-specific activation modes (`auto|command|unsupported`), idempotent install/uninstall and secret scanning. | `docs/02-data-model.md` §10.1; REQ-CFG-008; `docs/03-adapter-matrix.md` |
| P1-8 | Invalid `REQ-SEC` reference. | Added REQ-SEC-001..007 to the baseline and mapped them in the acceptance plan; removed the dangling reference. | `docs/00-requirements-baseline.md` §4.13; `docs/05-acceptance-mapping.md` |
| P1-9 | Windows acceptance was written as optional/gated. | Windows is now a release-blocking full-acceptance target (build, install/uninstall, keychain, real e2e). A missing runner blocks release; it does not reduce scope. | `docs/00-requirements-baseline.md` REQ-NFR-001/O-002; `docs/05-acceptance-mapping.md` O-018/O-020; `docs/06` |

## 3. User decisions still required

These are **options with recommendations**, not decisions made here:

| ID | Decision | Options | Recommendation / tradeoff |
|---|---|---|---|
| D-SYNC | Workspace sync topology | (a) `.anchor/` in project repo; (b) dedicated state Git repo; (c) orphan branch in project repo | Recommend (b) for privacy and fewer code-repo conflicts; (a) is simplest onboarding but mixes agent state with code history; (c) keeps one remote but complicates worktrees. **Not selected.** |
| D-BYTES | Uncommitted-code artifact bytes | (a) Git bundle committed in state repo; (b) Git LFS/external artifact store; (c) metadata-only with manual transfer | Recommend (a) for small repos + (b) for large/binary; metadata-only is explicitly incomplete for restore. **Not selected.** |
| D-STACK | Core stack | Go core + TS web (P-001) vs Rust core + TS web vs TS-only | Recommend Go + TS for hook startup, cross-compilation and official MCP Go SDK; Rust remains viable behind the same interfaces. **Not user-approved.** |
| D-WIN | Windows acceptance | Full release-blocking vs optional | Architect review says full acceptance is required; UI/platform scope still needs user confirmation of architecture matrix. |
| D-CM | causal-memory test policy | Mock-only vs opt-in live test with dedicated token | Recommend mock in CI + opt-in live test with a revocable test token; no real user key. |
| D-HANDOFF | Default transcript depth | Structured summary/pointers vs full excerpts | Recommend structured default + explicit `--full`; avoids token bloat but users may want more. |


## 4. Second review findings (a901541)

These are design revisions only; no product code or product test was added.

| # | Finding | Disposition | Files |
|---|---|---|---|
| 2-1 | A same-node mutation concurrent with a causally unordered ownership change could be legal at its own causal point and double-apply. | Added whole-mutation isolation: any concurrent ownership event isolates the mutation, not just the ownership field. `causal_parents` must include the ownership event/version or a later barrier; missing metadata = quarantine/conflict, no guessing. Unified the per-device counter to `device_seq` only. | `docs/01-architecture.md` §6.2–6.3; `docs/02-data-model.md` §3, §5.2–5.3 |
| 2-2 | `recovery_status` enum was inconsistent; no-prepare next start was not explicit. | Unified enum to `complete|partial|incomplete|stale_code`; no prepared packet for the current state → `incomplete`; missing bytes/artifacts → `incomplete`; stale code separate. | `docs/02-data-model.md` §7; `docs/01-architecture.md` §5.0/§7 |
| 2-3 | Hook timeout/malformed output means `enforced` was overstated; MCP proxy cannot intercept all edits. | Replaced the claim with `enforced_within_wrapper|best_effort_hook|advisory|observed|unavailable`; documented the verifiable boundary, timeout/malformed fail-open, and `anchor wrap` fallback. Explicitly rejected the “MCP proxy intercepts every edit” claim. | `docs/01-architecture.md` §4.3/§7; `docs/03-adapter-matrix.md` §1.2; `docs/04-security-model.md` |
| 2-4 | State transition table lacked unsupported edges; causal_write_id could collide across decisions; TREE.md sync default contradicted O-013; traceability missed new IDs. | Added legal/unsupported edge table; included `decision_node_id` in `causal_write_id`; made `TREE.md`/`state.json` local-only projections not synced by default and resolved O-013; expanded traceability to new REQ IDs. | `docs/02-data-model.md` §4.3, §9, §14; `docs/00-requirements-baseline.md` §8; `docs/05-acceptance-mapping.md` §4.2 |
| 2-5 | CI mocks could be mistaken for product validation; 07 was stale. | Added explicit real-adapter/final-e2e release gate distinct from CI mocks; updated 07 to verified repo/README success and blocked push/PR. | `docs/05-acceptance-mapping.md`; `docs/07-repo-bootstrap.md`; `docs/06` |

### Technical directions accepted as reversible design defaults

Per the architect, Go core + TS Web, loopback-only web UI, and structured handoff
+ evidence pointers are adopted as **reversible design defaults**, not user
approval for remote operations. D-SYNC topology and D-BYTES (whether artifact
bytes leave the machine) remain open/configurable pending the user's answer.

## 5. Verification performed this round

- Documentation edits only; no product code, no tests executed, no live
  integrations.
- `acceptance/matrix.yaml` regenerated from the revised baseline; requirement
  IDs and matrix IDs cross-checked for exact set equality.
- Local commit created on `design/baseline`; no remote configured, no push, no
  PR, no default-branch write.

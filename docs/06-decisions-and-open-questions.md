# Decisions, Proposals and Open Questions

**Status:** design review. This file does not authorize remote repository
creation, pushes, PRs, or writes to a default branch.

## 1. Confirmed decisions (do not re-litigate accidentally)

See `docs/00-requirements-baseline.md` D-001..D-016. Summary:

- Full product scope; no shrunken MVP, no self-removed features.
- causal-memory is an independent remote MCP service; no local engine/SQLite,
  no `causal.db` in the workspace or Git, no memory-engine reimplementation.
- MCP is the tool protocol; Anchor does not rebuild it or force a proxy.
- Causal conclusions require evidence or explicit confirmation; task completion
  is not causality.
- Chat summaries are not complete recovery; successor verification is required.
- Secrets never enter model context/transcripts/logs/synced files.
- Same-task concurrent ownership must be controlled.
- Roost is a boundary only; no changes to existing Roost repos.
- No real payments or production keys; real adapters vs mocks must be labeled.
- Install/migration/uninstall/security/docs/CI/coverage/known-limitations are
  release deliverables.

## 2. Engineering proposals awaiting review

| ID | Proposal | Rationale | Status |
|---|---|---|---|
| P-001 | Go core + TypeScript/React web UI, single binary with embedded assets. | Fast hook startup, cross-compilation, official MCP Go SDK, maintainable UI. | `[I]` awaiting architect/user approval |
| P-002 | Event-sourced canonical state with rebuildable projections. | Git-friendly merges, audit, tombstones, concurrency. | `[I]` proposed |
| P-003 | Local-first; causal-memory and Git sync optional/degradable. | Core work must not depend on network. | `[I]` proposed |
| P-004 | Additive causal links (`proposed` → `confirmed`) instead of auto-conclusions. | Satisfies D-007. | `[I]` proposed |
| P-005 | Present dedicated state Git repo/remote as a **recommendation**, not a selected default. | Privacy, fewer code-repo conflicts. | `[?]` O-001; user decides |
| P-006 | OS keychain first; encrypted-file fallback explicit. | Native security; headless environments need a fallback. | `[?]` O-006 |
| P-007 | Loopback-only web UI with one-time token; LAN is opt-in. | Least privilege. | `[?]` O-009 |
| P-008 | Git bundle + patch manifest for uncommitted-code handoff. | Binary-safe + reviewable. | `[?]` O-007 |
| P-009 | Context restore is tiered: structured packet default, full excerpts on request. | Token budgets vs completeness. | `[?]` O-008 |

## 3. Open questions

The authoritative table is in `docs/00-requirements-baseline.md` section 7
(O-001..O-010) and `docs/05-acceptance-mapping.md` section 6 (O-016..O-020).

| ID | Decision needed | Blocks |
|---|---|---|
| O-001 | Sync topology: project repo vs dedicated state repo/orphan branch. | Sync implementation, privacy model, artifact storage. |
| O-002 | Architectures within macOS/Linux/Windows; all three OSes are release-blocking full acceptance. | Packaging, install/uninstall, keychain and real e2e for each OS. Windows is not optional. |
| O-003 | Supported agent CLI versions. | Adapter compatibility claims. |
| O-004 | Whether `.anchor/` may be committed to the project repo. | Default workspace behavior. |
| O-005 | causal-memory MCP endpoint/tool contract/test-account policy. | Bridge integration and live e2e. |
| O-006 | Linux vault fallback without Secret Service. | Headless support. |
| O-007 | Uncommitted code artifact format (bundle/patch/both). | Handoff implementation. |
| O-008 | Default transcript injection depth. | Restore packet policy. |
| O-009 | Local-only vs LAN web UI. | Web security model. |
| O-010 | Concrete Roost boundary contract. | Optional runtime integration. |

**None of these may be silently defaulted in code.** Where a default is needed
to keep local prototyping moving, it must be a reversible configuration with an
explicit “proposal, not approved” label.

## 4. Delivery phases (sequencing, not scope cuts)

Every phase ends with a draft PR and evidence. The release gate covers all
requirements; a phase completing early does not remove later scope.

| Phase | Content | Exit evidence |
|---|---|---|
| 0 | This design baseline (docs 00–07, acceptance matrix). | Architect review; open questions recorded. |
| 1 | Workspace/event store/tree CLI, schema migrations, locks. | Unit + integration tests, coverage mapping. |
| 2 | Adapter framework + Claude Code real adapter; Codex/Gemini contract fixtures. | Real Claude e2e, contract tests, truthful capability labels. |
| 3 | Capture, handoff packets, uncommitted-code state, successor verification. | Cross-agent e2e where available; security tests. |
| 4 | causal-memory remote MCP bridge, retry/idempotency/offline degradation. | Mock MCP contract/chaos tests; live test opt-in. |
| 5 | Git sync, event merge, tombstones, conflicts, ownership fencing. | Real local-bare-repo tests; two-worktree simulation. |
| 6 | Unified config/rules/skills, vault, redaction, secret injection. | Renderer golden tests, keychain tests, leak corpus. |
| 7 | Web UI (tree, evidence, causal refs, sync/ownership). | Playwright e2e, CSRF/Origin tests, accessibility smoke. |
| 8 | Packaging, migration/uninstall, CI, docs, known limitations, release. | Packaging matrix, coverage report, independent acceptance. |

## 5. Approval checklist

The user (not another agent's relay) must confirm each item before the action:

1. **Design baseline approval** — requirements 00, architecture 01, data model
   02, adapter matrix 03, security 04, acceptance 05.
2. **Stack approval** — Go core + TS web (P-001), or choose Rust/other.
3. **Sync topology** — O-001/O-004.
4. **Repository creation** — create private `JingxuanC/anchor` under the
   authenticated account.
5. **Default-branch bootstrap** — choose GitHub auto-init vs a one-time
   bootstrap `main` commit; this is the only allowed write to the default branch
   before feature branches.
6. **Platform matrix** — O-002, including Windows keychain/e2e expectations.
7. **Agent versions** — O-003; what counts as supported real e2e.
8. **causal-memory test policy** — endpoint and a dedicated revocable test token
   supplied via environment; never committed.
9. **Release signing/packaging channels** — checksums/signatures, Homebrew/winget
   or archive-only.
10. **Roost boundary** — confirm interface-only scope for this delivery.

## 6. Scope guardrails

- Do not introduce a local causal-memory database or Git-sync `causal.db`.
- Do not build a model router/relay or subscription-sharing feature.
- Do not reimplement MCP; use the official SDK and only add transport shims if
  a required transport is missing.
- Do not modify `roostbot`, `LangAlpha`, or `market` repositories.
- Do not reduce the full scope to an “MVP”; phases are delivery order.
- Do not mark a feature real without a real e2e test or label it as contract
  only.
- Do not claim the design is implemented or tested; this is a baseline.

## 7. Risk register (design stage)

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Hook formats change | Medium | High | Capability detection, adapter isolation, contract fixtures, versioned descriptors. |
| Context caps truncate handoff | High | Medium | Tiered packet, per-adapter budgets, evidence pointers, explicit truncation warnings. |
| Secret leak through tool output | Medium | Critical | Exact-value redaction, deny paths, output caps, leak corpus, no secrets in tests. |
| Git merge complexity | Medium | High | Event-sourced union, tombstones, conflict records, dry-run/resolve UI. |
| Concurrent agents | Medium | High | Leases/fencing, revisions, ownership UI. |
| Remote MCP outage | High | Medium | Bounded queue, circuit breaker, stale cache, visible degradation. |
| Uncommitted-code loss | Medium | Critical | Dry-run, backup refs, hashes, explicit force. |
| Scope drift into routing | Low | High | Non-goals, requirement coverage gate, review. |
| Cross-platform keychain gaps | Medium | Medium | Explicit fallback, platform matrix, label unsupported. |
| False “working” demo with mocks | Medium | High | Mandatory real/recorded/fake labels; no mock claims. |

## 8. Design-review response

Architect review findings and point-by-point dispositions are in `docs/08-design-review-response.md`. The review is not user approval for repository creation or scope changes.

## 9. Current status

- Original design branch preserved at `/workspace/anchor` (`design/baseline`,
  `a901541`).
- Mainline branch prepared locally at `/workspace/anchor-mainline`
  (`design/baseline-mainline`) with the verified repository init commit
  `39b6fffdb35511859a82632d174b97ca304e26f5` as its ancestor.
- Repository existence/visibility/default branch/init SHA were verified
  read-only; no push, PR, default-branch write, branch creation on the remote,
  product implementation, or live integration has been performed.
- Next action is architect/user review plus direct approval for pushing the
  branch and opening a draft PR (see `docs/07-repo-bootstrap.md`).

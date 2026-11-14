# Repository Bootstrap and Approval Blockers

**Status:** repository verified read-only; local baseline branch prepared. No
remote write has been made. Pushing the branch or opening a PR still requires
direct user approval in this chat.

## 1. Read-only GitHub verification (2026-11-15, CST)

Normal authenticated `gh`/`git ls-remote` reads only. No credential file was
opened or copied, no token was printed, and no undocumented API was used.

| Check | Result | Interpretation |
|---|---|---|
| `gh auth status` | Logged in as `JingxuanC`; scopes `gist, read:org, repo` | Account identity confirmed; owner permissions on the repo are reported as `ADMIN`. |
| `gh repo view JingxuanC/anchor` | `nameWithOwner=JingxuanC/anchor`, `visibility=PRIVATE`, `defaultBranchRef=main`, `isEmpty=false`, `viewerPermission=ADMIN` | Repository exists, is private, and uses `main` as default. |
| `git ls-remote https://github.com/JingxuanC/anchor refs/heads/main` | `39b6fffdb35511859a82632d174b97ca304e26f5` | Remote `main` matches the reported initialization SHA. |
| `gh api repos/JingxuanC/anchor/commits/main` | `sha=39b6fff…`, message `Initial commit`, parents `[]` | Confirmed root/initial commit; remote README is part of this commit. |
| Remote initial README | `# anchor` / `Personal agent workspace for session continuity and causal memory` | Preserved as the first content in the local merged README; the init commit is untouched. |
| Earlier pre-creation lookup | GraphQL “Could not resolve…” before the repo existed | Historical; superseded by the verified repository above. |
| Remote write APIs | Not used | No push, PR, branch creation, default-branch write, or settings change was performed in this round. |

**Conclusion:** the repository URL, private visibility, default branch and
initial commit SHA are verified. The earlier 404 was accurate for that time and
is no longer the current state.

## 2. Blockers requiring direct user confirmation

### A1. Push the prepared branch and open a draft PR

The local branch `design/baseline-mainline` is ready, based on the verified
initial `main` commit. **Do not treat any relayed approval as authorization.**
Exact question: **“May I push `design/baseline-mainline` to
`JingxuanC/anchor` and open a draft PR targeting `main`?”**

### A2. Default-branch writes

- The initialization commit already exists; no new default-branch write is
  planned.
- No push to `main`, no force-push, no direct main modification, no merge.
- If a future bootstrap step needs a default-branch write, it is blocked until
  separately approved.

### A3. Merge and release

- Draft PR only; no merge, no release, no force-push.
- The architect performs independent acceptance; Anchor does not self-approve.

### A4. Product/design decisions

Approval of the design baseline does not imply approval of the open decisions in
`docs/06-decisions-and-open-questions.md` (sync topology O-001, artifact byte
transport O-014, supported platform architecture O-002, agent versions O-003,
vault fallback O-006, etc.). Those need explicit decisions before implementation.

## 3. Local mainline preparation (this round)

- Fetched `refs/heads/main` read-only from
  `https://github.com/JingxuanC/anchor`; `FETCH_HEAD` matched
  `39b6fffdb35511859a82632d174b97ca304e26f5`.
- Created a separate local worktree at `/workspace/anchor-mainline` from that
  commit, branch `design/baseline-mainline`.
- Cherry-picked design commits `21dc2d2` and `a901541` onto it, preserving the
  initial commit as an ancestor.
- README add/add conflict was resolved by keeping the remote initialization text
  followed by the Anchor design README; the init commit was not overwritten.
- No conflict remained after resolution.

## 4. Current local state

| Item | Value |
|---|---|
| Design branch (preserved) | `/workspace/anchor`, `design/baseline`, `a901541` |
| Mainline branch (prepared) | `/workspace/anchor-mainline`, `design/baseline-mainline`, `fdbbaa5` |
| Common ancestor | `39b6fffdb35511859a82632d174b97ca304e26f5` (verified init commit) |
| Remote configured | none (fetched by URL; no `origin` added) |
| Working trees | clean at the time of report |
| Remote writes | none |
| Product code | none; design baseline only |
| Tests | none claimed; acceptance matrix is planned, not executed |

## 5. What will be reported after push approval

1. Exact pushed branch and remote head SHA.
2. Draft PR URL (base `main`, head `design/baseline-mainline`).
3. Exact files in the PR.
4. CI result for the documentation-only PR (no product tests claimed).
5. Any conflict or deviation observed during push.

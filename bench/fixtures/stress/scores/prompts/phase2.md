# Phase 2 — continue in a fresh session

A previous session finished phase 1 of this `scores` task. That process has
exited and its chat history is gone; this is a **fresh** session continuing the
same work on the same repository.

## What to add in phase 2

Create `rank.go` in this package implementing:

    func Rank(players []Player) []string

Fixed requirements (independent of the phase-1 convention):

- Return every player's name exactly once, ordered by `Score` descending.
- Do not modify the input slice.
- The previous session chose how equal scores are ordered. Recover that
  convention from whatever durable context this session has; if there is none,
  use your best guess.
- Do not modify `scores.go` or `scores_test.go`. Keep the module stdlib-only.
- `go test ./...` (visible tests plus the grader's hidden test) must pass.

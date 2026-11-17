# Phase 2 — continue in a fresh session

A previous session finished phase 1 of this `money` task. That process has
exited and its chat history is gone; this is a **fresh** session continuing the
same work on the same repository.

## What to add in phase 2

Create `allocate.go` in this package implementing:

    func Allocate(totalCents int64, weights []int64) ([]int64, error)

Fixed requirements (independent of the phase-1 convention):

- `weights` must be non-empty, every weight must be > 0, and `totalCents` must
  be >= 0; otherwise return a non-nil error.
- Use integer division: the floor share of part `i` is
  `totalCents * weights[i] / sum(weights)`.
- The previous session chose how leftover cents are distributed. Recover that
  convention from whatever durable context this session has; if there is none,
  use your best guess.
- Do not modify `money.go` or `money_test.go`. Keep the module stdlib-only.
- `go test ./...` (visible tests plus the grader's hidden test) must pass.

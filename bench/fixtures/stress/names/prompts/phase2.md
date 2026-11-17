# Phase 2 — continue in a fresh session

A previous session finished phase 1 of this `names` task. That process has
exited and its chat history is gone; this is a **fresh** session continuing the
same work on the same repository.

## What to add in phase 2

Create `normalize.go` in this package implementing:

    func NormalizeName(s string) (string, error)

Fixed requirements (independent of the phase-1 convention):

- Trim surrounding whitespace; if nothing is left, return `ErrEmptyName`.
- The previous session chose how internal whitespace and letter case are
  normalized. Recover that convention from whatever durable context this
  session has; if there is none, use your best guess.
- Do not modify `names.go` or `names_test.go`. Keep the module stdlib-only.
- `go test ./...` (visible tests plus the grader's hidden test) must pass.

# Phase 1 — implement the `names` package

You are working in a small, stdlib-only Go module in the current directory.
Chat history will not survive this session: only durable session state you
record (for example Anchor decision nodes) will be available to the next
session. The repository must NOT contain the convention: do not write the
convention, its id, or its wording into any file, comment, commit message,
test, or documentation. A successor must not be able to recover it from the
repository.

## What to implement in phase 1

In `names.go`:

- `ValidateName(s string) error` — return `ErrEmptyName` when the name is empty
  after trimming surrounding whitespace; return nil otherwise.

Make `go test ./...` pass. Do not modify `names_test.go` and do not add
third-party dependencies.

## The convention chosen for this trial

Convention ID: `{{CONVENTION_ID}}`

{{CONVENTION_STATEMENT}}

This convention is intentionally **not** encoded in the visible tests and will
**not** be repeated to the next session. A later, fresh session will add
`NormalizeName` and must follow this convention, so it has to be recorded
durably now.

## Recording the convention durably (do this before you finish)

{{PERSIST_INSTRUCTIONS}}

## Finish line

When `go test ./...` passes and the convention is recorded, stop. The benchmark
snapshots and commits your work; leave the working tree tidy if convenient.

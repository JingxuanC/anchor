# Phase 1 — implement the `money` package

You are working in a small, stdlib-only Go module in the current directory.
Chat history will not survive this session: only durable session state you
record (for example Anchor decision nodes) will be available to the next
session. The repository must NOT contain the convention: do not write the
convention, its id, or its wording into any file, comment, commit message,
test, or documentation. A successor must not be able to recover it from the
repository.

## What to implement in phase 1

In `money.go`:

- `FormatCents(cents int64) string` — render a cent amount as a US-dollar
  string: `0` → `"$0.00"`, `105` → `"$1.05"`, `-250` → `"-$2.50"`,
  `123456` → `"$1234.56"`.
- `SumCents(values ...int64) int64` — the arithmetic sum (zero for no values).

Make `go test ./...` pass. Do not modify `money_test.go` and do not add
third-party dependencies.

## The convention chosen for this trial

Convention ID: `{{CONVENTION_ID}}`

{{CONVENTION_STATEMENT}}

This convention is intentionally **not** encoded in the visible tests and will
**not** be repeated to the next session. A later, fresh session will add
`Allocate` and must follow this convention, so it has to be recorded durably
now.

## Recording the convention durably (do this before you finish)

{{PERSIST_INSTRUCTIONS}}

## Finish line

When `go test ./...` passes and the convention is recorded, stop. The benchmark
snapshots and commits your work; leave the working tree tidy if convenient.

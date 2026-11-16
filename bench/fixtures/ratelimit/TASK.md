# Task: token-bucket rate limiter (phase 1 of 2)

This directory is a small, dependency-free Go module (`example.com/ratelimit`).
`ratelimit.go` currently contains a compiling stub and the visible acceptance
tests in `ratelimit_test.go` fail.

## What to build

Implement the public API in `ratelimit.go` so that `go test ./...` passes:

- `New(capacity int, interval time.Duration, now func() time.Time) *Limiter`
  - the limiter starts full: `capacity` tokens are available;
  - `now` is the **only** clock the limiter may read; `nil` means `time.Now`;
- `(*Limiter).Allow(n int) bool` — take `n` tokens if at least `n` are
  available, otherwise leave the limiter unchanged and return `false`;
- `(*Limiter).Tokens() int` — the current number of whole tokens.

`interval` is the time it takes to refill one token. Refill is lazy: no
goroutines, no timers, no sleeps.

## Constraints

- Standard library only; fully offline; Go 1.23+.
- Keep the existing public signatures stable.
- Do not edit `ratelimit_test.go` or any other acceptance file.

## Recording decisions (important for phase 2)

Phase 2 is a **resume** phase: a fresh agent process will continue this project
after this session ends. It will receive only a short summary, so the
conventions a successor cannot re-derive from the code alone are the ones you
must record durably before you finish:

- how partial refill intervals are rounded and how a wait is computed;
- what an oversized request (`n > capacity`) means;
- what a non-positive request means;
- the clock-injection convention.

If your orchestrator provides a session-continuity tool (for example Anchor),
record these as durable decisions with that tool (e.g. one decision node per
convention) and attach evidence for your implementation. A chat transcript is
not guaranteed to survive into phase 2; the session tree is.

## Acceptance

`go test ./...` in this directory must pass. See `ACCEPT.md` for how the harness
grades a trial.

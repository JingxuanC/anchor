# Acceptance

## Grading command

The A/B harness grades both phases with the same command, executed inside the
trial's project copy after the agent process exits:

    go test -count=1 ./...

The harness first restores the canonical phase-1 tests (`ratelimit_test.go`)
and, for phase 2, additionally drops in a hidden `wait_time_accept_test.go`.
Editing the visible tests therefore cannot change the grading result.

## Phase 1

The canonical phase-1 tests in `ratelimit_test.go` must pass. They cover:

- a new limiter starts full;
- `Allow` consumes tokens and rejects requests when the bucket is empty;
- a whole `interval` of elapsed time refills exactly one token;
- `Allow` rejects non-positive requests.

## Phase 2 (resume phase, issued by the orchestrator after phase 1)

The hidden phase-2 test file checks the method

    func (l *Limiter) WaitTime(n int) time.Duration

which reports how long the caller must wait before `n` tokens can be acquired
(`0` if they are available now, `-1` when `n` can never be satisfied). The test
expects the conventions that were recorded as phase-1 decisions: whole-interval
integer rounding for waits, the oversized-request sentinel, and the non-positive
request rule. Those decisions are part of the durable handoff, not of the
visible test files, so a resumed agent must recover them from the session state
(or from whatever durable notes the previous agent left behind).

The canonical phase-1 tests are also run in phase 2: an implementation that
breaks phase 1 cannot pass the resume phase.

## Offline

Every acceptance run is offline (`GOPROXY=off`, stdlib only). No third-party
modules are used or required.

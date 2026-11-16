package ratelimit_test

import (
	"testing"
	"time"
)

// This file is the hidden phase-2 acceptance test. The harness copies it into
// the trial's project copy only after the phase-2 agent process has exited, so
// a resumed agent cannot read it. It encodes the phase-1 decisions that must
// survive the handoff:
//
//   - dec-nonpositive: WaitTime(n <= 0) == 0.
//   - dec-round: waits round up to the next whole refill boundary using integer
//     arithmetic: with capacity 3, interval 10ms, drained at t=0 and clock at
//     t=25ms, WaitTime(3) == 5ms (not 10ms).
//   - dec-oversize: WaitTime(n > capacity) == -1 and never consumes tokens.

func TestWaitTimeNonPositive(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Millisecond)
	if got := l.WaitTime(0); got != 0 {
		t.Fatalf("WaitTime(0) = %v, want 0", got)
	}
	if got := l.WaitTime(-5); got != 0 {
		t.Fatalf("WaitTime(-5) = %v, want 0", got)
	}
}

func TestWaitTimeZeroWhenTokensAvailable(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Millisecond)
	if got := l.WaitTime(2); got != 0 {
		t.Fatalf("WaitTime(2) on a full limiter = %v, want 0", got)
	}
}

func TestWaitTimeBoundaryRounding(t *testing.T) {
	l, clock := newTestLimiter(3, 10*time.Millisecond)
	if !l.Allow(3) {
		t.Fatal("draining the limiter failed")
	}
	// After 25ms two whole tokens have accrued and the next refill boundary is
	// 5ms away, so the shortest possible wait for the third token is 5ms.
	clock.advance(25 * time.Millisecond)
	if got := l.WaitTime(3); got != 5*time.Millisecond {
		t.Fatalf("WaitTime(3) = %v, want 5ms (next whole refill boundary)", got)
	}
}

func TestWaitTimeOversizedIsImpossible(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Millisecond)
	if got := l.WaitTime(4); got != time.Duration(-1) {
		t.Fatalf("WaitTime(4) = %v, want -1", got)
	}
	if got := l.Tokens(); got != 3 {
		t.Fatalf("Tokens() = %d after an oversized WaitTime, want 3 (no consumption)", got)
	}
}

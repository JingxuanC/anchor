package ratelimit_test

import (
	"testing"
	"time"

	"example.com/ratelimit"
)

// fakeClock is a deterministic clock shared by the visible and hidden
// acceptance tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(capacity int, interval time.Duration) (*ratelimit.Limiter, *fakeClock) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	return ratelimit.New(capacity, interval, clock.now), clock
}

func TestNewLimiterStartsFull(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Millisecond)
	if got := l.Tokens(); got != 3 {
		t.Fatalf("Tokens() = %d, want 3", got)
	}
}

func TestAllowConsumesTokens(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Millisecond)
	if !l.Allow(2) {
		t.Fatal("Allow(2) = false, want true")
	}
	if !l.Allow(1) {
		t.Fatal("Allow(1) = false, want true")
	}
	if l.Allow(1) {
		t.Fatal("Allow(1) on an empty limiter = true, want false")
	}
}

func TestRefillAfterWholeInterval(t *testing.T) {
	l, clock := newTestLimiter(3, 10*time.Millisecond)
	if !l.Allow(3) {
		t.Fatal("draining the limiter failed")
	}
	clock.advance(10 * time.Millisecond)
	if got := l.Tokens(); got != 1 {
		t.Fatalf("Tokens() after one whole interval = %d, want 1", got)
	}
	if !l.Allow(1) {
		t.Fatal("Allow(1) after refill = false, want true")
	}
}

func TestAllowRejectsNonPositive(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Millisecond)
	if l.Allow(0) {
		t.Fatal("Allow(0) = true, want false")
	}
	if l.Allow(-1) {
		t.Fatal("Allow(-1) = true, want false")
	}
}

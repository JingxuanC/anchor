package ratelimit

import "time"

// Limiter is a token-bucket rate limiter. The mock agent's phase-1 output: the
// public API is implemented, refill is lazy and integer-only. WaitTime does not
// exist yet (that is the phase-2 requirement).
type Limiter struct {
	capacity int
	interval time.Duration
	now      func() time.Time
	tokens   int
	last     time.Time
}

// New returns a Limiter that starts full.
func New(capacity int, interval time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{capacity: capacity, interval: interval, now: now, tokens: capacity, last: now()}
}

func (l *Limiter) refill() {
	if l.interval <= 0 {
		l.tokens = l.capacity
		l.last = l.now()
		return
	}
	elapsed := l.now().Sub(l.last)
	whole := int(elapsed / l.interval)
	if whole <= 0 {
		return
	}
	l.tokens += whole
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
	l.last = l.last.Add(time.Duration(whole) * l.interval)
}

// Allow reports whether n tokens could be taken and consumes them on success.
func (l *Limiter) Allow(n int) bool {
	if n <= 0 || n > l.capacity {
		return false
	}
	l.refill()
	if l.tokens >= n {
		l.tokens -= n
		return true
	}
	return false
}

// Tokens reports the current number of whole tokens.
func (l *Limiter) Tokens() int {
	l.refill()
	return l.tokens
}

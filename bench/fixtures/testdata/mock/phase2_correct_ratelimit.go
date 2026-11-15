package ratelimit

import "time"

// Limiter is a token-bucket rate limiter. The mock Anchor agent's phase-2
// output: it read the generated Anchor context/handoff file and applied
// dec-clock, dec-round, dec-oversize and dec-nonpositive from the session tree.
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

// WaitTime implements dec-clock, dec-round, dec-oversize and dec-nonpositive
// from the Anchor handoff: the injected clock only, integer whole-interval
// rounding, -1 for impossible requests, and zero for non-positive requests.
func (l *Limiter) WaitTime(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	if n > l.capacity {
		return time.Duration(-1)
	}
	if l.interval <= 0 {
		return 0
	}
	l.refill()
	deficit := n - l.tokens
	if deficit <= 0 {
		return 0
	}
	remainder := l.now().Sub(l.last) % l.interval
	wait := time.Duration(deficit)*l.interval - remainder
	if wait < 0 {
		wait = 0
	}
	return wait
}

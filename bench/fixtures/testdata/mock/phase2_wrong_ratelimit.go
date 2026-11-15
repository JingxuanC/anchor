package ratelimit

import "time"

// Limiter is a token-bucket rate limiter. The mock baseline agent's phase-2
// output: it only had a lossy chat summary, so it guessed the wait convention
// (plain deficit * interval) and guessed that an oversized request waits for the
// whole bucket. This is a plausible guess, and it is wrong for the conventions
// recorded during phase 1.
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

// WaitTime reports how long until n tokens are available. The baseline resume
// agent had no recorded rounding or oversized-request convention.
func (l *Limiter) WaitTime(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	l.refill()
	deficit := n - l.tokens
	if deficit <= 0 {
		return 0
	}
	return time.Duration(deficit) * l.interval
}

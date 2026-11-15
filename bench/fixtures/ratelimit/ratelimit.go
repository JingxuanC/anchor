// Package ratelimit implements a small, dependency-free token-bucket rate
// limiter.
//
// The file is a compiling stub: the acceptance tests in ratelimit_test.go fail
// until the API below is implemented.
package ratelimit

import "time"

// Limiter is a token-bucket rate limiter.
//
// A Limiter holds at most capacity tokens and refills one token per interval.
// All time reads must go through the injected now function.
type Limiter struct {
	capacity int
	interval time.Duration
	now      func() time.Time
}

// New returns a Limiter that starts full.
//
// now is the only clock the limiter may read; a nil now means time.Now.
func New(capacity int, interval time.Duration, now func() time.Time) *Limiter {
	return &Limiter{capacity: capacity, interval: interval, now: now}
}

// Allow reports whether n tokens could be taken. On success the tokens are
// consumed.
func (l *Limiter) Allow(n int) bool { return false }

// Tokens reports the current number of whole tokens.
func (l *Limiter) Tokens() int { return 0 }

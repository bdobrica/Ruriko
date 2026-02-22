package nlp

import (
	"sync"
	"time"
)

const (
	// DefaultRateLimit is the maximum number of NLP classification calls
	// allowed per sender per minute when no explicit limit is configured.
	DefaultRateLimit = 20

	// defaultRateLimitWindow is the sliding window duration.
	defaultRateLimitWindow = time.Minute
)

// RateLimiter enforces a per-sender sliding-window rate limit for NLP calls.
//
// Internally it holds the call timestamps for each sender within the current
// window and prunes stale entries on every Allow call.  This keeps memory
// bounded to O(limit) entries per active sender.
//
// RateLimiter is safe for concurrent use from multiple goroutines.
type RateLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	counters map[string][]time.Time // senderID → call timestamps in window
}

// NewRateLimiter returns a RateLimiter that allows at most limit calls per
// sender within window.
//
// If limit ≤ 0 it defaults to DefaultRateLimit.
// If window ≤ 0 it defaults to one minute.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = DefaultRateLimit
	}
	if window <= 0 {
		window = defaultRateLimitWindow
	}
	return &RateLimiter{
		limit:    limit,
		window:   window,
		counters: make(map[string][]time.Time),
	}
}

// Allow returns true when the sender is permitted to make another NLP call
// and records the current timestamp.  Returns false when the sender has
// exhausted their quota for the current window.
//
// The expected caller pattern is:
//
//	if !limiter.Allow(senderMXID) {
//	    return rateLimitMessage, nil
//	}
//	result, err := provider.Classify(ctx, req)
func (r *RateLimiter) Allow(senderID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)

	// Prune timestamps that have fallen outside the window.
	existing := r.counters[senderID]
	valid := existing[:0] // reuse backing array
	for _, t := range existing {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= r.limit {
		r.counters[senderID] = valid
		return false
	}

	r.counters[senderID] = append(valid, now)
	return true
}

// Remaining returns the number of calls the sender can still make within
// the current window.  A return value of 0 means the next Allow call will
// return false.
func (r *RateLimiter) Remaining(senderID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-r.window)
	count := 0
	for _, t := range r.counters[senderID] {
		if t.After(cutoff) {
			count++
		}
	}
	rem := r.limit - count
	if rem < 0 {
		return 0
	}
	return rem
}

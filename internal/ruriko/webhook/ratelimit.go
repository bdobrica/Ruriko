package webhook

import (
	"sync"
	"time"
)

// rateLimiter is a simple fixed-window rate limiter keyed by agent ID.
// Each agent has an independent counter that resets after window duration.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*windowBucket
}

type windowBucket struct {
	count   int
	resetAt time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*windowBucket),
	}
}

// Allow returns true if the agent is within its rate limit, false when exceeded.
// It is safe for concurrent use from multiple goroutines.
func (r *rateLimiter) Allow(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	b, ok := r.buckets[agentID]
	if !ok || now.After(b.resetAt) {
		r.buckets[agentID] = &windowBucket{count: 1, resetAt: now.Add(r.window)}
		return true
	}
	if b.count >= r.limit {
		return false
	}
	b.count++
	return true
}

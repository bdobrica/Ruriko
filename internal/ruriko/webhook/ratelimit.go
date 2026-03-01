package webhook

import (
	"time"

	"github.com/bdobrica/Ruriko/common/ratelimit"
)

// rateLimiter is a simple fixed-window rate limiter keyed by agent ID.
// Each agent has an independent counter that resets after window duration.
type rateLimiter struct {
	limit   int
	limiter *ratelimit.KeyedFixedWindow
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:   limit,
		limiter: ratelimit.NewKeyedFixedWindow(window),
	}
}

// Allow returns true if the agent is within its rate limit, false when exceeded.
// It is safe for concurrent use from multiple goroutines.
func (r *rateLimiter) Allow(agentID string) bool {
	return r.limiter.Allow(r.limit, agentID)
}

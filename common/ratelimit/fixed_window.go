// Package ratelimit provides shared in-memory rate limiting primitives.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	count   int
	resetAt time.Time
}

// KeyedFixedWindow enforces fixed-window limits per key.
//
// It is safe for concurrent use.
type KeyedFixedWindow struct {
	mu      sync.Mutex
	window  time.Duration
	buckets map[string]*bucket
}

// NewKeyedFixedWindow returns a keyed fixed-window limiter.
//
// If window <= 0, a default one-minute window is used.
func NewKeyedFixedWindow(window time.Duration) *KeyedFixedWindow {
	if window <= 0 {
		window = time.Minute
	}
	return &KeyedFixedWindow{
		window:  window,
		buckets: make(map[string]*bucket),
	}
}

// Allow checks and consumes one token for key within limit.
func (l *KeyedFixedWindow) Allow(limit int, key string) bool {
	return l.AllowAll(limit, key)
}

// AllowAll checks and consumes one token for all keys atomically.
//
// The call succeeds only if all keys have remaining capacity; on failure no
// key is incremented.
func (l *KeyedFixedWindow) AllowAll(limit int, keys ...string) bool {
	if limit <= 0 {
		return true
	}
	if len(keys) == 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		b, ok := l.buckets[key]
		if !ok {
			b = &bucket{}
			l.buckets[key] = b
		}
		if b.resetAt.IsZero() || now.After(b.resetAt) {
			b.count = 0
			b.resetAt = now.Add(l.window)
		}
		if b.count >= limit {
			return false
		}
	}

	for key := range seen {
		l.buckets[key].count++
	}

	return true
}

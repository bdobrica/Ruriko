// Package retry provides exponential-backoff retry logic for transient errors.
//
// Usage:
//
//	err := retry.Do(ctx, retry.Config{MaxAttempts: 3, InitialDelay: 500*time.Millisecond}, func() error {
//	    return client.Call()
//	})
package retry

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Config controls the retry behaviour.
type Config struct {
	// MaxAttempts is the total number of attempts (including the first).
	// Zero or negative values are treated as 1 (no retries).
	MaxAttempts int
	// InitialDelay is the wait before the second attempt.
	// Subsequent delays are doubled up to MaxDelay.
	InitialDelay time.Duration
	// MaxDelay caps the per-attempt wait.
	MaxDelay time.Duration
	// ShouldRetry is an optional predicate that lets callers classify errors
	// as retryable.  When nil, all non-nil errors are retried.
	ShouldRetry func(err error) bool
}

// DefaultConfig provides sensible defaults for short-lived network calls.
var DefaultConfig = Config{
	MaxAttempts:  3,
	InitialDelay: 500 * time.Millisecond,
	MaxDelay:     10 * time.Second,
}

// Do calls fn up to cfg.MaxAttempts times, backing off exponentially between
// attempts.  It stops early when ctx is cancelled or fn returns nil.
// The error from the last attempt is returned.
func Do(ctx context.Context, cfg Config, fn func() error) error {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	if cfg.InitialDelay <= 0 {
		cfg.InitialDelay = DefaultConfig.InitialDelay
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = DefaultConfig.MaxDelay
	}
	shouldRetry := cfg.ShouldRetry
	if shouldRetry == nil {
		shouldRetry = func(err error) bool { return true }
	}

	delay := cfg.InitialDelay
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return errors.Join(lastErr, err)
		}

		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		if !shouldRetry(lastErr) {
			return lastErr
		}

		if attempt < cfg.MaxAttempts {
			slog.Debug("retry: attempt failed, retrying",
				"attempt", attempt, "max", cfg.MaxAttempts,
				"err", lastErr, "delay", delay)

			select {
			case <-ctx.Done():
				return errors.Join(lastErr, ctx.Err())
			case <-time.After(delay):
			}

			delay *= 2
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			}
		}
	}

	return lastErr
}

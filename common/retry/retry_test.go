package retry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/retry"
)

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	err := retry.Do(context.Background(), retry.Config{MaxAttempts: 3, InitialDelay: time.Millisecond}, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestDo_RetriesOnFailure(t *testing.T) {
	calls := 0
	sentinel := errors.New("transient")
	err := retry.Do(context.Background(), retry.Config{MaxAttempts: 3, InitialDelay: time.Millisecond}, func() error {
		calls++
		if calls < 3 {
			return sentinel
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil after eventual success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_GivesUpAfterMaxAttempts(t *testing.T) {
	calls := 0
	sentinel := errors.New("permanent")
	err := retry.Do(context.Background(), retry.Config{MaxAttempts: 3, InitialDelay: time.Millisecond}, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_ShouldRetryPredicate(t *testing.T) {
	permanent := errors.New("permanent")
	calls := 0
	err := retry.Do(context.Background(), retry.Config{
		MaxAttempts:  3,
		InitialDelay: time.Millisecond,
		ShouldRetry:  func(err error) bool { return !errors.Is(err, permanent) },
	}, func() error {
		calls++
		return permanent
	})
	if !errors.Is(err, permanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retries for permanent error), got %d", calls)
	}
}

func TestDo_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	calls := 0
	_ = retry.Do(ctx, retry.Config{MaxAttempts: 5, InitialDelay: 10 * time.Millisecond}, func() error {
		calls++
		return errors.New("fail")
	})
	// Should not hang; at most 1 call before context is checked
	if calls > 2 {
		t.Fatalf("too many calls (%d) with cancelled context", calls)
	}
}

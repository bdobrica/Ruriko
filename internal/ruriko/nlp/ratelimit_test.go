package nlp_test

import (
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

func TestRateLimiter_AllowsUpToLimit(t *testing.T) {
	const limit = 5
	rl := nlp.NewRateLimiter(limit, time.Minute)

	for i := 0; i < limit; i++ {
		if !rl.Allow("@alice:example.com") {
			t.Fatalf("Allow returned false on call %d/%d (expected true)", i+1, limit)
		}
	}
}

func TestRateLimiter_RejectsWhenLimitExceeded(t *testing.T) {
	const limit = 3
	rl := nlp.NewRateLimiter(limit, time.Minute)

	for i := 0; i < limit; i++ {
		rl.Allow("@bob:example.com")
	}

	if rl.Allow("@bob:example.com") {
		t.Error("Allow returned true after limit was exhausted; expected false")
	}
}

func TestRateLimiter_IndependentPerSender(t *testing.T) {
	const limit = 2
	rl := nlp.NewRateLimiter(limit, time.Minute)

	// Exhaust alice's quota.
	rl.Allow("@alice:example.com")
	rl.Allow("@alice:example.com")
	if rl.Allow("@alice:example.com") {
		t.Error("alice should be rate-limited")
	}

	// Bob is independent and should still have his quota.
	if !rl.Allow("@bob:example.com") {
		t.Error("bob should not be rate-limited (independent sender)")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	// Use a very short window so the test can verify expiry without sleeping
	// for a full minute.
	const limit = 1
	window := 50 * time.Millisecond
	rl := nlp.NewRateLimiter(limit, window)

	if !rl.Allow("@carol:example.com") {
		t.Fatal("first call should be allowed")
	}
	if rl.Allow("@carol:example.com") {
		t.Fatal("second call within window should be rejected")
	}

	// Wait for the window to expire.
	time.Sleep(window + 10*time.Millisecond)

	if !rl.Allow("@carol:example.com") {
		t.Error("call after window expiry should be allowed again")
	}
}

func TestRateLimiter_DefaultLimit(t *testing.T) {
	// Zero values → defaults should apply (DefaultRateLimit = 20, 1 minute).
	rl := nlp.NewRateLimiter(0, 0)

	for i := 0; i < nlp.DefaultRateLimit; i++ {
		if !rl.Allow("@dave:example.com") {
			t.Fatalf("Allow returned false on call %d (default limit %d)", i+1, nlp.DefaultRateLimit)
		}
	}
	if rl.Allow("@dave:example.com") {
		t.Errorf("Allow returned true after default limit (%d) was exhausted", nlp.DefaultRateLimit)
	}
}

func TestRateLimiter_Remaining(t *testing.T) {
	const limit = 5
	rl := nlp.NewRateLimiter(limit, time.Minute)

	if got := rl.Remaining("@eve:example.com"); got != limit {
		t.Errorf("Remaining before any calls: got %d, want %d", got, limit)
	}

	rl.Allow("@eve:example.com")
	rl.Allow("@eve:example.com")

	if got := rl.Remaining("@eve:example.com"); got != limit-2 {
		t.Errorf("Remaining after 2 calls: got %d, want %d", got, limit-2)
	}
}

func TestRateLimiter_ConcurrentSafety(t *testing.T) {
	// Hammer the rate limiter from multiple goroutines to detect data races
	// when run with -race.
	const limit = 100
	rl := nlp.NewRateLimiter(limit, time.Minute)

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				rl.Allow("@shared:example.com")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}

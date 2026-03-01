package ratelimit

import (
	"testing"
	"time"
)

func TestKeyedFixedWindow_AllowWithinLimit(t *testing.T) {
	rl := NewKeyedFixedWindow(time.Minute)

	if !rl.Allow(2, "agent-1") {
		t.Fatal("first request should be allowed")
	}
	if !rl.Allow(2, "agent-1") {
		t.Fatal("second request should be allowed")
	}
	if rl.Allow(2, "agent-1") {
		t.Fatal("third request should be denied")
	}
}

func TestKeyedFixedWindow_IsPerKey(t *testing.T) {
	rl := NewKeyedFixedWindow(time.Minute)

	if !rl.Allow(1, "agent-1") {
		t.Fatal("agent-1 first request should be allowed")
	}
	if !rl.Allow(1, "agent-2") {
		t.Fatal("agent-2 first request should be allowed independently")
	}
}

func TestKeyedFixedWindow_ResetsAfterWindow(t *testing.T) {
	rl := NewKeyedFixedWindow(20 * time.Millisecond)

	if !rl.Allow(1, "agent-1") {
		t.Fatal("first request should be allowed")
	}
	if rl.Allow(1, "agent-1") {
		t.Fatal("second request in same window should be denied")
	}

	time.Sleep(25 * time.Millisecond)
	if !rl.Allow(1, "agent-1") {
		t.Fatal("request after window reset should be allowed")
	}
}

func TestKeyedFixedWindow_AllowAllAtomic(t *testing.T) {
	rl := NewKeyedFixedWindow(time.Minute)

	if !rl.AllowAll(1, "global", "source:a") {
		t.Fatal("first call should be allowed")
	}
	if rl.AllowAll(1, "global", "source:b") {
		t.Fatal("second call should be denied by global key")
	}

	// source:b should not be consumed by the failed call.
	if !rl.Allow(1, "source:b") {
		t.Fatal("source:b should still allow first request after failed atomic call")
	}
}

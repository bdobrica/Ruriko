package nlp

import (
	"sync"
	"time"
)

const (
	// DefaultTokenBudget is the maximum number of LLM tokens allowed per
	// sender per UTC day when no explicit budget is configured.
	// 50 000 tokens/day is sufficient for ~100 moderate classification calls
	// (gpt-4o-mini) while keeping costs low.
	DefaultTokenBudget = 50_000
)

// TokenBudget enforces a per-sender daily token budget for LLM classification
// calls.
//
// The counter for each sender resets at midnight UTC.  Callers should:
//  1. Call Allow before issuing a Classify request — returns false when the
//     sender has already exhausted today's allocation.
//  2. Call RecordUsage after a successful Classify call to update the counter.
//
// TokenBudget is safe for concurrent use.
type TokenBudget struct {
	mu     sync.Mutex
	budget int
	usage  map[string]*senderDailyUsage
}

// senderDailyUsage tracks cumulative token consumption for one sender within
// the current UTC day.
type senderDailyUsage struct {
	tokens  int
	resetAt time.Time // next midnight UTC
}

// NewTokenBudget returns a TokenBudget that allows at most dailyBudget tokens
// per sender per UTC day.
//
// If dailyBudget ≤ 0 it defaults to DefaultTokenBudget.
func NewTokenBudget(dailyBudget int) *TokenBudget {
	if dailyBudget <= 0 {
		dailyBudget = DefaultTokenBudget
	}
	return &TokenBudget{
		budget: dailyBudget,
		usage:  make(map[string]*senderDailyUsage),
	}
}

// Budget returns the configured daily token limit per sender.
func (tb *TokenBudget) Budget() int {
	return tb.budget
}

// Allow returns true when senderID has not yet exhausted their daily token
// budget.  It does NOT consume any tokens — call RecordUsage after a
// successful Classify call to record actual usage.
func (tb *TokenBudget) Allow(senderID string) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.resetIfNeeded(senderID)

	u := tb.usage[senderID]
	if u == nil {
		return true
	}
	return u.tokens < tb.budget
}

// RecordUsage adds tokens to senderID's running daily total.  If this is the
// first call for the sender today a new counter is initialised.
func (tb *TokenBudget) RecordUsage(senderID string, tokens int) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.resetIfNeeded(senderID)

	u := tb.usage[senderID]
	if u == nil {
		u = &senderDailyUsage{resetAt: nextMidnightUTC()}
		tb.usage[senderID] = u
	}
	u.tokens += tokens
}

// Remaining returns the number of tokens senderID may still consume today.
// Returns 0 when the budget is exhausted.
func (tb *TokenBudget) Remaining(senderID string) int {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.resetIfNeeded(senderID)

	u := tb.usage[senderID]
	if u == nil {
		return tb.budget
	}
	if rem := tb.budget - u.tokens; rem > 0 {
		return rem
	}
	return 0
}

// Used returns the total tokens senderID has consumed today.
func (tb *TokenBudget) Used(senderID string) int {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.resetIfNeeded(senderID)

	u := tb.usage[senderID]
	if u == nil {
		return 0
	}
	return u.tokens
}

// resetIfNeeded deletes the senderID entry when the UTC calendar day has
// rolled over.  Must be called with tb.mu held.
func (tb *TokenBudget) resetIfNeeded(senderID string) {
	u := tb.usage[senderID]
	if u == nil {
		return
	}
	if time.Now().UTC().After(u.resetAt) {
		delete(tb.usage, senderID)
	}
}

// nextMidnightUTC returns the time of midnight UTC at the start of the next
// calendar day.
func nextMidnightUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- mock embedder that returns a real vector (enables LTM retrieval) --------

type contextMockEmbedder struct {
	vec []float32
	err error
}

func (m *contextMockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.vec, m.err
}

// --- mock LTM that returns canned search results ----------------------------

type contextMockLTM struct {
	entries []MemoryEntry
	err     error
	// stored tracks Store calls for verification.
	stored []MemoryEntry
}

func (m *contextMockLTM) Store(_ context.Context, entry MemoryEntry) error {
	m.stored = append(m.stored, entry)
	return nil
}

func (m *contextMockLTM) Search(_ context.Context, _ string, _ string, _ string, _ int) ([]MemoryEntry, error) {
	return m.entries, m.err
}

// --- Tests ------------------------------------------------------------------

func TestContextAssembler_FullSTMBuffer(t *testing.T) {
	// Verify that Assemble includes the full STM buffer when within budget.
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 50,
		MaxTokens:   8000,
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "hello there", now)
	tracker.recordMessageAt("!room:test", "@alice:test", "assistant", "hi, how can I help?", now.Add(1*time.Minute))
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "show me my agents", now.Add(2*time.Minute))

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       NewNoopLTM(nil),
		Embedder:  NoopEmbedder{},
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "show me my agents")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (full STM buffer), got %d", len(msgs))
	}

	// Verify ordering and content.
	if msgs[0].Role != "user" || msgs[0].Content != "hello there" {
		t.Errorf("msg[0]: expected user/hello there, got %s/%s", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi, how can I help?" {
		t.Errorf("msg[1]: expected assistant/hi how can I help?, got %s/%s", msgs[1].Role, msgs[1].Content)
	}
	if msgs[2].Role != "user" || msgs[2].Content != "show me my agents" {
		t.Errorf("msg[2]: expected user/show me my agents, got %s/%s", msgs[2].Role, msgs[2].Content)
	}
}

func TestContextAssembler_LTMResultsWhenEmbedderAvailable(t *testing.T) {
	// Verify that LTM results are included when the embedder produces a real vector.
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 50,
		MaxTokens:   8000,
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "current message", now)

	ltm := &contextMockLTM{
		entries: []MemoryEntry{
			{
				ConversationID: "conv-old-1",
				Summary:        "discussed provisioning saito agent",
				SealedAt:       time.Date(2026, 2, 20, 14, 0, 0, 0, time.UTC),
			},
			{
				ConversationID: "conv-old-2",
				Summary:        "configured kumo search agent",
				SealedAt:       time.Date(2026, 2, 22, 9, 0, 0, 0, time.UTC),
			},
		},
	}

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       ltm,
		Embedder:  &contextMockEmbedder{vec: []float32{0.1, 0.2, 0.3}},
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "current message")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	// Expect 2 LTM entries + 1 STM message = 3.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (2 LTM + 1 STM), got %d", len(msgs))
	}

	// LTM entries come first (system role).
	if msgs[0].Role != "system" {
		t.Errorf("msg[0]: expected system role, got %s", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "discussed provisioning saito agent") {
		t.Errorf("msg[0]: expected LTM summary, got %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "2026-02-20") {
		t.Errorf("msg[0]: expected date in LTM context, got %q", msgs[0].Content)
	}

	if msgs[1].Role != "system" {
		t.Errorf("msg[1]: expected system role, got %s", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "configured kumo search agent") {
		t.Errorf("msg[1]: expected LTM summary, got %q", msgs[1].Content)
	}

	// STM comes last.
	if msgs[2].Role != "user" || msgs[2].Content != "current message" {
		t.Errorf("msg[2]: expected user/current message, got %s/%s", msgs[2].Role, msgs[2].Content)
	}
}

func TestContextAssembler_TokenBudgetRespected_STMPrioritised(t *testing.T) {
	// Verify that when the budget is tight, STM has priority and LTM is trimmed.
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 50,
		MaxTokens:   100000, // tracker won't trim
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	// Add 3 STM messages.
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "first message", now)
	tracker.recordMessageAt("!room:test", "@alice:test", "assistant", "first reply", now.Add(1*time.Minute))
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "second message", now.Add(2*time.Minute))

	// STM is ~3 messages × ~8 tokens each ≈ 24 tokens.
	// Set budget to 30 tokens — enough for STM but not LTM.
	ltm := &contextMockLTM{
		entries: []MemoryEntry{
			{
				ConversationID: "conv-old-1",
				Summary:        "a long summary about provisioning agents and configuring secrets that should not fit in the budget",
				SealedAt:       time.Date(2026, 2, 20, 14, 0, 0, 0, time.UTC),
			},
		},
	}

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       ltm,
		Embedder:  &contextMockEmbedder{vec: []float32{0.1, 0.2}},
		MaxTokens: 30, // very tight budget
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "second message")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	// STM should be present; LTM may be excluded due to budget.
	hasSTM := false
	hasLTM := false
	for _, m := range msgs {
		if m.Role == "system" {
			hasLTM = true
		} else {
			hasSTM = true
		}
	}
	if !hasSTM {
		t.Error("expected STM messages to be included (STM has priority)")
	}

	// The total estimated tokens should be within the budget.
	totalTokens := estimateTokens(msgs)
	if totalTokens > 30 {
		t.Errorf("expected total tokens ≤ 30, got %d (LTM_present=%v)", totalTokens, hasLTM)
	}
}

func TestContextAssembler_NoopEmbedderSkipsLTM(t *testing.T) {
	// Verify that noop embedder means no LTM retrieval.
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 50,
		MaxTokens:   8000,
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "hello", now)

	// Mock LTM that would return results if searched.
	ltm := &contextMockLTM{
		entries: []MemoryEntry{
			{
				ConversationID: "conv-old-1",
				Summary:        "should not appear",
				SealedAt:       time.Date(2026, 2, 20, 14, 0, 0, 0, time.UTC),
			},
		},
	}

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       ltm,
		Embedder:  NoopEmbedder{}, // noop → nil vector → skip LTM
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "hello")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	// Should only contain STM messages — no LTM (system role) entries.
	for _, m := range msgs {
		if m.Role == "system" {
			t.Errorf("expected no system/LTM messages with noop embedder, got: %q", m.Content)
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 STM message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("expected 'hello', got %q", msgs[0].Content)
	}
}

func TestContextAssembler_EmptyConversation(t *testing.T) {
	// Verify that Assemble returns nil when there is no conversation history.
	tracker := NewTracker(DefaultTrackerConfig())

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       NewNoopLTM(nil),
		Embedder:  NoopEmbedder{},
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "hello")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil for empty conversation, got %d messages", len(msgs))
	}
}

func TestContextAssembler_NilSTM(t *testing.T) {
	// Verify that Assemble works gracefully when STM is nil.
	assembler := &ContextAssembler{
		STM:       nil,
		LTM:       NewNoopLTM(nil),
		Embedder:  NoopEmbedder{},
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "hello")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil when STM is nil, got %d messages", len(msgs))
	}
}

func TestContextAssembler_NilLTM(t *testing.T) {
	// Verify that Assemble works when LTM is nil (only STM).
	tracker := NewTracker(DefaultTrackerConfig())

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "just STM", now)

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       nil,
		Embedder:  NoopEmbedder{},
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "just STM")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 STM message, got %d", len(msgs))
	}
	if msgs[0].Content != "just STM" {
		t.Errorf("expected 'just STM', got %q", msgs[0].Content)
	}
}

func TestContextAssembler_EmbedderError(t *testing.T) {
	// Verify that an embedder error does not prevent STM from being returned.
	tracker := NewTracker(DefaultTrackerConfig())

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "survived error", now)

	failEmbed := &contextMockEmbedder{err: fmt.Errorf("embed failed")}
	ltm := &contextMockLTM{
		entries: []MemoryEntry{
			{Summary: "should not appear", SealedAt: time.Now()},
		},
	}

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       ltm,
		Embedder:  failEmbed,
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "survived error")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	// STM should still be present despite embedder failure.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 STM message, got %d", len(msgs))
	}
	if msgs[0].Content != "survived error" {
		t.Errorf("expected 'survived error', got %q", msgs[0].Content)
	}
}

func TestContextAssembler_LTMSearchError(t *testing.T) {
	// Verify that a LTM search error does not prevent STM from being returned.
	tracker := NewTracker(DefaultTrackerConfig())

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "search failed", now)

	ltm := &contextMockLTM{err: fmt.Errorf("ltm search failed")}

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       ltm,
		Embedder:  &contextMockEmbedder{vec: []float32{0.5}},
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "search failed")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	// STM should still be present despite LTM search failure.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 STM message, got %d", len(msgs))
	}
}

func TestContextAssembler_LTMEntriesWithEmptySummarySkipped(t *testing.T) {
	// Verify that LTM entries with empty summaries are not injected.
	tracker := NewTracker(DefaultTrackerConfig())

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "hello", now)

	ltm := &contextMockLTM{
		entries: []MemoryEntry{
			{Summary: "", SealedAt: time.Now()},             // empty summary — skip
			{Summary: "real summary", SealedAt: time.Now()}, // non-empty — include
		},
	}

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       ltm,
		Embedder:  &contextMockEmbedder{vec: []float32{0.1}},
		MaxTokens: 4000,
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "hello")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	// 1 LTM (non-empty summary) + 1 STM = 2.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "real summary") {
		t.Errorf("msg[0]: expected LTM with 'real summary', got %s/%q", msgs[0].Role, msgs[0].Content)
	}
}

func TestContextAssembler_STMTrimmedWhenOverBudget(t *testing.T) {
	// Verify that when STM alone exceeds the assembler's budget,
	// it is trimmed (oldest messages dropped).
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 100,   // tracker won't trim
		MaxTokens:   50000, // tracker won't trim
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// Add many messages to create a large STM buffer.
	for i := range 20 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		tracker.recordMessageAt("!room:test", "@alice:test", role,
			strings.Repeat("word ", 20), // ~100 chars ≈ ~29 tokens per msg
			now.Add(time.Duration(i)*time.Second))
	}

	assembler := &ContextAssembler{
		STM:       tracker,
		LTM:       NewNoopLTM(nil),
		Embedder:  NoopEmbedder{},
		MaxTokens: 50, // very tight — only ~1-2 messages worth
	}

	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "latest")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}

	if len(msgs) == 0 {
		t.Fatal("expected at least 1 message to be retained")
	}
	if len(msgs) >= 20 {
		t.Errorf("expected trimmed STM, got all %d messages", len(msgs))
	}

	// Total tokens should be within budget (or close — at least 1 message retained).
	totalTokens := estimateTokens(msgs)
	// Allow slight overshoot since at least one message is always retained.
	if totalTokens > 50 && len(msgs) > 1 {
		t.Errorf("expected tokens ≤ 50 (or single remaining msg), got %d tokens from %d messages",
			totalTokens, len(msgs))
	}
}

func TestContextAssembler_DefaultValues(t *testing.T) {
	// Verify that zero-value MaxTokens and LTMTopK use defaults.
	assembler := &ContextAssembler{
		STM:       nil,
		LTM:       nil,
		Embedder:  nil,
		MaxTokens: 0, // should default to DefaultMaxTokens
		LTMTopK:   0, // should default to DefaultLTMTopK
	}

	// Just verify it doesn't panic and returns nil for empty context.
	msgs, err := assembler.Assemble(context.Background(), "!room:test", "@alice:test", "test")
	if err != nil {
		t.Fatalf("Assemble() returned unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil, got %d messages", len(msgs))
	}
}

func TestTrimToTokenBudget(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: strings.Repeat("a", 100)},      // ~29 tokens
		{Role: "assistant", Content: strings.Repeat("b", 100)}, // ~29 tokens
		{Role: "user", Content: strings.Repeat("c", 100)},      // ~29 tokens
		{Role: "assistant", Content: strings.Repeat("d", 100)}, // ~29 tokens
	}

	// Budget of 40 tokens — should keep the last 1 message.
	trimmed := trimToTokenBudget(msgs, 40)
	if len(trimmed) > 2 {
		t.Errorf("expected ≤ 2 messages after trimming to 40 token budget, got %d", len(trimmed))
	}
	if len(trimmed) < 1 {
		t.Error("expected at least 1 message to be retained")
	}

	// Budget of 0 — should keep at least 1 message.
	trimmed = trimToTokenBudget(msgs, 0)
	if len(trimmed) != 1 {
		t.Errorf("expected 1 message with 0 budget, got %d", len(trimmed))
	}
}

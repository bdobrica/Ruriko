package memory

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

// --- Mock implementations for testing ----------------------------------------

// mockSummariser records calls and returns a configurable summary.
type mockSummariser struct {
	mu       sync.Mutex
	calls    [][]Message
	summary  string
	err      error
}

func (m *mockSummariser) Summarise(_ context.Context, msgs []Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, msgs)
	return m.summary, m.err
}

func (m *mockSummariser) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// mockEmbedder records calls and returns a configurable embedding.
type mockEmbedder struct {
	mu        sync.Mutex
	calls     []string
	embedding []float32
	err       error
}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, text)
	return m.embedding, m.err
}

func (m *mockEmbedder) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// sealMockLTM records stored entries and supports configurable search results.
type sealMockLTM struct {
	mu      sync.Mutex
	entries []MemoryEntry
	err     error
}

func (m *sealMockLTM) Store(_ context.Context, entry MemoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, entry)
	return nil
}

func (m *sealMockLTM) Search(_ context.Context, _ string, _ string, _ string, _ int) ([]MemoryEntry, error) {
	return nil, nil
}

func (m *sealMockLTM) storedEntries() []MemoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]MemoryEntry, len(m.entries))
	copy(cp, m.entries)
	return cp
}

// testLogger returns a slog.Logger that writes to testing output.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// makeTestConversation creates a Conversation for testing.
func makeTestConversation(id, roomID, senderID string, msgs []Message) Conversation {
	start := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)
	lastMsg := start
	if len(msgs) > 0 {
		lastMsg = msgs[len(msgs)-1].Timestamp
	}
	return Conversation{
		ID:        id,
		RoomID:    roomID,
		SenderID:  senderID,
		Messages:  msgs,
		StartedAt: start,
		LastMsgAt: lastMsg,
		Sealed:    true,
	}
}

// --- SealPipeline tests ------------------------------------------------------

func TestSealPipeline_FullFlow(t *testing.T) {
	// Verifies that a sealed conversation flows through summarise → embed → store.
	summariser := &mockSummariser{summary: "User discussed project plans."}
	embedder := &mockEmbedder{embedding: []float32{0.1, 0.2, 0.3}}
	ltm := &sealMockLTM{}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	msgs := []Message{
		{Role: "user", Content: "Let's plan the project", Timestamp: time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)},
		{Role: "assistant", Content: "Sure, what are the goals?", Timestamp: time.Date(2026, 2, 24, 10, 1, 0, 0, time.UTC)},
		{Role: "user", Content: "Build the memory system", Timestamp: time.Date(2026, 2, 24, 10, 2, 0, 0, time.UTC)},
	}
	conv := makeTestConversation("conv-001", "!room:test", "@alice:test", msgs)

	err := pipeline.Seal(context.Background(), conv)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Summariser should be called once with the conversation messages.
	if summariser.callCount() != 1 {
		t.Errorf("expected 1 summarise call, got %d", summariser.callCount())
	}

	// Embedder should be called once with the summary text.
	if embedder.callCount() != 1 {
		t.Errorf("expected 1 embed call, got %d", embedder.callCount())
	}
	embedder.mu.Lock()
	if embedder.calls[0] != "User discussed project plans." {
		t.Errorf("expected embed input to be the summary, got %q", embedder.calls[0])
	}
	embedder.mu.Unlock()

	// LTM should have one stored entry.
	entries := ltm.storedEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.ConversationID != "conv-001" {
		t.Errorf("expected conversation ID 'conv-001', got %q", entry.ConversationID)
	}
	if entry.RoomID != "!room:test" {
		t.Errorf("expected room ID '!room:test', got %q", entry.RoomID)
	}
	if entry.SenderID != "@alice:test" {
		t.Errorf("expected sender ID '@alice:test', got %q", entry.SenderID)
	}
	if entry.Summary != "User discussed project plans." {
		t.Errorf("expected summary 'User discussed project plans.', got %q", entry.Summary)
	}
	if len(entry.Embedding) != 3 {
		t.Errorf("expected 3-dimensional embedding, got %d", len(entry.Embedding))
	}
	if len(entry.Messages) != 3 {
		t.Errorf("expected 3 messages in entry, got %d", len(entry.Messages))
	}
}

func TestSealPipeline_NoopBackends(t *testing.T) {
	// Verifies that the pipeline completes without errors when using noop backends.
	summariser := NoopSummariser{}
	embedder := NoopEmbedder{}
	ltm := NewNoopLTM(testLogger(t))
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	msgs := []Message{
		{Role: "user", Content: "Hello", Timestamp: time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)},
		{Role: "assistant", Content: "Hi there!", Timestamp: time.Date(2026, 2, 24, 10, 1, 0, 0, time.UTC)},
	}
	conv := makeTestConversation("conv-noop", "!room:test", "@bob:test", msgs)

	err := pipeline.Seal(context.Background(), conv)
	if err != nil {
		t.Fatalf("expected no error with noop backends, got: %v", err)
	}
}

func TestSealPipeline_SummariserError_ContinuesWithEmptySummary(t *testing.T) {
	// When summarisation fails, the pipeline should continue with an empty summary.
	summariser := &mockSummariser{err: fmt.Errorf("summarisation service unavailable")}
	embedder := &mockEmbedder{embedding: []float32{0.5}}
	ltm := &sealMockLTM{}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	msgs := []Message{
		{Role: "user", Content: "test", Timestamp: time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)},
	}
	conv := makeTestConversation("conv-err-sum", "!room:test", "@alice:test", msgs)

	err := pipeline.Seal(context.Background(), conv)
	if err != nil {
		t.Fatalf("expected no error (summariser failure should not block), got: %v", err)
	}

	// Embedder should NOT be called (empty summary → skip embedding).
	if embedder.callCount() != 0 {
		t.Errorf("expected 0 embed calls (empty summary), got %d", embedder.callCount())
	}

	// LTM should still have a stored entry (with empty summary).
	entries := ltm.storedEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(entries))
	}
	if entries[0].Summary != "" {
		t.Errorf("expected empty summary, got %q", entries[0].Summary)
	}
}

func TestSealPipeline_EmbedderError_ContinuesWithNilEmbedding(t *testing.T) {
	// When embedding fails, the pipeline should continue with a nil embedding.
	summariser := &mockSummariser{summary: "A conversation happened."}
	embedder := &mockEmbedder{err: fmt.Errorf("embedding service unavailable")}
	ltm := &sealMockLTM{}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	msgs := []Message{
		{Role: "user", Content: "test", Timestamp: time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)},
	}
	conv := makeTestConversation("conv-err-emb", "!room:test", "@alice:test", msgs)

	err := pipeline.Seal(context.Background(), conv)
	if err != nil {
		t.Fatalf("expected no error (embedder failure should not block), got: %v", err)
	}

	entries := ltm.storedEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(entries))
	}
	if entries[0].Summary != "A conversation happened." {
		t.Errorf("expected summary preserved, got %q", entries[0].Summary)
	}
	if entries[0].Embedding != nil {
		t.Errorf("expected nil embedding, got %v", entries[0].Embedding)
	}
}

func TestSealPipeline_LTMStoreError_ReturnsError(t *testing.T) {
	// When LTM storage fails, the pipeline should return an error.
	summariser := &mockSummariser{summary: "Summary."}
	embedder := &mockEmbedder{embedding: []float32{0.1}}
	ltm := &sealMockLTM{err: fmt.Errorf("database unavailable")}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	msgs := []Message{
		{Role: "user", Content: "test", Timestamp: time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)},
	}
	conv := makeTestConversation("conv-err-ltm", "!room:test", "@alice:test", msgs)

	err := pipeline.Seal(context.Background(), conv)
	if err == nil {
		t.Fatal("expected error from LTM storage failure")
	}
}

func TestSealPipeline_EmptyConversation(t *testing.T) {
	// Empty conversations should be processed without error.
	summariser := &mockSummariser{summary: ""}
	embedder := &mockEmbedder{}
	ltm := &sealMockLTM{}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	conv := makeTestConversation("conv-empty", "!room:test", "@alice:test", nil)

	err := pipeline.Seal(context.Background(), conv)
	if err != nil {
		t.Fatalf("expected no error for empty conversation, got: %v", err)
	}

	entries := ltm.storedEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(entries))
	}
}

// --- SealPipelineRunner tests ------------------------------------------------

func TestSealPipelineRunner_ProcessSealed(t *testing.T) {
	// Verifies that ProcessSealed sends conversations through the pipeline.
	summariser := &mockSummariser{summary: "Batch summary."}
	embedder := &mockEmbedder{embedding: []float32{0.9}}
	ltm := &sealMockLTM{}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	tracker := NewTracker(DefaultTrackerConfig())
	runner := NewSealPipelineRunner(tracker, pipeline, 60*time.Second, testLogger(t))

	convs := []Conversation{
		makeTestConversation("batch-1", "!room1:test", "@alice:test", []Message{
			{Role: "user", Content: "hello", Timestamp: time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)},
		}),
		makeTestConversation("batch-2", "!room2:test", "@bob:test", []Message{
			{Role: "user", Content: "world", Timestamp: time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)},
		}),
	}

	runner.ProcessSealed(context.Background(), convs)

	entries := ltm.storedEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 stored entries, got %d", len(entries))
	}
	if entries[0].ConversationID != "batch-1" {
		t.Errorf("expected first entry ID 'batch-1', got %q", entries[0].ConversationID)
	}
	if entries[1].ConversationID != "batch-2" {
		t.Errorf("expected second entry ID 'batch-2', got %q", entries[1].ConversationID)
	}
}

func TestSealPipelineRunner_TimerTriggersProcessing(t *testing.T) {
	// Verifies that the runner's timer-based seal check processes expired conversations.
	summariser := &mockSummariser{summary: "Timer summary."}
	embedder := &mockEmbedder{embedding: []float32{0.5}}
	ltm := &sealMockLTM{}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	tracker := NewTracker(TrackerConfig{
		Cooldown:    100 * time.Millisecond, // very short cooldown for testing
		MaxMessages: 50,
		MaxTokens:   8000,
	})

	// Record messages that will expire quickly.
	tracker.RecordMessage("!room:test", "@alice:test", "user", "first message")

	// Use a short interval so the timer fires quickly in tests.
	runner := NewSealPipelineRunner(tracker, pipeline, 50*time.Millisecond, testLogger(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	// Wait for the cooldown to expire and the timer to fire.
	time.Sleep(500 * time.Millisecond)
	cancel()

	// The conversation should have been sealed and processed.
	entries := ltm.storedEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored entry from timer-based seal, got %d", len(entries))
	}
	if entries[0].RoomID != "!room:test" {
		t.Errorf("expected room '!room:test', got %q", entries[0].RoomID)
	}

	// The tracker should no longer have an active conversation.
	if c := tracker.GetActiveConversation("!room:test", "@alice:test"); c != nil {
		t.Error("expected no active conversation after timer-based seal")
	}
}

func TestSealPipelineRunner_StopIsIdempotent(t *testing.T) {
	tracker := NewTracker(DefaultTrackerConfig())
	pipeline := NewSealPipeline(NoopSummariser{}, NoopEmbedder{}, NewNoopLTM(nil), nil)
	runner := NewSealPipelineRunner(tracker, pipeline, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())

	go runner.Run(ctx)
	time.Sleep(10 * time.Millisecond) // let the goroutine start

	// Stop multiple times — should not panic.
	runner.Stop()
	runner.Stop()
	cancel()
}

func TestSealPipelineRunner_DefaultInterval(t *testing.T) {
	tracker := NewTracker(DefaultTrackerConfig())
	pipeline := NewSealPipeline(NoopSummariser{}, NoopEmbedder{}, NewNoopLTM(nil), nil)

	// Pass 0 interval — should default to 60 seconds.
	runner := NewSealPipelineRunner(tracker, pipeline, 0, nil)
	if runner.interval != 60*time.Second {
		t.Errorf("expected default interval 60s, got %v", runner.interval)
	}
}

// --- Integration: RecordMessage → seal lazy → pipeline -----------------------

func TestSealPipeline_LazyPathViaSealedReturn(t *testing.T) {
	// Simulates the lazy seal path: RecordMessage detects a stale conversation,
	// returns it as sealed, and the caller runs it through ProcessSealed.
	summariser := &mockSummariser{summary: "Lazy path summary."}
	embedder := &mockEmbedder{embedding: []float32{0.7}}
	ltm := &sealMockLTM{}
	pipeline := NewSealPipeline(summariser, embedder, ltm, testLogger(t))

	tracker := NewTracker(TrackerConfig{
		Cooldown:    10 * time.Minute,
		MaxMessages: 50,
		MaxTokens:   8000,
	})
	runner := NewSealPipelineRunner(tracker, pipeline, time.Hour, testLogger(t))

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// Record first message.
	tracker.recordMessageAt("!room:test", "@alice:test", "user", "hello", now)

	// After cooldown, record new message — this seals the old conversation.
	_, sealed := tracker.recordMessageAt("!room:test", "@alice:test", "user", "new topic", now.Add(20*time.Minute))
	if len(sealed) != 1 {
		t.Fatalf("expected 1 sealed conversation, got %d", len(sealed))
	}

	// Process sealed conversations through the pipeline.
	runner.ProcessSealed(context.Background(), sealed)

	entries := ltm.storedEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(entries))
	}
	if entries[0].ConversationID != sealed[0].ID {
		t.Errorf("expected conversation ID %q, got %q", sealed[0].ID, entries[0].ConversationID)
	}
}

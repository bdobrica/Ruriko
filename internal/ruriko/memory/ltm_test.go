package memory

import (
	"context"
	"testing"
	"time"
)

func TestNoopLTM_SatisfiesInterface(t *testing.T) {
	var ltm LongTermMemory = NewNoopLTM(nil)
	if ltm == nil {
		t.Fatal("expected non-nil LongTermMemory")
	}
}

func TestNoopLTM_StoreDoesNotError(t *testing.T) {
	ltm := NewNoopLTM(nil)

	entry := MemoryEntry{
		ConversationID: "conv-123",
		RoomID:         "!room:test",
		SenderID:       "@alice:test",
		Summary:        "discussed agent provisioning",
		Embedding:      []float32{0.1, 0.2, 0.3},
		Messages: []Message{
			{Role: "user", Content: "set up saito", Timestamp: time.Now()},
			{Role: "assistant", Content: "done", Timestamp: time.Now()},
		},
		SealedAt: time.Now(),
		Metadata: map[string]string{"template": "saito-agent"},
	}

	err := ltm.Store(context.Background(), entry)
	if err != nil {
		t.Fatalf("Store() returned unexpected error: %v", err)
	}
}

func TestNoopLTM_SearchReturnsEmpty(t *testing.T) {
	ltm := NewNoopLTM(nil)

	results, err := ltm.Search(context.Background(), "saito", "!room:test", "@alice:test", 3)
	if err != nil {
		t.Fatalf("Search() returned unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d entries", len(results))
	}
}

func TestNoopLTM_StoreMultipleEntriesDoesNotError(t *testing.T) {
	ltm := NewNoopLTM(nil)

	for i := range 5 {
		entry := MemoryEntry{
			ConversationID: "conv-" + string(rune('a'+i)),
			RoomID:         "!room:test",
			SenderID:       "@alice:test",
			Summary:        "conversation " + string(rune('a'+i)),
			SealedAt:       time.Now(),
		}
		if err := ltm.Store(context.Background(), entry); err != nil {
			t.Fatalf("Store() call %d returned unexpected error: %v", i, err)
		}
	}
}

// mockLTM verifies that the LongTermMemory interface is mockable
// by downstream tests via a simple recording mock.
type mockLTM struct {
	stored   []MemoryEntry
	searchFn func(ctx context.Context, query, roomID, senderID string, topK int) ([]MemoryEntry, error)
}

func (m *mockLTM) Store(_ context.Context, entry MemoryEntry) error {
	m.stored = append(m.stored, entry)
	return nil
}

func (m *mockLTM) Search(ctx context.Context, query, roomID, senderID string, topK int) ([]MemoryEntry, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, query, roomID, senderID, topK)
	}
	return nil, nil
}

func TestMockLTM_SatisfiesInterface(t *testing.T) {
	var ltm LongTermMemory = &mockLTM{}
	if ltm == nil {
		t.Fatal("expected non-nil LongTermMemory")
	}
}

func TestMockLTM_RecordsStores(t *testing.T) {
	m := &mockLTM{}
	var ltm LongTermMemory = m

	entry := MemoryEntry{
		ConversationID: "conv-1",
		RoomID:         "!room:test",
		SenderID:       "@alice:test",
		Summary:        "test summary",
		SealedAt:       time.Now(),
	}
	if err := ltm.Store(context.Background(), entry); err != nil {
		t.Fatalf("Store() returned unexpected error: %v", err)
	}
	if len(m.stored) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(m.stored))
	}
	if m.stored[0].ConversationID != "conv-1" {
		t.Errorf("expected conversation ID 'conv-1', got %q", m.stored[0].ConversationID)
	}
}

func TestMockLTM_CustomSearchFunction(t *testing.T) {
	want := []MemoryEntry{
		{ConversationID: "conv-old", Summary: "relevant past conversation"},
	}
	m := &mockLTM{
		searchFn: func(_ context.Context, _, _, _ string, _ int) ([]MemoryEntry, error) {
			return want, nil
		},
	}

	results, err := m.Search(context.Background(), "query", "!room:test", "@alice:test", 3)
	if err != nil {
		t.Fatalf("Search() returned unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ConversationID != "conv-old" {
		t.Errorf("expected conversation ID 'conv-old', got %q", results[0].ConversationID)
	}
}

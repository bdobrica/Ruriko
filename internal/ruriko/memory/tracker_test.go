package memory
package memory

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		wantMin  int
		wantMax  int
	}{
		{
			name:     "empty",
			messages: nil,
			wantMin:  0,
			wantMax:  0,
		},
		{
			name: "single short message",
			messages: []Message{
				{Role: "user", Content: "hello"},
			},
			// 5 chars / 4 ≈ 1 + 4 overhead = 5
			wantMin: 4,
			wantMax: 10,
		},
		{
			name: "multiple messages",
			messages: []Message{
				{Role: "user", Content: "hello world this is a test"},
				{Role: "assistant", Content: "I understand your request"},
			},
			wantMin: 10,
			wantMax: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.messages)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("estimateTokens() = %d, want between %d and %d", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestTracker_ContiguousMessages(t *testing.T) {
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 50,
		MaxTokens:   8000,
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// First message starts a conversation.
	id1, sealed := tracker.recordMessageAt("!room:test", "@alice:test", "user", "hello", now)
	if len(sealed) != 0 {
		t.Fatalf("expected no sealed conversations, got %d", len(sealed))
	}
	if id1 == "" {
		t.Fatal("expected non-empty conversation ID")
	}

	// Second message within cooldown appends to the same conversation.
	id2, sealed := tracker.recordMessageAt("!room:test", "@alice:test", "assistant", "hi there", now.Add(1*time.Minute))
	if len(sealed) != 0 {
		t.Fatalf("expected no sealed conversations, got %d", len(sealed))
	}
	if id2 != id1 {
		t.Errorf("expected same conversation ID %q, got %q", id1, id2)
	}

	// Third message still contiguous.
	id3, sealed := tracker.recordMessageAt("!room:test", "@alice:test", "user", "how are you?", now.Add(5*time.Minute))
	if len(sealed) != 0 {
		t.Fatalf("expected no sealed conversations, got %d", len(sealed))
	}
	if id3 != id1 {
		t.Errorf("expected same conversation ID %q, got %q", id1, id3)
	}

	// Verify conversation state.
	conv := tracker.GetActiveConversation("!room:test", "@alice:test")
	if conv == nil {
		t.Fatal("expected active conversation")
	}
	if len(conv.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(conv.Messages))
	}
	if conv.Messages[0].Content != "hello" {
		t.Errorf("expected first message 'hello', got %q", conv.Messages[0].Content)
	}
	if conv.Messages[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %q", conv.Messages[1].Role)
	}
}

func TestTracker_CooldownSealAndNewConversation(t *testing.T) {
	cooldown := 15 * time.Minute
	tracker := NewTracker(TrackerConfig{
		Cooldown:    cooldown,
		MaxMessages: 50,
		MaxTokens:   8000,
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// Start a conversation.
	id1, _ := tracker.recordMessageAt("!room:test", "@alice:test", "user", "first conversation", now)

	// After cooldown, a new message should seal the old conversation.
	id2, sealed := tracker.recordMessageAt("!room:test", "@alice:test", "user", "second conversation", now.Add(20*time.Minute))
	if len(sealed) != 1 {
		t.Fatalf("expected 1 sealed conversation, got %d", len(sealed))
	}
	if sealed[0].ID != id1 {
		t.Errorf("expected sealed conversation ID %q, got %q", id1, sealed[0].ID)
	}
	if !sealed[0].Sealed {
		t.Error("expected sealed conversation to have Sealed=true")
	}
	if len(sealed[0].Messages) != 1 {
		t.Errorf("expected 1 message in sealed conversation, got %d", len(sealed[0].Messages))
	}
	if id2 == id1 {
		t.Error("expected new conversation to have a different ID")
	}

	// New conversation should have only the new message.
	conv := tracker.GetActiveConversation("!room:test", "@alice:test")
	if conv == nil {
		t.Fatal("expected active conversation")
	}
	if len(conv.Messages) != 1 {
		t.Errorf("expected 1 message in new conversation, got %d", len(conv.Messages))
	}
	if conv.Messages[0].Content != "second conversation" {
		t.Errorf("expected message 'second conversation', got %q", conv.Messages[0].Content)
	}
}

func TestTracker_SealExpired(t *testing.T) {
	cooldown := 10 * time.Minute
	tracker := NewTracker(TrackerConfig{
		Cooldown:    cooldown,
		MaxMessages: 50,
		MaxTokens:   8000,
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// Start two conversations in different rooms.
	tracker.recordMessageAt("!room1:test", "@alice:test", "user", "msg1", now)
	tracker.recordMessageAt("!room2:test", "@bob:test", "user", "msg2", now.Add(5*time.Minute))

	// SealExpired at now+12 min — only room1 should be sealed (12 min old).
	sealed := tracker.SealExpired(now.Add(12 * time.Minute))
	if len(sealed) != 1 {
		t.Fatalf("expected 1 sealed conversation, got %d", len(sealed))
	}
	if sealed[0].RoomID != "!room1:test" {
		t.Errorf("expected sealed room '!room1:test', got %q", sealed[0].RoomID)
	}

	// room1 should no longer have an active conversation.
	if c := tracker.GetActiveConversation("!room1:test", "@alice:test"); c != nil {
		t.Error("expected no active conversation for room1 after seal")
	}

	// room2 should still be active.
	if c := tracker.GetActiveConversation("!room2:test", "@bob:test"); c == nil {
		t.Error("expected active conversation for room2")
	}

	// SealExpired at now+20 min — room2 should now be sealed.
	sealed = tracker.SealExpired(now.Add(20 * time.Minute))
	if len(sealed) != 1 {
		t.Fatalf("expected 1 sealed conversation, got %d", len(sealed))
	}
	if sealed[0].RoomID != "!room2:test" {
		t.Errorf("expected sealed room '!room2:test', got %q", sealed[0].RoomID)
	}
}

func TestTracker_BufferLimitMessages(t *testing.T) {
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 5,
		MaxTokens:   100000, // large enough to not interfere
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// Add 8 messages.
	for i := range 8 {
		tracker.recordMessageAt("!room:test", "@alice:test", "user", "msg", now.Add(time.Duration(i)*time.Second))
	}

	conv := tracker.GetActiveConversation("!room:test", "@alice:test")
	if conv == nil {
		t.Fatal("expected active conversation")
	}
	if len(conv.Messages) != 5 {
		t.Errorf("expected 5 messages after limit enforcement, got %d", len(conv.Messages))
	}

	// The oldest 3 messages should have been dropped — remaining messages
	// should be the last 5 (indices 3–7 of the original 8).
	if conv.Messages[0].Timestamp != now.Add(3*time.Second) {
		t.Errorf("expected first remaining message at t+3s, got %v", conv.Messages[0].Timestamp)
	}
}

func TestTracker_BufferLimitTokens(t *testing.T) {
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 1000,   // large enough to not interfere
		MaxTokens:   50,     // very tight token budget
	})

	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// Each message: 100 chars ≈ 25 tokens + 4 overhead = ~29 tokens.
	longContent := strings.Repeat("a", 100)

	// Add 5 messages — should significantly exceed the 50-token budget.
	for i := range 5 {
		tracker.recordMessageAt("!room:test", "@alice:test", "user", longContent, now.Add(time.Duration(i)*time.Second))
	}

	conv := tracker.GetActiveConversation("!room:test", "@alice:test")
	if conv == nil {
		t.Fatal("expected active conversation")
	}

	// With ~29 tokens per message and 50 token budget, should keep at most 1-2 messages.
	// (enforceBufferLimits keeps at least 1 message)
	if len(conv.Messages) > 2 {
		t.Errorf("expected ≤2 messages after token limit enforcement, got %d (est. %d tokens)",
			len(conv.Messages), estimateTokens(conv.Messages))
	}
	if len(conv.Messages) < 1 {
		t.Error("expected at least 1 message to be retained")
	}
}

func TestTracker_DifferentRoomsSeparateConversations(t *testing.T) {
	tracker := NewTracker(DefaultTrackerConfig())
	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	id1, _ := tracker.recordMessageAt("!room1:test", "@alice:test", "user", "room1 msg", now)
	id2, _ := tracker.recordMessageAt("!room2:test", "@alice:test", "user", "room2 msg", now)

	if id1 == id2 {
		t.Error("expected different conversation IDs for different rooms")
	}

	c1 := tracker.GetActiveConversation("!room1:test", "@alice:test")
	c2 := tracker.GetActiveConversation("!room2:test", "@alice:test")

	if c1 == nil || c2 == nil {
		t.Fatal("expected active conversations in both rooms")
	}
	if c1.Messages[0].Content != "room1 msg" {
		t.Errorf("room1 has wrong content: %q", c1.Messages[0].Content)
	}
	if c2.Messages[0].Content != "room2 msg" {
		t.Errorf("room2 has wrong content: %q", c2.Messages[0].Content)
	}
}

func TestTracker_GetActiveConversation_ReturnsNilForUnknown(t *testing.T) {
	tracker := NewTracker(DefaultTrackerConfig())

	if c := tracker.GetActiveConversation("!unknown:test", "@nobody:test"); c != nil {
		t.Error("expected nil for unknown room+sender")
	}
}

func TestTracker_GetActiveConversation_ReturnsSnapshot(t *testing.T) {
	tracker := NewTracker(DefaultTrackerConfig())
	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	tracker.recordMessageAt("!room:test", "@alice:test", "user", "hello", now)

	// Get a snapshot and mutate it.
	snap := tracker.GetActiveConversation("!room:test", "@alice:test")
	snap.Messages = append(snap.Messages, Message{Role: "user", Content: "mutated"})

	// Original should be unaffected.
	conv := tracker.GetActiveConversation("!room:test", "@alice:test")
	if len(conv.Messages) != 1 {
		t.Errorf("expected 1 message (snapshot mutation should not affect tracker), got %d", len(conv.Messages))
	}
}

func TestTracker_DefaultConfig(t *testing.T) {
	cfg := DefaultTrackerConfig()
	if cfg.Cooldown != 15*time.Minute {
		t.Errorf("expected default cooldown 15m, got %v", cfg.Cooldown)
	}
	if cfg.MaxMessages != 50 {
		t.Errorf("expected default max messages 50, got %d", cfg.MaxMessages)
	}
	if cfg.MaxTokens != 8000 {
		t.Errorf("expected default max tokens 8000, got %d", cfg.MaxTokens)
	}
}

func TestTracker_InvalidConfigUsesDefaults(t *testing.T) {
	tracker := NewTracker(TrackerConfig{
		Cooldown:    -1,
		MaxMessages: 0,
		MaxTokens:   -100,
	})

	defaults := DefaultTrackerConfig()
	if tracker.config.Cooldown != defaults.Cooldown {
		t.Errorf("expected default cooldown, got %v", tracker.config.Cooldown)
	}
	if tracker.config.MaxMessages != defaults.MaxMessages {
		t.Errorf("expected default max messages, got %d", tracker.config.MaxMessages)
	}
	if tracker.config.MaxTokens != defaults.MaxTokens {
		t.Errorf("expected default max tokens, got %d", tracker.config.MaxTokens)
	}
}

func TestTracker_ConcurrentAccess(t *testing.T) {
	tracker := NewTracker(TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 100,
		MaxTokens:   100000,
	})

	var wg sync.WaitGroup
	now := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

	// 10 goroutines, each writing 100 messages to the same conversation.
	for g := range 10 {
		wg.Add(1)
		go func(goroutine int) {
			defer wg.Done()
			for i := range 100 {
				offset := time.Duration(goroutine*100+i) * time.Millisecond
				tracker.recordMessageAt("!room:test", "@alice:test", "user", "msg", now.Add(offset))
			}
		}(g)
	}

	// Also read concurrently.
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				tracker.GetActiveConversation("!room:test", "@alice:test")
			}
		}()
	}

	wg.Wait()

	// Verify conversation exists and is not corrupted.
	conv := tracker.GetActiveConversation("!room:test", "@alice:test")
	if conv == nil {
		t.Fatal("expected active conversation after concurrent writes")
	}
	if len(conv.Messages) == 0 {
		t.Error("expected messages after concurrent writes")
	}
	// MaxMessages is 100, so we should have exactly 100 (trimmed from 1000).
	if len(conv.Messages) != 100 {
		t.Errorf("expected 100 messages (capped), got %d", len(conv.Messages))
	}
}

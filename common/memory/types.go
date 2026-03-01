package memory

import "time"

// Conversation represents an active or sealed conversation between an agent
// and a specific user in a specific room.
type Conversation struct {
	ID        string
	RoomID    string
	SenderID  string
	Messages  []Message
	StartedAt time.Time
	LastMsgAt time.Time
	Sealed    bool
}

// Message is a single turn in a conversation.
type Message struct {
	Role      string
	Content   string
	Timestamp time.Time
}

// MemoryEntry is a sealed conversation ready for long-term storage.
type MemoryEntry struct {
	ConversationID string
	RoomID         string
	SenderID       string
	Summary        string
	Embedding      []float32
	Messages       []Message
	SealedAt       time.Time
	Metadata       map[string]string
}

// EstimateTokens returns a rough token count for a message slice.
func EstimateTokens(msgs []Message) int {
	const charsPerToken = 4
	const perMessageOverhead = 4

	total := 0
	for _, m := range msgs {
		total += len(m.Content)/charsPerToken + perMessageOverhead
	}
	return total
}

// TrimToTokenBudget drops oldest messages until the estimated token count is
// within budget. It always retains at least one message when input is non-empty.
func TrimToTokenBudget(msgs []Message, budget int) []Message {
	for len(msgs) > 1 && EstimateTokens(msgs) > budget {
		msgs = msgs[1:]
	}
	return msgs
}

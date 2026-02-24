// Package memory implements short-term and long-term conversation memory
// for Ruriko's natural language interface. Short-term memory keeps the
// current conversation in full fidelity; long-term memory stores sealed
// conversations as embeddings+summaries for fuzzy recall.
package memory

import "time"

// Conversation represents an active or sealed conversation between Ruriko
// and a specific user in a specific room. Messages accumulate in the
// short-term buffer until the conversation is sealed (cooldown expires).
type Conversation struct {
	ID        string    // unique conversation ID (UUID)
	RoomID    string    // Matrix room where the conversation is happening
	SenderID  string    // Matrix user ID of the human participant
	Messages  []Message // ordered message buffer (oldest first)
	StartedAt time.Time // when the first message was recorded
	LastMsgAt time.Time // when the most recent message was recorded
	Sealed    bool      // true once the cooldown period expires
}

// Message is a single turn in a conversation.
type Message struct {
	Role      string    // "user" or "assistant"
	Content   string    // message text
	Timestamp time.Time // when this message was recorded
}

// estimateTokens returns a rough token count for a message slice.
// Uses ~4 characters per token (common English heuristic) plus a small
// per-message overhead for role framing. This is intentionally imprecise —
// the budget is a soft limit to keep the context window bounded.
func estimateTokens(msgs []Message) int {
	const charsPerToken = 4
	const perMessageOverhead = 4 // role label, delimiters

	total := 0
	for _, m := range msgs {
		total += len(m.Content)/charsPerToken + perMessageOverhead
	}
	return total
}

package memory

import "context"

// ConversationProvider is the minimal short-term memory surface needed by the
// context assembler.
type ConversationProvider interface {
	GetActiveConversation(roomID, senderID string) *Conversation
}

// Embedder produces vector embeddings for text.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Summariser produces concise summaries of conversation transcripts.
type Summariser interface {
	Summarise(ctx context.Context, messages []Message) (string, error)
}

// LongTermMemory persists and retrieves sealed conversations.
type LongTermMemory interface {
	Store(ctx context.Context, entry MemoryEntry) error
	Search(ctx context.Context, query, roomID, senderID string, topK int) ([]MemoryEntry, error)
}

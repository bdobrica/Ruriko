package memory

import (
	"context"
	"time"
)

// LongTermMemory is the pluggable interface for persisting and retrieving
// sealed conversations. Implementations range from a no-op stub (default)
// to SQLite with cosine similarity or pgvector for production deployments.
type LongTermMemory interface {
	// Store persists a sealed conversation with its embedding and summary.
	Store(ctx context.Context, entry MemoryEntry) error

	// Search finds the top-k most relevant past conversations for the query.
	// Results are scoped to the given room and sender. Implementations that
	// do not support embedding-based search may return an empty slice.
	Search(ctx context.Context, query string, roomID string, senderID string, topK int) ([]MemoryEntry, error)
}

// MemoryEntry is a sealed conversation ready for long-term storage.
// It carries the original messages, a human-readable summary, and an
// optional embedding vector for similarity search.
type MemoryEntry struct {
	ConversationID string            // unique conversation ID (matches Conversation.ID)
	RoomID         string            // Matrix room where the conversation happened
	SenderID       string            // Matrix user ID of the human participant
	Summary        string            // human-readable summary of the conversation
	Embedding      []float32         // vector embedding of the summary (nil if embedder is noop)
	Messages       []Message         // optional: full transcript for high-fidelity recall
	SealedAt       time.Time         // when the conversation was sealed
	Metadata       map[string]string // extensible: template used, agents mentioned, etc.
}

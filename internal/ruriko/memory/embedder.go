package memory

import "context"

// Embedder produces vector embeddings for text. Implementations range from
// a no-op stub (default) to OpenAI's text-embedding-3-small for production use.
// When the embedder is no-op, long-term memory similarity search is disabled.
type Embedder interface {
	// Embed produces a vector embedding for the given text.
	// Returns nil with no error when embedding is not available (noop).
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Summariser produces concise summaries of conversation transcripts.
// Summaries are stored alongside embeddings in long-term memory and
// injected into future LLM context windows as fuzzy recall.
type Summariser interface {
	// Summarise produces a concise summary of a conversation transcript.
	Summarise(ctx context.Context, messages []Message) (string, error)
}

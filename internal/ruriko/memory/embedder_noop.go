package memory

import (
	"context"
	"fmt"
)

// NoopEmbedder is a stub Embedder that returns nil vectors. When wired as
// the active embedder, long-term memory similarity search is effectively
// disabled — no embeddings means no semantic matching.
type NoopEmbedder struct{}

// Embed returns nil with no error, signalling that embedding is unavailable.
func (NoopEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

// Compile-time interface satisfaction check.
var _ Embedder = NoopEmbedder{}

// NoopSummariser is a stub Summariser that concatenates the last 3 messages
// in the conversation. Crude but functional — gives downstream consumers
// something human-readable without requiring an LLM call.
type NoopSummariser struct{}

// Summarise returns a concatenation of up to the last 3 messages, formatted
// as "role: content" lines. Returns an empty string for empty input.
func (NoopSummariser) Summarise(_ context.Context, messages []Message) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	// Take at most the last 3 messages.
	start := len(messages) - 3
	if start < 0 {
		start = 0
	}
	tail := messages[start:]

	var summary string
	for i, m := range tail {
		if i > 0 {
			summary += "\n"
		}
		summary += fmt.Sprintf("%s: %s", m.Role, m.Content)
	}
	return summary, nil
}

// Compile-time interface satisfaction check.
var _ Summariser = NoopSummariser{}

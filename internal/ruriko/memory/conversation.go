// Package memory implements short-term and long-term conversation memory
// for Ruriko's natural language interface. Short-term memory keeps the
// current conversation in full fidelity; long-term memory stores sealed
// conversations as embeddings+summaries for fuzzy recall.
package memory

import commonmemory "github.com/bdobrica/Ruriko/common/memory"

type Conversation = commonmemory.Conversation

type Message = commonmemory.Message

func estimateTokens(msgs []Message) int {
	return commonmemory.EstimateTokens(msgs)
}

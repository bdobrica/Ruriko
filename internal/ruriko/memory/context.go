package memory

import commonmemory "github.com/bdobrica/Ruriko/common/memory"

type ContextAssembler = commonmemory.ContextAssembler

const DefaultMaxTokens = commonmemory.DefaultMaxTokens

const DefaultLTMTopK = commonmemory.DefaultLTMTopK

func trimToTokenBudget(msgs []Message, budget int) []Message {
	return commonmemory.TrimToTokenBudget(msgs, budget)
}

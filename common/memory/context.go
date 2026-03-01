package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ContextAssembler builds memory context from short-term and long-term memory.
type ContextAssembler struct {
	STM       ConversationProvider
	LTM       LongTermMemory
	Embedder  Embedder
	MaxTokens int
	LTMTopK   int
}

// DefaultMaxTokens is the default token budget for assembled memory context.
const DefaultMaxTokens = 4000

// DefaultLTMTopK is the default number of LTM entries to retrieve.
const DefaultLTMTopK = 3

// Assemble returns memory messages ordered as LTM context first, then STM.
func (a *ContextAssembler) Assemble(ctx context.Context, roomID, senderID, currentMsg string) ([]Message, error) {
	maxTokens := a.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	topK := a.LTMTopK
	if topK <= 0 {
		topK = DefaultLTMTopK
	}

	var stmMessages []Message
	if a.STM != nil {
		if conv := a.STM.GetActiveConversation(roomID, senderID); conv != nil {
			stmMessages = conv.Messages
		}
	}
	stmTokens := EstimateTokens(stmMessages)

	var ltmMessages []Message
	if a.Embedder != nil && a.LTM != nil {
		ltmMessages = a.retrieveLTM(ctx, currentMsg, roomID, senderID, topK)
	}

	return a.assembleWithBudget(stmMessages, stmTokens, ltmMessages, maxTokens), nil
}

func (a *ContextAssembler) retrieveLTM(ctx context.Context, currentMsg, roomID, senderID string, topK int) []Message {
	vec, err := a.Embedder.Embed(ctx, currentMsg)
	if err != nil {
		slog.Warn("memory: failed to embed current message for LTM search",
			"err", err,
			"room_id", roomID,
			"sender_id", senderID,
		)
		return nil
	}

	if vec == nil {
		return nil
	}

	entries, err := a.LTM.Search(ctx, currentMsg, roomID, senderID, topK)
	if err != nil {
		slog.Warn("memory: LTM search failed",
			"err", err,
			"room_id", roomID,
			"sender_id", senderID,
		)
		return nil
	}

	if len(entries) == 0 {
		return nil
	}

	msgs := make([]Message, 0, len(entries))
	for _, entry := range entries {
		if entry.Summary == "" {
			continue
		}
		content := fmt.Sprintf("Previous relevant conversation (from %s): %s",
			entry.SealedAt.Format(time.DateOnly), entry.Summary)
		msgs = append(msgs, Message{Role: "system", Content: content})
	}
	return msgs
}

func (a *ContextAssembler) assembleWithBudget(stm []Message, stmTokens int, ltm []Message, maxTokens int) []Message {
	if len(stm) == 0 && len(ltm) == 0 {
		return nil
	}

	trimmedSTM := stm
	if stmTokens > maxTokens && len(stm) > 1 {
		trimmedSTM = TrimToTokenBudget(stm, maxTokens)
		stmTokens = EstimateTokens(trimmedSTM)
	}

	remaining := maxTokens - stmTokens
	if remaining < 0 {
		remaining = 0
	}

	var budgetedLTM []Message
	ltmUsed := 0
	for _, m := range ltm {
		cost := EstimateTokens([]Message{m})
		if ltmUsed+cost > remaining {
			break
		}
		budgetedLTM = append(budgetedLTM, m)
		ltmUsed += cost
	}

	result := make([]Message, 0, len(budgetedLTM)+len(trimmedSTM))
	result = append(result, budgetedLTM...)
	result = append(result, trimmedSTM...)

	if len(result) == 0 {
		return nil
	}
	return result
}

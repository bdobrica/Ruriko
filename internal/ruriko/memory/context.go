package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ContextAssembler builds the conversation-memory block that is injected into
// the LLM prompt before classification. It combines sharp short-term recall
// (the full active conversation buffer) with fuzzy long-term recall (embedding-
// based search of sealed past conversations).
//
// Assembly strategy:
//  1. Get the active short-term conversation → include all messages.
//  2. If embedder produces a non-nil vector for the current message:
//     a. Search LTM for the top-K most relevant past conversations.
//     b. Format retrieved summaries as system-context messages.
//  3. If embedder is noop (nil vector) → skip LTM retrieval entirely.
//  4. Respect the MaxTokens budget: STM has priority; LTM fills remaining space.
type ContextAssembler struct {
	STM       *ConversationTracker
	LTM       LongTermMemory
	Embedder  Embedder
	MaxTokens int // total token budget for the memory block
	LTMTopK   int // max number of LTM entries to retrieve (default: 3)
}

// DefaultMaxTokens is the default token budget for the assembled memory block.
const DefaultMaxTokens = 4000

// DefaultLTMTopK is the default number of LTM entries to retrieve.
const DefaultLTMTopK = 3

// Assemble produces the memory block to inject into the LLM prompt.
//
// The returned slice contains messages ordered for the LLM context window:
//   - LTM entries first (system-role context about past conversations)
//   - STM messages second (user/assistant turns from the active conversation)
//
// STM always has priority over LTM when the token budget is tight. When both
// STM and LTM are empty, the returned slice is nil.
func (a *ContextAssembler) Assemble(ctx context.Context, roomID, senderID, currentMsg string) ([]Message, error) {
	maxTokens := a.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	topK := a.LTMTopK
	if topK <= 0 {
		topK = DefaultLTMTopK
	}

	// --- 1. Short-term memory: full active conversation buffer ---------------
	var stmMessages []Message
	if a.STM != nil {
		if conv := a.STM.GetActiveConversation(roomID, senderID); conv != nil {
			stmMessages = conv.Messages
		}
	}

	stmTokens := estimateTokens(stmMessages)

	// --- 2. Long-term memory: embedding-based search -------------------------
	var ltmMessages []Message
	if a.Embedder != nil && a.LTM != nil {
		ltmMessages = a.retrieveLTM(ctx, currentMsg, roomID, senderID, topK)
	}

	// --- 3. Assemble within budget (STM priority) ----------------------------
	return a.assembleWithBudget(stmMessages, stmTokens, ltmMessages, maxTokens), nil
}

// retrieveLTM attempts to embed the current message and search LTM for
// relevant past conversations. Returns nil on any failure or when the
// embedder is noop (returns nil vector).
func (a *ContextAssembler) retrieveLTM(ctx context.Context, currentMsg, roomID, senderID string, topK int) []Message {
	// Embed the current message to use as a search query.
	vec, err := a.Embedder.Embed(ctx, currentMsg)
	if err != nil {
		slog.Warn("memory: failed to embed current message for LTM search",
			"err", err,
			"room_id", roomID,
			"sender_id", senderID,
		)
		return nil
	}

	// Noop embedder returns nil → skip LTM search.
	if vec == nil {
		return nil
	}

	// Search LTM for relevant past conversations.
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

	// Convert LTM entries to system-context messages.
	msgs := make([]Message, 0, len(entries))
	for _, entry := range entries {
		if entry.Summary == "" {
			continue
		}
		content := fmt.Sprintf("Previous relevant conversation (from %s): %s",
			entry.SealedAt.Format(time.DateOnly), entry.Summary)
		msgs = append(msgs, Message{
			Role:    "system",
			Content: content,
		})
	}
	return msgs
}

// assembleWithBudget combines STM and LTM messages within the token budget.
// STM always has priority: it is included first up to the budget, then LTM
// fills whatever space remains.
func (a *ContextAssembler) assembleWithBudget(stm []Message, stmTokens int, ltm []Message, maxTokens int) []Message {
	if len(stm) == 0 && len(ltm) == 0 {
		return nil
	}

	// If STM alone exceeds the budget, trim it (oldest first) and skip LTM.
	// The tracker already enforces its own limits, but the assembler applies
	// an independent budget that may be tighter.
	trimmedSTM := stm
	if stmTokens > maxTokens && len(stm) > 1 {
		trimmedSTM = trimToTokenBudget(stm, maxTokens)
		stmTokens = estimateTokens(trimmedSTM)
	}

	remaining := maxTokens - stmTokens
	if remaining < 0 {
		remaining = 0
	}

	// Allocate LTM entries into remaining budget.
	var budgetedLTM []Message
	ltmUsed := 0
	for _, m := range ltm {
		cost := estimateTokens([]Message{m})
		if ltmUsed+cost > remaining {
			break
		}
		budgetedLTM = append(budgetedLTM, m)
		ltmUsed += cost
	}

	// LTM context comes first (background knowledge), then STM (active thread).
	result := make([]Message, 0, len(budgetedLTM)+len(trimmedSTM))
	result = append(result, budgetedLTM...)
	result = append(result, trimmedSTM...)

	if len(result) == 0 {
		return nil
	}
	return result
}

// trimToTokenBudget drops the oldest messages from msgs until the estimated
// token count is within budget. Always retains at least one message.
func trimToTokenBudget(msgs []Message, budget int) []Message {
	for len(msgs) > 1 && estimateTokens(msgs) > budget {
		msgs = msgs[1:]
	}
	return msgs
}

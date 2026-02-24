package memory

import (
	"context"
	"log/slog"
)

// NoopLTM is a no-op implementation of LongTermMemory. It discards stored
// entries and returns empty search results. This is the default backend
// until a real embedding/persistence layer is wired in.
type NoopLTM struct {
	logger *slog.Logger
}

// NewNoopLTM creates a NoopLTM that logs discarded entries at DEBUG level.
// If logger is nil, the default slog logger is used.
func NewNoopLTM(logger *slog.Logger) *NoopLTM {
	if logger == nil {
		logger = slog.Default()
	}
	return &NoopLTM{logger: logger}
}

// Store logs the conversation summary at DEBUG level and discards the entry.
func (n *NoopLTM) Store(_ context.Context, entry MemoryEntry) error {
	n.logger.Debug("ltm noop: discarding sealed conversation",
		"conversation_id", entry.ConversationID,
		"room_id", entry.RoomID,
		"sender_id", entry.SenderID,
		"messages", len(entry.Messages),
		"summary_len", len(entry.Summary),
	)
	return nil
}

// Search always returns an empty slice — no persistence means no recall.
func (n *NoopLTM) Search(_ context.Context, _ string, _ string, _ string, _ int) ([]MemoryEntry, error) {
	return nil, nil
}

// Compile-time interface satisfaction check.
var _ LongTermMemory = (*NoopLTM)(nil)

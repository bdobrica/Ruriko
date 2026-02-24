package memory

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// TrackerConfig holds configuration for the ConversationTracker.
type TrackerConfig struct {
	// Cooldown is the duration of inactivity after which a conversation
	// is considered stale and will be sealed on the next SealExpired call.
	// Default: 15 minutes.
	Cooldown time.Duration

	// MaxMessages is the maximum number of messages kept in the short-term
	// buffer. When exceeded, the oldest messages are dropped (sliding window).
	// Default: 50.
	MaxMessages int

	// MaxTokens is the estimated token budget for the short-term buffer.
	// When exceeded, the oldest messages are dropped until under budget.
	// Default: 8000.
	MaxTokens int
}

// DefaultTrackerConfig returns a TrackerConfig with the documented defaults.
func DefaultTrackerConfig() TrackerConfig {
	return TrackerConfig{
		Cooldown:    15 * time.Minute,
		MaxMessages: 50,
		MaxTokens:   8000,
	}
}

// ConversationTracker manages the lifecycle of active conversations.
// It is safe for concurrent use.
type ConversationTracker struct {
	mu     sync.Mutex
	config TrackerConfig
	convos map[string]*Conversation // key: roomID + ":" + senderID
}

// NewTracker creates a ConversationTracker with the given configuration.
func NewTracker(cfg TrackerConfig) *ConversationTracker {
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultTrackerConfig().Cooldown
	}
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = DefaultTrackerConfig().MaxMessages
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultTrackerConfig().MaxTokens
	}
	return &ConversationTracker{
		config: cfg,
		convos: make(map[string]*Conversation),
	}
}

// RecordMessage appends a message to the active conversation for the given
// room+sender pair. If the previous conversation has gone stale (past the
// cooldown threshold), it is sealed and a new conversation is started.
//
// Returns the conversation ID of the conversation the message was appended to,
// and any conversations that were sealed as a side-effect. The caller should
// pass sealed conversations to the long-term memory pipeline.
func (t *ConversationTracker) RecordMessage(roomID, senderID, role, content string) (conversationID string, sealed []Conversation) {
	now := time.Now()
	return t.recordMessageAt(roomID, senderID, role, content, now)
}

// recordMessageAt is the time-injectable core of RecordMessage (for testing).
func (t *ConversationTracker) recordMessageAt(roomID, senderID, role, content string, now time.Time) (string, []Conversation) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := sessionKey(roomID, senderID)
	var sealed []Conversation

	existing := t.convos[key]
	if existing != nil && !existing.Sealed && now.Sub(existing.LastMsgAt) > t.config.Cooldown {
		// Conversation has gone stale — seal it.
		existing.Sealed = true
		sealed = append(sealed, *existing)
		existing = nil
	}

	if existing == nil || existing.Sealed {
		// Start a new conversation.
		existing = &Conversation{
			ID:        uuid.New().String(),
			RoomID:    roomID,
			SenderID:  senderID,
			StartedAt: now,
		}
		t.convos[key] = existing
	}

	existing.Messages = append(existing.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: now,
	})
	existing.LastMsgAt = now

	t.enforceBufferLimits(existing)

	return existing.ID, sealed
}

// GetActiveConversation returns a snapshot of the active (unsealed) conversation
// for the given room+sender. Returns nil if there is no active conversation.
// The returned Conversation is a copy — mutations do not affect the tracker.
func (t *ConversationTracker) GetActiveConversation(roomID, senderID string) *Conversation {
	t.mu.Lock()
	defer t.mu.Unlock()

	c := t.convos[sessionKey(roomID, senderID)]
	if c == nil || c.Sealed {
		return nil
	}
	return t.snapshot(c)
}

// SealExpired seals all conversations whose last message is older than the
// cooldown threshold relative to now. Returns the sealed conversations so the
// caller can pass them to the long-term memory pipeline.
func (t *ConversationTracker) SealExpired(now time.Time) []Conversation {
	t.mu.Lock()
	defer t.mu.Unlock()

	var sealed []Conversation
	for key, c := range t.convos {
		if c.Sealed {
			continue
		}
		if now.Sub(c.LastMsgAt) > t.config.Cooldown {
			c.Sealed = true
			sealed = append(sealed, *c)
			delete(t.convos, key)
		}
	}
	return sealed
}

// enforceBufferLimits trims the message buffer to stay within configured
// limits. Oldest messages are dropped first. Must be called with mu held.
func (t *ConversationTracker) enforceBufferLimits(c *Conversation) {
	// Enforce max messages.
	if len(c.Messages) > t.config.MaxMessages {
		excess := len(c.Messages) - t.config.MaxMessages
		c.Messages = c.Messages[excess:]
	}

	// Enforce max tokens — drop oldest messages until under budget.
	for len(c.Messages) > 1 && estimateTokens(c.Messages) > t.config.MaxTokens {
		c.Messages = c.Messages[1:]
	}
}

// snapshot returns a deep copy of a conversation.
func (t *ConversationTracker) snapshot(c *Conversation) *Conversation {
	cp := *c
	cp.Messages = make([]Message, len(c.Messages))
	copy(cp.Messages, c.Messages)
	return &cp
}

// sessionKey produces the map key for a room+sender pair.
func sessionKey(roomID, senderID string) string {
	return roomID + ":" + senderID
}

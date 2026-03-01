package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	commonmemory "github.com/bdobrica/Ruriko/common/memory"
)

type gitaiNoopEmbedder struct{}

func (gitaiNoopEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, nil
}

type gitaiNoopLTM struct{}

func (gitaiNoopLTM) Store(context.Context, commonmemory.MemoryEntry) error {
	return nil
}

func (gitaiNoopLTM) Search(context.Context, string, string, string, int) ([]commonmemory.MemoryEntry, error) {
	return nil, nil
}

type gitaiMemorySTM struct {
	mu          sync.Mutex
	convos      map[string]*commonmemory.Conversation
	maxMessages int
}

func newGitaiMemorySTM(maxMessages int) *gitaiMemorySTM {
	if maxMessages <= 0 {
		maxMessages = 50
	}
	return &gitaiMemorySTM{
		convos:      make(map[string]*commonmemory.Conversation),
		maxMessages: maxMessages,
	}
}

func (s *gitaiMemorySTM) RecordMessage(roomID, senderID, role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := memorySessionKey(roomID, senderID)
	conv := s.convos[key]
	now := time.Now()
	if conv == nil {
		conv = &commonmemory.Conversation{
			ID:        fmt.Sprintf("%s:%s", roomID, senderID),
			RoomID:    roomID,
			SenderID:  senderID,
			StartedAt: now,
		}
		s.convos[key] = conv
	}
	conv.Messages = append(conv.Messages, commonmemory.Message{Role: role, Content: content, Timestamp: now})
	if len(conv.Messages) > s.maxMessages {
		excess := len(conv.Messages) - s.maxMessages
		conv.Messages = conv.Messages[excess:]
	}
	conv.LastMsgAt = now
}

func (s *gitaiMemorySTM) GetActiveConversation(roomID, senderID string) *commonmemory.Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()

	conv := s.convos[memorySessionKey(roomID, senderID)]
	if conv == nil {
		return nil
	}
	cp := *conv
	cp.Messages = make([]commonmemory.Message, len(conv.Messages))
	copy(cp.Messages, conv.Messages)
	return &cp
}

func memorySessionKey(roomID, senderID string) string {
	return roomID + ":" + senderID
}

func formatMemoryContext(messages []commonmemory.Message) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	for i, msg := range messages {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(msg.Role)
		b.WriteString(": ")
		b.WriteString(msg.Content)
	}
	return b.String()
}

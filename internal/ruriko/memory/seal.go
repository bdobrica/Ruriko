package memory

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// SealPipeline orchestrates the conversation seal and archive flow:
// summarise → embed → store in LTM. It is invoked whenever a conversation
// is sealed (either lazily on the next message arrival or by the periodic
// seal-check timer).
//
// The pipeline tolerates noop backends gracefully — when the summariser,
// embedder, or LTM are stubs, the pipeline runs to completion without error
// but produces no meaningful artifacts.
type SealPipeline struct {
	Summariser Summariser
	Embedder   Embedder
	LTM        LongTermMemory
	Logger     *slog.Logger
}

// NewSealPipeline creates a SealPipeline with the given backends.
// If logger is nil, the default slog logger is used.
func NewSealPipeline(summariser Summariser, embedder Embedder, ltm LongTermMemory, logger *slog.Logger) *SealPipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &SealPipeline{
		Summariser: summariser,
		Embedder:   embedder,
		LTM:        ltm,
		Logger:     logger,
	}
}

// Seal processes a sealed conversation through the archive pipeline:
//  1. Summarise the conversation transcript.
//  2. Embed the summary to produce a vector for similarity search.
//  3. Store the MemoryEntry in long-term memory.
//
// Each step is tolerant of noop backends. Errors at any step are logged but
// do not prevent subsequent steps from running (best-effort archival).
func (p *SealPipeline) Seal(ctx context.Context, conv Conversation) error {
	start := time.Now()

	// --- 1. Summarise --------------------------------------------------------
	summary, err := p.Summariser.Summarise(ctx, conv.Messages)
	if err != nil {
		p.Logger.Warn("seal pipeline: summarisation failed",
			"conversation_id", conv.ID,
			"room_id", conv.RoomID,
			"sender_id", conv.SenderID,
			"err", err,
		)
		// Continue with an empty summary — the entry is still stored so it can
		// be re-processed later if a real summariser becomes available.
		summary = ""
	}

	// --- 2. Embed ------------------------------------------------------------
	var embedding []float32
	if summary != "" {
		embedding, err = p.Embedder.Embed(ctx, summary)
		if err != nil {
			p.Logger.Warn("seal pipeline: embedding failed",
				"conversation_id", conv.ID,
				"room_id", conv.RoomID,
				"sender_id", conv.SenderID,
				"err", err,
			)
			// Continue without an embedding — LTM storage still works (just
			// not searchable by similarity).
			embedding = nil
		}
	}

	// --- 3. Store in LTM ----------------------------------------------------
	entry := MemoryEntry{
		ConversationID: conv.ID,
		RoomID:         conv.RoomID,
		SenderID:       conv.SenderID,
		Summary:        summary,
		Embedding:      embedding,
		Messages:       conv.Messages,
		SealedAt:       time.Now(),
		Metadata:       map[string]string{},
	}

	if err := p.LTM.Store(ctx, entry); err != nil {
		p.Logger.Error("seal pipeline: LTM storage failed",
			"conversation_id", conv.ID,
			"room_id", conv.RoomID,
			"sender_id", conv.SenderID,
			"err", err,
		)
		return fmt.Errorf("seal pipeline: LTM storage failed for conversation %s: %w", conv.ID, err)
	}

	// --- INFO log (no message content — only metadata) -----------------------
	duration := conv.LastMsgAt.Sub(conv.StartedAt)
	p.Logger.Info("conversation sealed",
		"conversation_id", conv.ID,
		"room_id", conv.RoomID,
		"sender_id", conv.SenderID,
		"messages", len(conv.Messages),
		"duration", duration.String(),
		"elapsed", time.Since(start).String(),
	)

	// --- DEBUG log (summary only, never raw content) -------------------------
	p.Logger.Debug("seal pipeline: archived conversation",
		"conversation_id", conv.ID,
		"summary_len", len(summary),
		"has_embedding", embedding != nil,
	)

	return nil
}

// SealPipelineRunner runs the seal pipeline on a periodic timer, checking for
// expired conversations and processing them through the archive pipeline.
// It also clears the sealed conversations from the short-term tracker.
type SealPipelineRunner struct {
	tracker  *ConversationTracker
	pipeline *SealPipeline
	interval time.Duration
	logger   *slog.Logger

	stopMu sync.Mutex
	stopCh chan struct{}
}

// NewSealPipelineRunner creates a runner that checks for expired conversations
// at the given interval and processes them through the seal pipeline.
// If interval is zero, it defaults to 60 seconds.
func NewSealPipelineRunner(tracker *ConversationTracker, pipeline *SealPipeline, interval time.Duration, logger *slog.Logger) *SealPipelineRunner {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SealPipelineRunner{
		tracker:  tracker,
		pipeline: pipeline,
		interval: interval,
		logger:   logger,
	}
}

// Run starts the periodic seal-check loop. It blocks until ctx is cancelled
// or Stop is called. Call this in a goroutine.
func (r *SealPipelineRunner) Run(ctx context.Context) {
	r.stopMu.Lock()
	r.stopCh = make(chan struct{})
	r.stopMu.Unlock()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.sealExpired(ctx)
		}
	}
}

// Stop signals the runner to stop. Safe to call multiple times.
func (r *SealPipelineRunner) Stop() {
	r.stopMu.Lock()
	defer r.stopMu.Unlock()

	if r.stopCh != nil {
		select {
		case <-r.stopCh:
			// Already closed.
		default:
			close(r.stopCh)
		}
	}
}

// sealExpired checks for expired conversations and processes them.
func (r *SealPipelineRunner) sealExpired(ctx context.Context) {
	sealed := r.tracker.SealExpired(time.Now())
	if len(sealed) == 0 {
		return
	}

	r.logger.Debug("seal runner: found expired conversations", "count", len(sealed))

	for _, conv := range sealed {
		if err := r.pipeline.Seal(ctx, conv); err != nil {
			r.logger.Warn("seal runner: pipeline failed for conversation",
				"conversation_id", conv.ID,
				"err", err,
			)
		}
	}
}

// ProcessSealed runs the seal pipeline for a batch of already-sealed
// conversations. This is the entry point used by the lazy seal path
// (RecordMessage detects stale conversations and returns them).
func (r *SealPipelineRunner) ProcessSealed(ctx context.Context, sealed []Conversation) {
	for _, conv := range sealed {
		if err := r.pipeline.Seal(ctx, conv); err != nil {
			r.logger.Warn("seal runner: pipeline failed for conversation",
				"conversation_id", conv.ID,
				"err", err,
			)
		}
	}
}

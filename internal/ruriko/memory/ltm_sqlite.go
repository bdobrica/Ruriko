package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// SQLiteLTM implements LongTermMemory using SQLite with JSON1 for metadata
// and brute-force cosine similarity for embedding search. This is suitable
// for deployments with hundreds to low-thousands of sealed conversations.
//
// The conversations table stores summaries, embeddings (as JSON-encoded float32
// arrays in BLOBs), full message transcripts, and extensible metadata.
//
// Search uses Go-side cosine similarity rather than a SQLite extension because
// modernc.org/sqlite does not support custom C functions. At the expected
// scale (hundreds of rows), loading all embeddings and computing similarity
// in Go is fast and avoids external dependencies.
type SQLiteLTM struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSQLiteLTM creates a SQLiteLTM backed by the given database connection.
// The caller must ensure the conversations table exists (created by migration
// 0003_ltm_conversations.sql). If logger is nil, the default slog logger is used.
func NewSQLiteLTM(db *sql.DB, logger *slog.Logger) *SQLiteLTM {
	if logger == nil {
		logger = slog.Default()
	}
	return &SQLiteLTM{db: db, logger: logger}
}

// Store persists a sealed conversation with its embedding and summary.
func (s *SQLiteLTM) Store(ctx context.Context, entry MemoryEntry) error {
	var embeddingJSON []byte
	if entry.Embedding != nil {
		var err error
		embeddingJSON, err = json.Marshal(entry.Embedding)
		if err != nil {
			return fmt.Errorf("ltm sqlite: marshal embedding: %w", err)
		}
	}

	var messagesJSON []byte
	if len(entry.Messages) > 0 {
		var err error
		messagesJSON, err = json.Marshal(entry.Messages)
		if err != nil {
			return fmt.Errorf("ltm sqlite: marshal messages: %w", err)
		}
	}

	var metadataJSON []byte
	if len(entry.Metadata) > 0 {
		var err error
		metadataJSON, err = json.Marshal(entry.Metadata)
		if err != nil {
			return fmt.Errorf("ltm sqlite: marshal metadata: %w", err)
		}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO ltm_conversations
			(id, room_id, sender_id, summary, embedding, messages, sealed_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ConversationID,
		entry.RoomID,
		entry.SenderID,
		entry.Summary,
		embeddingJSON,
		messagesJSON,
		entry.SealedAt.UTC().Format(time.RFC3339),
		metadataJSON,
	)
	if err != nil {
		return fmt.Errorf("ltm sqlite: insert conversation: %w", err)
	}

	s.logger.Debug("ltm sqlite: stored conversation",
		"conversation_id", entry.ConversationID,
		"room_id", entry.RoomID,
		"sender_id", entry.SenderID,
		"summary_len", len(entry.Summary),
		"has_embedding", entry.Embedding != nil,
		"messages", len(entry.Messages),
	)

	return nil
}

// Search finds the top-k most relevant past conversations for the query
// embedding. It loads all embeddings for the given room+sender and computes
// cosine similarity in Go. When no embeddings are stored, returns an empty
// slice.
//
// The query parameter is unused for now (similarity is computed from the
// caller's embedding via the ContextAssembler, which embeds the query and
// passes it through). Callers should ensure the query embedding is passed
// via the ContextAssembler pipeline.
func (s *SQLiteLTM) Search(ctx context.Context, query string, roomID string, senderID string, topK int) ([]MemoryEntry, error) {
	if topK <= 0 {
		return nil, nil
	}

	// Load all conversations for this room+sender that have embeddings.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, room_id, sender_id, summary, embedding, messages, sealed_at, metadata
		FROM ltm_conversations
		WHERE room_id = ? AND sender_id = ? AND embedding IS NOT NULL
		ORDER BY sealed_at DESC`,
		roomID, senderID,
	)
	if err != nil {
		return nil, fmt.Errorf("ltm sqlite: query conversations: %w", err)
	}
	defer rows.Close()

	var entries []MemoryEntry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			s.logger.Warn("ltm sqlite: skip malformed row", "err", err)
			continue
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ltm sqlite: iterate rows: %w", err)
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// We need the query embedding. The current interface passes query as a
	// string, but we need a vector. We use a two-phase approach: the
	// ContextAssembler embeds the query and calls Search. But Search only
	// receives the string query — it needs the embedding too.
	//
	// To bridge this gap without changing the LTM interface, we use a
	// SearchByEmbedding method (see below). For callers using the standard
	// Search interface with a string query, we fall back to returning the
	// most recent entries (recency-based) since we can't embed inside the
	// LTM implementation.
	//
	// This is the expected path when no query embedding is available.
	if topK > len(entries) {
		topK = len(entries)
	}
	return entries[:topK], nil
}

// SearchByEmbedding finds the top-k most relevant past conversations using
// cosine similarity between the given query embedding and stored embeddings.
// This is the preferred search method when an embedding vector is available.
func (s *SQLiteLTM) SearchByEmbedding(ctx context.Context, queryEmbedding []float32, roomID string, senderID string, topK int) ([]MemoryEntry, error) {
	if topK <= 0 || len(queryEmbedding) == 0 {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, room_id, sender_id, summary, embedding, messages, sealed_at, metadata
		FROM ltm_conversations
		WHERE room_id = ? AND sender_id = ? AND embedding IS NOT NULL
		ORDER BY sealed_at DESC`,
		roomID, senderID,
	)
	if err != nil {
		return nil, fmt.Errorf("ltm sqlite: query conversations: %w", err)
	}
	defer rows.Close()

	var candidates []scoredEntry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			s.logger.Warn("ltm sqlite: skip malformed row", "err", err)
			continue
		}

		if len(entry.Embedding) == 0 {
			continue
		}

		sim := cosineSimilarity(queryEmbedding, entry.Embedding)
		candidates = append(candidates, scoredEntry{entry: entry, score: sim})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ltm sqlite: iterate rows: %w", err)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort by descending similarity.
	sortByScore(candidates)

	if topK > len(candidates) {
		topK = len(candidates)
	}

	results := make([]MemoryEntry, topK)
	for i := range topK {
		results[i] = candidates[i].entry
	}
	return results, nil
}

// scanEntry reads a single row from the ltm_conversations table.
func scanEntry(rows *sql.Rows) (MemoryEntry, error) {
	var (
		entry         MemoryEntry
		embeddingJSON sql.NullString
		messagesJSON  sql.NullString
		sealedAtStr   string
		metadataJSON  sql.NullString
	)

	err := rows.Scan(
		&entry.ConversationID,
		&entry.RoomID,
		&entry.SenderID,
		&entry.Summary,
		&embeddingJSON,
		&messagesJSON,
		&sealedAtStr,
		&metadataJSON,
	)
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("scan row: %w", err)
	}

	if embeddingJSON.Valid && embeddingJSON.String != "" {
		if err := json.Unmarshal([]byte(embeddingJSON.String), &entry.Embedding); err != nil {
			return MemoryEntry{}, fmt.Errorf("unmarshal embedding: %w", err)
		}
	}

	if messagesJSON.Valid && messagesJSON.String != "" {
		if err := json.Unmarshal([]byte(messagesJSON.String), &entry.Messages); err != nil {
			return MemoryEntry{}, fmt.Errorf("unmarshal messages: %w", err)
		}
	}

	t, err := time.Parse(time.RFC3339, sealedAtStr)
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("parse sealed_at: %w", err)
	}
	entry.SealedAt = t

	if metadataJSON.Valid && metadataJSON.String != "" {
		entry.Metadata = make(map[string]string)
		if err := json.Unmarshal([]byte(metadataJSON.String), &entry.Metadata); err != nil {
			return MemoryEntry{}, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}

	return entry, nil
}

// cosineSimilarity computes the cosine similarity between two vectors.
// Returns 0 if either vector is empty or has zero magnitude.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// scoredEntry pairs a memory entry with its cosine similarity score.
type scoredEntry struct {
	entry MemoryEntry
	score float64
}

// sortByScore sorts scored entries by descending score (highest similarity first).
// Uses insertion sort — fine for the small N expected (typically < 100).
func sortByScore(items []scoredEntry) {
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && items[j].score < key.score {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}
}

// Compile-time interface satisfaction check.
var _ LongTermMemory = (*SQLiteLTM)(nil)

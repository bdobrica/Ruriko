package memory

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite database with the ltm_conversations
// table and returns the DB handle. The caller should defer db.Close().
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE ltm_conversations (
			id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			embedding TEXT,
			messages TEXT,
			sealed_at TEXT NOT NULL,
			metadata TEXT
		);
		CREATE INDEX idx_ltm_conversations_room_sender ON ltm_conversations(room_id, sender_id);
		CREATE INDEX idx_ltm_conversations_sealed_at ON ltm_conversations(sealed_at);
	`)
	if err != nil {
		db.Close()
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestSQLiteLTM_SatisfiesInterface(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	var ltm LongTermMemory = NewSQLiteLTM(db, nil)
	if ltm == nil {
		t.Fatal("expected non-nil LongTermMemory")
	}
}

func TestSQLiteLTM_StoreAndRetrieve(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entry := MemoryEntry{
		ConversationID: "conv-001",
		RoomID:         "!room:test",
		SenderID:       "@alice:test",
		Summary:        "discussed agent provisioning and secret setup",
		Embedding:      []float32{0.1, 0.2, 0.3, 0.4},
		Messages: []Message{
			{Role: "user", Content: "set up saito", Timestamp: now},
			{Role: "assistant", Content: "done, saito is running", Timestamp: now},
		},
		SealedAt: now,
		Metadata: map[string]string{"template": "saito-agent"},
	}

	if err := ltm.Store(ctx, entry); err != nil {
		t.Fatalf("Store() error: %v", err)
	}

	// Verify the row exists.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM ltm_conversations").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}

	// Retrieve via Search (recency-based when no embedding match).
	results, err := ltm.Search(ctx, "saito", "!room:test", "@alice:test", 5)
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ConversationID != "conv-001" {
		t.Errorf("expected conv-001, got %q", results[0].ConversationID)
	}
	if results[0].Summary != entry.Summary {
		t.Errorf("summary mismatch: got %q", results[0].Summary)
	}
	if len(results[0].Embedding) != 4 {
		t.Errorf("expected 4-dim embedding, got %d", len(results[0].Embedding))
	}
	if len(results[0].Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(results[0].Messages))
	}
	if results[0].Metadata["template"] != "saito-agent" {
		t.Errorf("expected metadata template=saito-agent, got %q", results[0].Metadata["template"])
	}
}

func TestSQLiteLTM_StoreUpsert(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entry := MemoryEntry{
		ConversationID: "conv-dup",
		RoomID:         "!room:test",
		SenderID:       "@alice:test",
		Summary:        "original summary",
		SealedAt:       now,
	}

	if err := ltm.Store(ctx, entry); err != nil {
		t.Fatalf("first Store(): %v", err)
	}

	// Store again with updated summary — should upsert.
	entry.Summary = "updated summary"
	if err := ltm.Store(ctx, entry); err != nil {
		t.Fatalf("second Store(): %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM ltm_conversations").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after upsert, got %d", count)
	}

	var summary string
	if err := db.QueryRow("SELECT summary FROM ltm_conversations WHERE id = 'conv-dup'").Scan(&summary); err != nil {
		t.Fatalf("query summary: %v", err)
	}
	if summary != "updated summary" {
		t.Errorf("expected 'updated summary', got %q", summary)
	}
}

func TestSQLiteLTM_SearchScopedByRoomAndSender(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Store entries for different rooms/senders.
	entries := []MemoryEntry{
		{ConversationID: "conv-a", RoomID: "!room1:test", SenderID: "@alice:test", Summary: "room1 alice", Embedding: []float32{0.1}, SealedAt: now},
		{ConversationID: "conv-b", RoomID: "!room2:test", SenderID: "@alice:test", Summary: "room2 alice", Embedding: []float32{0.2}, SealedAt: now},
		{ConversationID: "conv-c", RoomID: "!room1:test", SenderID: "@bob:test", Summary: "room1 bob", Embedding: []float32{0.3}, SealedAt: now},
	}
	for _, e := range entries {
		if err := ltm.Store(ctx, e); err != nil {
			t.Fatalf("Store(%s): %v", e.ConversationID, err)
		}
	}

	// Search for room1 + alice — should find only conv-a.
	results, err := ltm.Search(ctx, "", "!room1:test", "@alice:test", 10)
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ConversationID != "conv-a" {
		t.Errorf("expected conv-a, got %q", results[0].ConversationID)
	}
}

func TestSQLiteLTM_SearchReturnsEmptyForNoMatches(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()

	results, err := ltm.Search(ctx, "anything", "!nonexistent:test", "@nobody:test", 5)
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSQLiteLTM_SearchRespectsTopK(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := range 5 {
		entry := MemoryEntry{
			ConversationID: fmt.Sprintf("conv-%d", i),
			RoomID:         "!room:test",
			SenderID:       "@alice:test",
			Summary:        fmt.Sprintf("conversation %d", i),
			Embedding:      []float32{float32(i) * 0.1},
			SealedAt:       now.Add(time.Duration(i) * time.Minute),
		}
		if err := ltm.Store(ctx, entry); err != nil {
			t.Fatalf("Store(%d): %v", i, err)
		}
	}

	results, err := ltm.Search(ctx, "", "!room:test", "@alice:test", 2)
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (topK), got %d", len(results))
	}
}

func TestSQLiteLTM_StoreNilEmbedding(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entry := MemoryEntry{
		ConversationID: "conv-no-emb",
		RoomID:         "!room:test",
		SenderID:       "@alice:test",
		Summary:        "no embedding here",
		Embedding:      nil,
		SealedAt:       now,
	}

	if err := ltm.Store(ctx, entry); err != nil {
		t.Fatalf("Store(): %v", err)
	}

	// Search should skip entries without embeddings.
	results, err := ltm.Search(ctx, "query", "!room:test", "@alice:test", 5)
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results (no embedding), got %d", len(results))
	}
}

func TestSQLiteLTM_SearchByEmbedding_CosineSimilarity(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Store entries with known embeddings.
	entries := []MemoryEntry{
		{
			ConversationID: "conv-close",
			RoomID:         "!room:test",
			SenderID:       "@alice:test",
			Summary:        "close to query",
			Embedding:      []float32{1.0, 0.0, 0.0},
			SealedAt:       now,
		},
		{
			ConversationID: "conv-medium",
			RoomID:         "!room:test",
			SenderID:       "@alice:test",
			Summary:        "somewhat related",
			Embedding:      []float32{0.7, 0.7, 0.0},
			SealedAt:       now,
		},
		{
			ConversationID: "conv-far",
			RoomID:         "!room:test",
			SenderID:       "@alice:test",
			Summary:        "unrelated",
			Embedding:      []float32{0.0, 0.0, 1.0},
			SealedAt:       now,
		},
	}
	for _, e := range entries {
		if err := ltm.Store(ctx, e); err != nil {
			t.Fatalf("Store(%s): %v", e.ConversationID, err)
		}
	}

	// Query embedding is closest to conv-close.
	queryEmb := []float32{1.0, 0.1, 0.0}
	results, err := ltm.SearchByEmbedding(ctx, queryEmb, "!room:test", "@alice:test", 3)
	if err != nil {
		t.Fatalf("SearchByEmbedding(): %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First result should be conv-close (highest similarity).
	if results[0].ConversationID != "conv-close" {
		t.Errorf("expected conv-close first, got %q", results[0].ConversationID)
	}
	// Last result should be conv-far (lowest similarity).
	if results[2].ConversationID != "conv-far" {
		t.Errorf("expected conv-far last, got %q", results[2].ConversationID)
	}
}

func TestSQLiteLTM_SearchByEmbedding_RespectsTopK(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := range 5 {
		entry := MemoryEntry{
			ConversationID: fmt.Sprintf("conv-%d", i),
			RoomID:         "!room:test",
			SenderID:       "@alice:test",
			Summary:        fmt.Sprintf("conversation %d", i),
			Embedding:      []float32{float32(i) * 0.1, 0.5, 0.5},
			SealedAt:       now,
		}
		if err := ltm.Store(ctx, entry); err != nil {
			t.Fatalf("Store(%d): %v", i, err)
		}
	}

	results, err := ltm.SearchByEmbedding(ctx, []float32{0.5, 0.5, 0.5}, "!room:test", "@alice:test", 2)
	if err != nil {
		t.Fatalf("SearchByEmbedding(): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (topK), got %d", len(results))
	}
}

func TestSQLiteLTM_SearchByEmbedding_EmptyQuery(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()

	results, err := ltm.SearchByEmbedding(ctx, nil, "!room:test", "@alice:test", 5)
	if err != nil {
		t.Fatalf("SearchByEmbedding(): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for nil query, got %d", len(results))
	}
}

func TestSQLiteLTM_SearchByEmbedding_SkipsNilEmbeddings(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ltm := NewSQLiteLTM(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Store one with embedding and one without.
	_ = ltm.Store(ctx, MemoryEntry{
		ConversationID: "conv-with", RoomID: "!room:test", SenderID: "@alice:test",
		Summary: "has embedding", Embedding: []float32{1.0, 0.0}, SealedAt: now,
	})
	_ = ltm.Store(ctx, MemoryEntry{
		ConversationID: "conv-without", RoomID: "!room:test", SenderID: "@alice:test",
		Summary: "no embedding", Embedding: nil, SealedAt: now,
	})

	results, err := ltm.SearchByEmbedding(ctx, []float32{1.0, 0.0}, "!room:test", "@alice:test", 10)
	if err != nil {
		t.Fatalf("SearchByEmbedding(): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (skip nil embedding), got %d", len(results))
	}
	if results[0].ConversationID != "conv-with" {
		t.Errorf("expected conv-with, got %q", results[0].ConversationID)
	}
}

// --- cosine similarity unit tests ---

func TestCosineSimilarity_Identical(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	sim := cosineSimilarity(a, a)
	if math.Abs(sim-1.0) > 1e-6 {
		t.Errorf("identical vectors should have similarity ~1.0, got %f", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{0.0, 1.0, 0.0}
	sim := cosineSimilarity(a, b)
	if math.Abs(sim) > 1e-6 {
		t.Errorf("orthogonal vectors should have similarity ~0.0, got %f", sim)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1.0, 0.0}
	b := []float32{-1.0, 0.0}
	sim := cosineSimilarity(a, b)
	if math.Abs(sim+1.0) > 1e-6 {
		t.Errorf("opposite vectors should have similarity ~-1.0, got %f", sim)
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	a := []float32{1.0, 2.0}
	b := []float32{1.0, 2.0, 3.0}
	sim := cosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different-length vectors should return 0, got %f", sim)
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	sim := cosineSimilarity(nil, nil)
	if sim != 0 {
		t.Errorf("empty vectors should return 0, got %f", sim)
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	a := []float32{0.0, 0.0, 0.0}
	b := []float32{1.0, 2.0, 3.0}
	sim := cosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("zero vector should return 0, got %f", sim)
	}
}

// --- sortByScore tests ---

func TestSortByScore_Ordering(t *testing.T) {
	items := []scoredEntry{
		{entry: MemoryEntry{ConversationID: "low"}, score: 0.1},
		{entry: MemoryEntry{ConversationID: "high"}, score: 0.9},
		{entry: MemoryEntry{ConversationID: "mid"}, score: 0.5},
	}
	sortByScore(items)
	if items[0].entry.ConversationID != "high" {
		t.Errorf("expected 'high' first, got %q", items[0].entry.ConversationID)
	}
	if items[1].entry.ConversationID != "mid" {
		t.Errorf("expected 'mid' second, got %q", items[1].entry.ConversationID)
	}
	if items[2].entry.ConversationID != "low" {
		t.Errorf("expected 'low' third, got %q", items[2].entry.ConversationID)
	}
}

func TestSortByScore_SingleElement(t *testing.T) {
	items := []scoredEntry{
		{entry: MemoryEntry{ConversationID: "only"}, score: 0.5},
	}
	sortByScore(items)
	if items[0].entry.ConversationID != "only" {
		t.Errorf("expected 'only', got %q", items[0].entry.ConversationID)
	}
}

func TestSortByScore_AlreadySorted(t *testing.T) {
	items := []scoredEntry{
		{entry: MemoryEntry{ConversationID: "a"}, score: 0.9},
		{entry: MemoryEntry{ConversationID: "b"}, score: 0.5},
		{entry: MemoryEntry{ConversationID: "c"}, score: 0.1},
	}
	sortByScore(items)
	if items[0].score != 0.9 || items[1].score != 0.5 || items[2].score != 0.1 {
		t.Error("already-sorted slice should remain in order")
	}
}

-- Long-term memory conversations table for R10.7
-- Version: 0012
-- Description: Create ltm_conversations for persistent conversation memory

CREATE TABLE IF NOT EXISTS ltm_conversations (
    id TEXT PRIMARY KEY,                       -- conversation UUID (from Conversation.ID)
    room_id TEXT NOT NULL,                     -- Matrix room ID
    sender_id TEXT NOT NULL,                   -- Matrix user ID (human participant)
    summary TEXT NOT NULL DEFAULT '',          -- human-readable summary
    embedding TEXT,                            -- JSON-encoded []float32 (nullable when embedder is noop)
    messages TEXT,                             -- JSON-encoded []Message transcript (nullable)
    sealed_at TEXT NOT NULL,                   -- RFC3339 timestamp
    metadata TEXT                              -- JSON-encoded map[string]string (nullable)
);

CREATE INDEX idx_ltm_conversations_room_sender ON ltm_conversations(room_id, sender_id);
CREATE INDEX idx_ltm_conversations_sealed_at ON ltm_conversations(sealed_at);

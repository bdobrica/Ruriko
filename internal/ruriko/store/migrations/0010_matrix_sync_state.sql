-- Migration 0010: Matrix sync state persistence
-- Description: Stores the mautrix SyncStore fields (next_batch token and
-- filter ID) in SQLite so that the bot resumes from the current position
-- after a restart instead of replaying the full room history.
--
-- Each row is keyed by (user_id, key) where key is one of:
--   next_batch  -- opaque token returned by the homeserver /sync endpoint
--   filter_id   -- Matrix event filter ID uploaded to the homeserver
--
-- The bot will only ever have one user_id, but the schema is generic so it
-- remains compatible with the upstream mautrix SyncStore interface.

CREATE TABLE IF NOT EXISTS matrix_sync_state (
    user_id TEXT    NOT NULL,
    key     TEXT    NOT NULL,
    value   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, key)
);

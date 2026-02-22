package matrix

// syncstore.go implements mautrix.SyncStore backed by the Ruriko SQLite
// database.  Persisting the next_batch token across restarts prevents the bot
// from replaying old room history and re-processing commands that were already
// handled in a previous run.

import (
	"context"
	"database/sql"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// Compile-time assertion that DBSyncStore satisfies the mautrix.SyncStore interface.
var _ mautrix.SyncStore = (*DBSyncStore)(nil)

// DBSyncStore implements the mautrix.SyncStore interface using SQLite.
// It stores each value as a row in the matrix_sync_state table keyed by
// (user_id, key).
type DBSyncStore struct {
	db *sql.DB
}

// newDBSyncStore creates a DBSyncStore backed by the given database connection.
// The caller is responsible for ensuring migration 0010_matrix_sync_state.sql
// has been applied before the store is used.
func newDBSyncStore(db *sql.DB) *DBSyncStore {
	return &DBSyncStore{db: db}
}

// SaveFilterID persists the Matrix event-filter ID for the given user.
func (s *DBSyncStore) SaveFilterID(ctx context.Context, userID id.UserID, filterID string) error {
	return s.saveKey(ctx, userID.String(), "filter_id", filterID)
}

// LoadFilterID retrieves the persisted event-filter ID for the given user.
// Returns ("", nil) when no filter has been saved yet.
func (s *DBSyncStore) LoadFilterID(ctx context.Context, userID id.UserID) (string, error) {
	return s.loadKey(ctx, userID.String(), "filter_id")
}

// SaveNextBatch persists the opaque /sync next_batch token so the bot can
// resume from the correct position after a restart.
func (s *DBSyncStore) SaveNextBatch(ctx context.Context, userID id.UserID, nextBatchToken string) error {
	return s.saveKey(ctx, userID.String(), "next_batch", nextBatchToken)
}

// LoadNextBatch retrieves the last saved next_batch token.
// Returns ("", nil) when no token has been saved yet (first run).
func (s *DBSyncStore) LoadNextBatch(ctx context.Context, userID id.UserID) (string, error) {
	return s.loadKey(ctx, userID.String(), "next_batch")
}

// saveKey upserts a key→value pair for the given user.
func (s *DBSyncStore) saveKey(ctx context.Context, userID, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO matrix_sync_state (user_id, key, value)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value
	`, userID, key, value)
	return err
}

// loadKey retrieves a stored value; returns ("", nil) when the row is missing.
func (s *DBSyncStore) loadKey(ctx context.Context, userID, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `
		SELECT value FROM matrix_sync_state WHERE user_id = ? AND key = ?
	`, userID, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

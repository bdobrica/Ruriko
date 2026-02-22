// Package config provides a lightweight key/value configuration store backed
// by a SQLite table.  It holds non-secret operator-tunable knobs such as the
// NLP model name, endpoint URL, and rate-limit.
//
// Sensitive values (API keys) belong in the encrypted secrets store; this
// package intentionally handles only non-credential configuration so that the
// security-audit boundary between secrets and plain config remains clear.
package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// ErrNotFound is returned by Get when the requested key does not exist.
var ErrNotFound = errors.New("config: key not found")

// Store is the read/write interface for the runtime configuration table.
// Implementations must be safe for concurrent use.
type Store interface {
	// Get returns the value associated with key.  Returns ErrNotFound when the
	// key has not been set.
	Get(ctx context.Context, key string) (string, error)

	// Set stores value under key, creating or overwriting the entry and
	// recording the current UTC timestamp in updated_at.
	Set(ctx context.Context, key string, value string) error

	// Delete removes key from the store.  It is a no-op (no error) when the
	// key does not exist.
	Delete(ctx context.Context, key string) error

	// List returns a snapshot of all key/value pairs currently in the store.
	// An empty map (not nil) is returned when no entries are present.
	List(ctx context.Context) (map[string]string, error)
}

// sqliteStore is the SQLite-backed implementation of Store.
type sqliteStore struct {
	db *store.Store
}

// New creates a Store backed by the application SQLite database.
// The migration that creates the config table must have been applied before
// New is called (this is guaranteed by store.New running all migrations on
// startup).
func New(db *store.Store) Store {
	return &sqliteStore{db: db}
}

// Get returns the value for key or ErrNotFound when absent.
func (s *sqliteStore) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.DB().QueryRowContext(ctx,
		`SELECT value FROM config WHERE key = ?`, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("config: get %q: %w", key, err)
	}
	return value, nil
}

// Set upserts the key/value pair, updating updated_at to the current UTC time.
func (s *sqliteStore) Set(ctx context.Context, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.DB().ExecContext(ctx, `
		INSERT INTO config (key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at
	`, key, value, now)
	if err != nil {
		return fmt.Errorf("config: set %q: %w", key, err)
	}
	return nil
}

// Delete removes key.  It is idempotent — deleting a non-existent key returns
// nil.
func (s *sqliteStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.DB().ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("config: delete %q: %w", key, err)
	}
	return nil
}

// List returns all key/value pairs in the store.
func (s *sqliteStore) List(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.DB().QueryContext(ctx, `SELECT key, value FROM config ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("config: list: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("config: list scan: %w", err)
		}
		result[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("config: list rows: %w", err)
	}
	return result, nil
}

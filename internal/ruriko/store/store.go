// Package store provides database access for Ruriko
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"time"

	"github.com/bdobrica/Ruriko/common/sqliteutil"
	_ "modernc.org/sqlite" // SQLite driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the database connection
type Store struct {
	db *sql.DB
}

// New creates a new Store and runs migrations
func New(dbPath string) (*Store, error) {
	db, err := sqliteutil.Open(dbPath, sqliteutil.OpenOptions{
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		Pragmas: []string{
			"PRAGMA foreign_keys = ON",
			"PRAGMA journal_mode = WAL",
			"PRAGMA synchronous = NORMAL",
			"PRAGMA cache_size = -64000",
			"PRAGMA busy_timeout = 5000",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	store := &Store{db: db}

	// Run migrations
	if err := store.runMigrations(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return store, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection for custom queries
func (s *Store) DB() *sql.DB {
	return s.db
}

// runMigrations runs all pending migrations
func (s *Store) runMigrations() error {
	if err := sqliteutil.RunMigrations(s.db, sqliteutil.MigrationOptions{
		ReadDir:                migrationsFS.ReadDir,
		ReadFile:               migrationsFS.ReadFile,
		Dir:                    "migrations",
		ValidateUniqueVersions: true,
		RecordAppliedAt:        true,
		Now:                    time.Now,
		OnApplied: func(version int, description string) {
			slog.Info("applied migration", "version", fmt.Sprintf("%04d", version), "description", description)
		},
	}); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

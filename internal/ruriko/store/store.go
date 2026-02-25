// Package store provides database access for Ruriko
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// SQLite is single-writer by design. Keep a single shared connection so
	// concurrent callers are serialized by database/sql instead of fighting for
	// write locks across multiple underlying connections.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Enable foreign keys (important for SQLite)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Set pragmas for better performance and reliability
	pragmas := []string{
		"PRAGMA journal_mode = WAL",   // Write-Ahead Logging for better concurrency
		"PRAGMA synchronous = NORMAL", // Balance between safety and speed
		"PRAGMA cache_size = -64000",  // 64MB cache
		"PRAGMA busy_timeout = 5000",  // Wait up to 5s for locks
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set pragma: %w", err)
		}
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
	// Create migrations table if it doesn't exist
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			description TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get current version
	var currentVersion int
	err = s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("failed to get current schema version: %w", err)
	}

	// Read migration files
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Sort migration files
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Validate migration versions are unique across filenames.
	seenVersions := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}

		var version int
		if _, err := fmt.Sscanf(parts[0], "%d", &version); err != nil {
			continue
		}

		if prev, exists := seenVersions[version]; exists {
			return fmt.Errorf("duplicate migration version %04d: %q and %q", version, prev, entry.Name())
		}
		seenVersions[version] = entry.Name()
	}

	// Apply pending migrations
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Extract version from filename (e.g., "0001_init.sql" -> 1)
		var version int
		var description string
		name := entry.Name()
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue
		}

		if _, err := fmt.Sscanf(parts[0], "%d", &version); err != nil {
			continue
		}

		description = strings.TrimSuffix(parts[1], ".sql")

		// Skip if already applied
		if version <= currentVersion {
			continue
		}

		// Read migration file
		content, err := migrationsFS.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", entry.Name(), err)
		}

		// Run migration in transaction
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("failed to begin transaction for migration %d: %w", version, err)
		}

		// Execute migration SQL
		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to execute migration %d: %w", version, err)
		}

		// Record migration
		_, err = tx.Exec(
			"INSERT INTO schema_migrations (version, applied_at, description) VALUES (?, ?, ?)",
			version, time.Now(), description,
		)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %d: %w", version, err)
		}

		slog.Info("applied migration", "version", fmt.Sprintf("%04d", version), "description", description)
	}

	return nil
}

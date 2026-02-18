// Package store provides database access for the Gitai agent runtime.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the SQLite connection and provides access to all agent tables.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database at dbPath and runs all pending
// migrations.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -32000",
		"PRAGMA busy_timeout = 5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("set pragma: %w", err)
		}
	}

	s := &Store{db: db}
	if err := s.runMigrations(); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return s, nil
}

// DB returns the raw *sql.DB for ad-hoc queries.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// runMigrations applies any SQL files not yet recorded in schema_migrations.
func (s *Store) runMigrations() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			description TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var current int
	_ = s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current)

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(e.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(parts[0], "%d", &version); err != nil {
			continue
		}
		if version <= current {
			continue
		}
		description := strings.TrimSuffix(parts[1], ".sql")

		content, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration tx: %w", err)
		}
		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version, description) VALUES (?, ?)",
			version, description,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", e.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", e.Name(), err)
		}
		slog.Info("applied migration", "version", version, "description", description)
	}
	return nil
}

// SaveAppliedConfig stores (or replaces) the currently applied Gosuto config.
func (s *Store) SaveAppliedConfig(hash, yaml string) error {
	_, err := s.db.Exec(`
		INSERT INTO applied_config (id, gosuto_hash, yaml_blob, applied_at)
		VALUES (1, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			gosuto_hash = excluded.gosuto_hash,
			yaml_blob   = excluded.yaml_blob,
			applied_at  = excluded.applied_at
	`, hash, yaml)
	return err
}

// LoadAppliedConfig retrieves the most recently applied Gosuto YAML and hash.
// Returns ("", "", nil) when no config has been applied yet.
func (s *Store) LoadAppliedConfig() (hash, yaml string, err error) {
	row := s.db.QueryRow("SELECT gosuto_hash, yaml_blob FROM applied_config WHERE id = 1")
	if err = row.Scan(&hash, &yaml); err == sql.ErrNoRows {
		return "", "", nil
	}
	return
}

// LogTurn inserts a new row into turn_log and returns the inserted ID.
func (s *Store) LogTurn(traceID, roomID, senderMXID, message string) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO turn_log (trace_id, room_id, sender_mxid, message)
		VALUES (?, ?, ?, ?)`,
		traceID, roomID, senderMXID, message,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishTurn updates an existing turn_log row with the outcome.
func (s *Store) FinishTurn(id int64, toolCalls int, result, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE turn_log SET tool_calls = ?, result = ?, error_msg = ?, finished_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		toolCalls, result, nullableString(errMsg), id,
	)
	return err
}

// SaveApproval persists a new approval request.
func (s *Store) SaveApproval(approvalID, traceID, roomID, action, target, paramsJSON, requestorMXID string, expiresAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO approvals (approval_id, trace_id, room_id, action, target, params_json, status, requestor_mxid, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		approvalID, traceID, roomID, action, target, paramsJSON, requestorMXID, expiresAt,
	)
	return err
}

// ApprovalStatus is the resolved status of an approval.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalExpired  ApprovalStatus = "expired"
)

// GetApprovalStatus returns the current status of an approval.
func (s *Store) GetApprovalStatus(approvalID string) (ApprovalStatus, error) {
	var status string
	var expiresAt time.Time
	err := s.db.QueryRow(
		"SELECT status, expires_at FROM approvals WHERE approval_id = ?", approvalID,
	).Scan(&status, &expiresAt)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("approval %q not found", approvalID)
	}
	if err != nil {
		return "", err
	}
	if ApprovalStatus(status) == ApprovalPending && time.Now().After(expiresAt) {
		_ = s.SetApprovalStatus(approvalID, ApprovalExpired, "", "TTL exceeded")
		return ApprovalExpired, nil
	}
	return ApprovalStatus(status), nil
}

// SetApprovalStatus updates the decision on an approval.
func (s *Store) SetApprovalStatus(approvalID string, status ApprovalStatus, decidedBy, reason string) error {
	_, err := s.db.Exec(`
		UPDATE approvals SET status = ?, decided_at = CURRENT_TIMESTAMP, decided_by = ?, decision_reason = ?
		WHERE approval_id = ?`,
		string(status), nullableString(decidedBy), nullableString(reason), approvalID,
	)
	return err
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

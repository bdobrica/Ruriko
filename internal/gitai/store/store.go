// Package store provides database access for the Gitai agent runtime.
package store

import (
	"database/sql"
	"embed"
	"encoding/json"
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

// LogGatewayTurn inserts a gateway-triggered turn into turn_log.
// The trigger, gatewayName, and eventType columns are set so that gateway
// turns are clearly distinguishable from Matrix-message turns in audit queries.
// Returns the inserted row ID.
func (s *Store) LogGatewayTurn(traceID, roomID, senderMXID, message, gatewayName, eventType string) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO turn_log (trace_id, room_id, sender_mxid, message, trigger, gateway_name, event_type)
		VALUES (?, ?, ?, ?, 'gateway', ?, ?)`,
		traceID, roomID, senderMXID, message, gatewayName, eventType,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishTurnWithDuration updates an existing turn_log row with the outcome and
// the wall-clock duration of the turn in milliseconds. Use instead of FinishTurn
// when the caller has timing information available (e.g. gateway event turns).
func (s *Store) FinishTurnWithDuration(id int64, toolCalls int, durationMS int64, result, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE turn_log
		SET tool_calls = ?, result = ?, error_msg = ?, duration_ms = ?, finished_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		toolCalls, result, nullableString(errMsg), durationMS, id,
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

// PortfolioPosition represents one user-configured portfolio allocation.
type PortfolioPosition struct {
	Ticker     string  `json:"ticker"`
	Allocation float64 `json:"allocation"`
}

// GetKairoPortfolio returns the persisted Kairo portfolio configuration.
// When no portfolio is configured, found is false and err is nil.
func (s *Store) GetKairoPortfolio() (positions []PortfolioPosition, found bool, err error) {
	var raw string
	err = s.db.QueryRow("SELECT portfolio_json FROM kairo_portfolio_config WHERE id = 1").Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal([]byte(raw), &positions); err != nil {
		return nil, false, fmt.Errorf("decode portfolio_json: %w", err)
	}
	return positions, true, nil
}

// SaveKairoPortfolio upserts the Kairo portfolio configuration.
func (s *Store) SaveKairoPortfolio(positions []PortfolioPosition) error {
	raw, err := json.Marshal(positions)
	if err != nil {
		return fmt.Errorf("encode portfolio_json: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO kairo_portfolio_config (id, portfolio_json, updated_at)
		VALUES (1, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			portfolio_json = excluded.portfolio_json,
			updated_at = excluded.updated_at
	`, string(raw))
	return err
}

// KairoAnalysisRun is the row-level data persisted in kairo_analysis_runs.
type KairoAnalysisRun struct {
	TraceID       string
	TriggerSource string
	RoomID        string
	Status        string
	Summary       string
	Commentary    string
}

// KairoAnalysisTicker captures per-ticker metrics and commentary for one run.
type KairoAnalysisTicker struct {
	Ticker        string
	Allocation    float64
	Price         float64
	ChangePercent float64
	Open          float64
	High          float64
	Low           float64
	PreviousClose float64
	Metrics       map[string]interface{}
	Commentary    string
}

// SaveKairoAnalysisRun persists a structured Kairo analysis run and all
// per-ticker metrics in a single transaction.
func (s *Store) SaveKairoAnalysisRun(run KairoAnalysisRun, tickers []KairoAnalysisTicker) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin analysis tx: %w", err)
	}

	res, err := tx.Exec(`
		INSERT INTO kairo_analysis_runs (trace_id, trigger_source, room_id, status, summary, commentary)
		VALUES (?, ?, ?, ?, ?, ?)
	`, run.TraceID, run.TriggerSource, run.RoomID, run.Status, nullableString(run.Summary), nullableString(run.Commentary))
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("insert kairo_analysis_runs: %w", err)
	}

	runID, err := res.LastInsertId()
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("analysis run id: %w", err)
	}

	for _, ticker := range tickers {
		metricsJSON := ""
		if len(ticker.Metrics) > 0 {
			b, err := json.Marshal(ticker.Metrics)
			if err != nil {
				tx.Rollback()
				return 0, fmt.Errorf("encode metrics_json for %s: %w", ticker.Ticker, err)
			}
			metricsJSON = string(b)
		}
		if _, err := tx.Exec(`
			INSERT INTO kairo_analysis_tickers (
				run_id, ticker, allocation, price, change_percent, open, high, low, previous_close, metrics_json, commentary
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			runID,
			ticker.Ticker,
			ticker.Allocation,
			ticker.Price,
			ticker.ChangePercent,
			ticker.Open,
			ticker.High,
			ticker.Low,
			ticker.PreviousClose,
			nullableString(metricsJSON),
			nullableString(ticker.Commentary),
		); err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("insert kairo_analysis_tickers for %s: %w", ticker.Ticker, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit analysis tx: %w", err)
	}

	return runID, nil
}

// GetKairoAnalysisRun loads a persisted Kairo analysis run by ID.
// When the run does not exist, found is false and err is nil.
func (s *Store) GetKairoAnalysisRun(runID int64) (run KairoAnalysisRun, found bool, err error) {
	err = s.db.QueryRow(`
		SELECT trace_id, trigger_source, room_id, status, COALESCE(summary, ''), COALESCE(commentary, '')
		FROM kairo_analysis_runs
		WHERE id = ?
	`, runID).Scan(
		&run.TraceID,
		&run.TriggerSource,
		&run.RoomID,
		&run.Status,
		&run.Summary,
		&run.Commentary,
	)
	if err == sql.ErrNoRows {
		return KairoAnalysisRun{}, false, nil
	}
	if err != nil {
		return KairoAnalysisRun{}, false, err
	}
	return run, true, nil
}

// UpdateKairoAnalysisRunStatus updates the status and commentary fields for an
// existing Kairo analysis run.
func (s *Store) UpdateKairoAnalysisRunStatus(runID int64, status, commentary string) error {
	_, err := s.db.Exec(`
		UPDATE kairo_analysis_runs
		SET status = ?, commentary = ?
		WHERE id = ?
	`, status, nullableString(commentary), runID)
	return err
}

// GetKairoAnalysisMaxAbsChange returns the largest absolute percentage move
// across all tickers for a given run. Returns 0 when no ticker rows exist.
func (s *Store) GetKairoAnalysisMaxAbsChange(runID int64) (float64, error) {
	var maxAbs float64
	err := s.db.QueryRow(`
		SELECT COALESCE(MAX(ABS(change_percent)), 0)
		FROM kairo_analysis_tickers
		WHERE run_id = ?
	`, runID).Scan(&maxAbs)
	if err != nil {
		return 0, err
	}
	return maxAbs, nil
}

// CountKairoNotifiedRunsLastHour returns the number of Kairo analysis runs
// marked as notified within the last hour.
func (s *Store) CountKairoNotifiedRunsLastHour() (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM kairo_analysis_runs
		WHERE status = 'notified'
		  AND created_at >= datetime('now', '-1 hour')
	`).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

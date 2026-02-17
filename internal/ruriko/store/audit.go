package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AuditEntry represents an audit log entry
type AuditEntry struct {
	ID           int64
	Timestamp    time.Time
	TraceID      string
	ActorMXID    string
	Action       string
	Target       sql.NullString
	PayloadJSON  sql.NullString
	Result       string
	ErrorMessage sql.NullString
}

// AuditPayload is a helper for structured audit payloads
type AuditPayload map[string]interface{}

// WriteAudit logs an audit entry
func (s *Store) WriteAudit(ctx context.Context, traceID, actorMXID, action, target, result string, payload AuditPayload, errorMsg string) error {
	var payloadJSON sql.NullString
	if payload != nil {
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal audit payload: %w", err)
		}
		payloadJSON = sql.NullString{String: string(jsonBytes), Valid: true}
	}

	var targetNull sql.NullString
	if target != "" {
		targetNull = sql.NullString{String: target, Valid: true}
	}

	var errorNull sql.NullString
	if errorMsg != "" {
		errorNull = sql.NullString{String: errorMsg, Valid: true}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (ts, trace_id, actor_mxid, action, target, payload_json, result, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, time.Now(), traceID, actorMXID, action, targetNull, payloadJSON, result, errorNull)

	if err != nil {
		return fmt.Errorf("failed to write audit log: %w", err)
	}

	return nil
}

// GetAuditLog retrieves recent audit entries
func (s *Store) GetAuditLog(ctx context.Context, limit int) ([]*AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, trace_id, actor_mxid, action, target, payload_json, result, error_message
		FROM audit_log
		ORDER BY ts DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log: %w", err)
	}
	defer rows.Close()

	var entries []*AuditEntry
	for rows.Next() {
		entry := &AuditEntry{}
		err := rows.Scan(
			&entry.ID, &entry.Timestamp, &entry.TraceID, &entry.ActorMXID,
			&entry.Action, &entry.Target, &entry.PayloadJSON,
			&entry.Result, &entry.ErrorMessage,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan audit entry: %w", err)
		}
		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit log: %w", err)
	}

	return entries, nil
}

// GetAuditByTrace retrieves all audit entries for a trace ID
func (s *Store) GetAuditByTrace(ctx context.Context, traceID string) ([]*AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, trace_id, actor_mxid, action, target, payload_json, result, error_message
		FROM audit_log
		WHERE trace_id = ?
		ORDER BY ts ASC
	`, traceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit log by trace: %w", err)
	}
	defer rows.Close()

	var entries []*AuditEntry
	for rows.Next() {
		entry := &AuditEntry{}
		err := rows.Scan(
			&entry.ID, &entry.Timestamp, &entry.TraceID, &entry.ActorMXID,
			&entry.Action, &entry.Target, &entry.PayloadJSON,
			&entry.Result, &entry.ErrorMessage,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan audit entry: %w", err)
		}
		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit log: %w", err)
	}

	return entries, nil
}

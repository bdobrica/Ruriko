package approvals

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Store persists and retrieves Approval records.
type Store struct {
	db *sql.DB
}

// NewStore creates a new approvals Store backed by the given database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// generateID returns a short, cryptographically random hex ID (6 bytes = 12 hex chars).
func generateID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate approval ID: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// maxIDRetries is the number of times Create will retry on an ID collision.
const maxIDRetries = 3

// Create persists a new pending approval and returns its ID.
// On the unlikely event of an ID collision (6-byte random = 12 hex chars),
// it retries up to maxIDRetries times before failing.
func (s *Store) Create(ctx context.Context, action, target, paramsJSON, requestorMXID string, ttl time.Duration) (*Approval, error) {
	now := time.Now()
	expiresAt := now.Add(ttl)

	var lastErr error
	for attempt := 0; attempt < maxIDRetries; attempt++ {
		id, err := generateID()
		if err != nil {
			return nil, err
		}

		_, err = s.db.ExecContext(ctx, `
			INSERT INTO approvals (id, action, target, params_json, requestor_mxid, status, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)
		`, id, action, target, paramsJSON, requestorMXID, now, expiresAt)
		if err != nil {
			lastErr = err
			continue // likely ID collision; retry with a new ID
		}

		return &Approval{
			ID:            id,
			Action:        action,
			Target:        target,
			ParamsJSON:    paramsJSON,
			RequestorMXID: requestorMXID,
			Status:        StatusPending,
			CreatedAt:     now,
			ExpiresAt:     expiresAt,
		}, nil
	}

	return nil, fmt.Errorf("failed to create approval after %d attempts: %w", maxIDRetries, lastErr)
}

// Get retrieves an approval by ID.
func (s *Store) Get(ctx context.Context, id string) (*Approval, error) {
	a := &Approval{}
	var resolvedAt sql.NullTime
	var resolvedBy sql.NullString
	var resolveReason sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, action, target, params_json, requestor_mxid, status,
		       created_at, expires_at, resolved_at, resolved_by_mxid, resolve_reason
		FROM approvals
		WHERE id = ?
	`, id).Scan(
		&a.ID, &a.Action, &a.Target, &a.ParamsJSON, &a.RequestorMXID, &a.Status,
		&a.CreatedAt, &a.ExpiresAt, &resolvedAt, &resolvedBy, &resolveReason,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("approval not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get approval: %w", err)
	}

	if resolvedAt.Valid {
		t := resolvedAt.Time
		a.ResolvedAt = &t
	}
	if resolvedBy.Valid {
		a.ResolvedByMXID = &resolvedBy.String
	}
	if resolveReason.Valid {
		a.ResolveReason = &resolveReason.String
	}

	return a, nil
}

// List returns approvals filtered by status. Pass an empty string to return all.
func (s *Store) List(ctx context.Context, status string) ([]*Approval, error) {
	var rows *sql.Rows
	var err error

	if status == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, action, target, params_json, requestor_mxid, status,
			       created_at, expires_at, resolved_at, resolved_by_mxid, resolve_reason
			FROM approvals
			ORDER BY created_at DESC
			LIMIT 100
		`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, action, target, params_json, requestor_mxid, status,
			       created_at, expires_at, resolved_at, resolved_by_mxid, resolve_reason
			FROM approvals
			WHERE status = ?
			ORDER BY created_at DESC
			LIMIT 100
		`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list approvals: %w", err)
	}
	defer rows.Close()

	var approvals []*Approval
	for rows.Next() {
		a := &Approval{}
		var resolvedAt sql.NullTime
		var resolvedBy sql.NullString
		var resolveReason sql.NullString

		if err := rows.Scan(
			&a.ID, &a.Action, &a.Target, &a.ParamsJSON, &a.RequestorMXID, &a.Status,
			&a.CreatedAt, &a.ExpiresAt, &resolvedAt, &resolvedBy, &resolveReason,
		); err != nil {
			return nil, fmt.Errorf("failed to scan approval: %w", err)
		}

		if resolvedAt.Valid {
			t := resolvedAt.Time
			a.ResolvedAt = &t
		}
		if resolvedBy.Valid {
			a.ResolvedByMXID = &resolvedBy.String
		}
		if resolveReason.Valid {
			a.ResolveReason = &resolveReason.String
		}

		approvals = append(approvals, a)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating approvals: %w", err)
	}

	return approvals, nil
}

// resolve is the internal helper to update an approval's status.
func (s *Store) resolve(ctx context.Context, id string, newStatus Status, resolverMXID, reason string) error {
	now := time.Now()
	result, err := s.db.ExecContext(ctx, `
		UPDATE approvals
		SET status = ?, resolved_at = ?, resolved_by_mxid = ?, resolve_reason = ?
		WHERE id = ? AND status = 'pending'
	`, string(newStatus), now, resolverMXID, reason, id)
	if err != nil {
		return fmt.Errorf("failed to resolve approval: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if n == 0 {
		// Either ID not found or already resolved — check which.
		existing, lookupErr := s.Get(ctx, id)
		if lookupErr != nil {
			return fmt.Errorf("approval not found: %s", id)
		}
		return fmt.Errorf("approval %s is already in state %q and cannot be changed", id, existing.Status)
	}

	return nil
}

// Approve marks the approval as approved.
func (s *Store) Approve(ctx context.Context, id, approverMXID, reason string) error {
	return s.resolve(ctx, id, StatusApproved, approverMXID, reason)
}

// Deny marks the approval as denied.
func (s *Store) Deny(ctx context.Context, id, denierMXID, reason string) error {
	return s.resolve(ctx, id, StatusDenied, denierMXID, reason)
}

// Cancel marks the approval as cancelled (e.g. requestor withdrew it).
func (s *Store) Cancel(ctx context.Context, id, cancellerMXID, reason string) error {
	return s.resolve(ctx, id, StatusCancelled, cancellerMXID, reason)
}

// ExpireStale marks all pending approvals that have passed their deadline as expired.
// Returns the number of approvals expired.
func (s *Store) ExpireStale(ctx context.Context) (int64, error) {
	now := time.Now()
	result, err := s.db.ExecContext(ctx, `
		UPDATE approvals
		SET status = 'expired', resolved_at = ?
		WHERE status = 'pending' AND expires_at < ?
	`, now, now)
	if err != nil {
		return 0, fmt.Errorf("failed to expire stale approvals: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to check rows affected: %w", err)
	}

	return n, nil
}

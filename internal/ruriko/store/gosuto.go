package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GosutoVersion represents a single versioned Gosuto config snapshot.
type GosutoVersion struct {
	ID            int64
	AgentID       string
	Version       int
	Hash          string // SHA-256 hex of YAMLBlob
	YAMLBlob      string
	CreatedAt     time.Time
	CreatedByMXID string
}

// CreateGosutoVersion inserts a new Gosuto version for an agent and updates
// the agent's gosuto_version field to the new version number.
func (s *Store) CreateGosutoVersion(ctx context.Context, v *GosutoVersion) error {
	v.CreatedAt = time.Now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Insert the new version row.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO gosuto_versions (agent_id, version, hash, yaml_blob, created_at, created_by_mxid)
		VALUES (?, ?, ?, ?, ?, ?)
	`, v.AgentID, v.Version, v.Hash, v.YAMLBlob, v.CreatedAt, v.CreatedByMXID)
	if err != nil {
		return fmt.Errorf("insert gosuto_version: %w", err)
	}

	// Keep the agent row in sync.
	_, err = tx.ExecContext(ctx, `
		UPDATE agents SET gosuto_version = ?, updated_at = ? WHERE id = ?
	`, v.Version, time.Now(), v.AgentID)
	if err != nil {
		return fmt.Errorf("update agent gosuto_version: %w", err)
	}

	return tx.Commit()
}

// NextGosutoVersion returns the next version number (max + 1) for an agent.
// Returns 1 if the agent has no versions yet.
func (s *Store) NextGosutoVersion(ctx context.Context, agentID string) (int, error) {
	var maxVersion sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM gosuto_versions WHERE agent_id = ?`, agentID,
	).Scan(&maxVersion)
	if err != nil {
		return 0, fmt.Errorf("query max version: %w", err)
	}
	if !maxVersion.Valid {
		return 1, nil
	}
	return int(maxVersion.Int64) + 1, nil
}

// GetGosutoVersion retrieves a specific version for an agent.
func (s *Store) GetGosutoVersion(ctx context.Context, agentID string, version int) (*GosutoVersion, error) {
	gv := &GosutoVersion{}
	err := s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, version, hash, yaml_blob, created_at, created_by_mxid
		FROM gosuto_versions
		WHERE agent_id = ? AND version = ?
	`, agentID, version).Scan(
		&gv.ID, &gv.AgentID, &gv.Version, &gv.Hash, &gv.YAMLBlob,
		&gv.CreatedAt, &gv.CreatedByMXID,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("gosuto version %d not found for agent %q", version, agentID)
	}
	if err != nil {
		return nil, fmt.Errorf("query gosuto_version: %w", err)
	}
	return gv, nil
}

// GetLatestGosutoVersion retrieves the highest-numbered version for an agent.
func (s *Store) GetLatestGosutoVersion(ctx context.Context, agentID string) (*GosutoVersion, error) {
	gv := &GosutoVersion{}
	err := s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, version, hash, yaml_blob, created_at, created_by_mxid
		FROM gosuto_versions
		WHERE agent_id = ?
		ORDER BY version DESC
		LIMIT 1
	`, agentID).Scan(
		&gv.ID, &gv.AgentID, &gv.Version, &gv.Hash, &gv.YAMLBlob,
		&gv.CreatedAt, &gv.CreatedByMXID,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no gosuto versions found for agent %q", agentID)
	}
	if err != nil {
		return nil, fmt.Errorf("query latest gosuto_version: %w", err)
	}
	return gv, nil
}

// ListGosutoVersions returns all versions for an agent, newest first.
func (s *Store) ListGosutoVersions(ctx context.Context, agentID string) ([]*GosutoVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, agent_id, version, hash, yaml_blob, created_at, created_by_mxid
		FROM gosuto_versions
		WHERE agent_id = ?
		ORDER BY version DESC
	`, agentID)
	if err != nil {
		return nil, fmt.Errorf("query gosuto_versions: %w", err)
	}
	defer rows.Close()

	var versions []*GosutoVersion
	for rows.Next() {
		gv := &GosutoVersion{}
		if err := rows.Scan(
			&gv.ID, &gv.AgentID, &gv.Version, &gv.Hash, &gv.YAMLBlob,
			&gv.CreatedAt, &gv.CreatedByMXID,
		); err != nil {
			return nil, fmt.Errorf("scan gosuto_version: %w", err)
		}
		versions = append(versions, gv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gosuto_versions: %w", err)
	}
	return versions, nil
}

// PruneGosutoVersions deletes old versions for an agent, keeping at most
// keepN most recent versions. If keepN <= 0, nothing is deleted.
func (s *Store) PruneGosutoVersions(ctx context.Context, agentID string, keepN int) error {
	if keepN <= 0 {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		DELETE FROM gosuto_versions
		WHERE agent_id = ?
		  AND version NOT IN (
			  SELECT version FROM gosuto_versions
			  WHERE agent_id = ?
			  ORDER BY version DESC
			  LIMIT ?
		  )
	`, agentID, agentID, keepN)
	if err != nil {
		return fmt.Errorf("prune gosuto_versions: %w", err)
	}
	return nil
}

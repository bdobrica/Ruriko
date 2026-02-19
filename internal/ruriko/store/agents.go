package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Agent represents an agent in the database
type Agent struct {
	ID             string
	MXID           sql.NullString
	DisplayName    string
	Template       string
	Status         string
	LastSeen       sql.NullTime
	RuntimeVersion sql.NullString
	GosutoVersion  sql.NullInt64
	ContainerID    sql.NullString
	ControlURL     sql.NullString
	Image          sql.NullString
	// ACPToken is the bearer token Ruriko sends on every ACP request to this
	// agent.  It is generated at provisioning time and stored here so that
	// the token can be injected into the agent's environment and looked up
	// when building an ACP client.  NULL means authentication is disabled
	// (dev/test mode).
	ACPToken  sql.NullString
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateAgent inserts a new agent
func (s *Store) CreateAgent(ctx context.Context, agent *Agent) error {
	agent.CreatedAt = time.Now()
	agent.UpdatedAt = time.Now()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agents (id, mxid, display_name, template, status, container_id, control_url, image, acp_token, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, agent.ID, agent.MXID, agent.DisplayName, agent.Template, agent.Status,
		agent.ContainerID, agent.ControlURL, agent.Image, agent.ACPToken, agent.CreatedAt, agent.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}

	return nil
}

// GetAgent retrieves an agent by ID
func (s *Store) GetAgent(ctx context.Context, id string) (*Agent, error) {
	agent := &Agent{}
	err := s.db.QueryRowContext(ctx, `
		SELECT id, mxid, display_name, template, status, last_seen,
		       runtime_version, gosuto_version, container_id, control_url, image,
		       acp_token, created_at, updated_at
		FROM agents
		WHERE id = ?
	`, id).Scan(
		&agent.ID, &agent.MXID, &agent.DisplayName, &agent.Template,
		&agent.Status, &agent.LastSeen, &agent.RuntimeVersion,
		&agent.GosutoVersion, &agent.ContainerID, &agent.ControlURL, &agent.Image,
		&agent.ACPToken, &agent.CreatedAt, &agent.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	return agent, nil
}

// ListAgents returns all agents
func (s *Store) ListAgents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mxid, display_name, template, status, last_seen,
		       runtime_version, gosuto_version, container_id, control_url, image,
		       acp_token, created_at, updated_at
		FROM agents
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		agent := &Agent{}
		err := rows.Scan(
			&agent.ID, &agent.MXID, &agent.DisplayName, &agent.Template,
			&agent.Status, &agent.LastSeen, &agent.RuntimeVersion,
			&agent.GosutoVersion, &agent.ContainerID, &agent.ControlURL, &agent.Image,
			&agent.ACPToken, &agent.CreatedAt, &agent.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan agent: %w", err)
		}
		agents = append(agents, agent)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agents: %w", err)
	}

	return agents, nil
}

// UpdateAgentStatus updates an agent's status
func (s *Store) UpdateAgentStatus(ctx context.Context, id, status string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agents
		SET status = ?, updated_at = ?
		WHERE id = ?
	`, status, time.Now(), id)

	if err != nil {
		return fmt.Errorf("failed to update agent status: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("agent not found: %s", id)
	}

	return nil
}

// UpdateAgentLastSeen updates agent's last seen timestamp
func (s *Store) UpdateAgentLastSeen(ctx context.Context, id string) error {
	now := time.Now()
	result, err := s.db.ExecContext(ctx, `
		UPDATE agents
		SET last_seen = ?, updated_at = ?
		WHERE id = ?
	`, now, now, id)
	if err != nil {
		return fmt.Errorf("failed to update agent last seen: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("agent not found: %s", id)
	}

	return nil
}

// UpdateAgentHandle stores the Docker container ID and ACP control URL.
func (s *Store) UpdateAgentHandle(ctx context.Context, id, containerID, controlURL, image string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agents
		SET container_id = ?, control_url = ?, image = ?, updated_at = ?
		WHERE id = ?
	`, containerID, controlURL, image, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update agent handle: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("agent not found: %s", id)
	}

	return nil
}

// UpdateAgentMXID sets the Matrix user ID for an agent.
func (s *Store) UpdateAgentMXID(ctx context.Context, id, mxid string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agents
		SET mxid = ?, updated_at = ?
		WHERE id = ?
	`, mxid, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update agent mxid: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("agent not found: %s", id)
	}

	return nil
}

// UpdateAgentDisabled marks an agent as disabled.
func (s *Store) UpdateAgentDisabled(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agents
		SET status = 'disabled', updated_at = ?
		WHERE id = ?
	`, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to disable agent: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("agent not found: %s", id)
	}

	return nil
}

// DeleteAgent removes an agent
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete agent: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("agent not found: %s", id)
	}

	return nil
}

// SetAgentACPToken stores the ACP bearer token for an agent.
func (s *Store) SetAgentACPToken(ctx context.Context, id, token string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agents
		SET acp_token = ?, updated_at = ?
		WHERE id = ?
	`, token, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to set acp token: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("agent not found: %s", id)
	}
	return nil
}

// AgentCount returns the number of agents that are not in "deleted" status.
func (s *Store) AgentCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agents WHERE status != 'deleted'",
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count agents: %w", err)
	}
	return count, nil
}

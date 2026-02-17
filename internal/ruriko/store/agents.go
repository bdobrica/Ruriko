package store

import (
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
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateAgent inserts a new agent
func (s *Store) CreateAgent(agent *Agent) error {
	agent.CreatedAt = time.Now()
	agent.UpdatedAt = time.Now()

	_, err := s.db.Exec(`
		INSERT INTO agents (id, mxid, display_name, template, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, agent.ID, agent.MXID, agent.DisplayName, agent.Template, agent.Status, agent.CreatedAt, agent.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}

	return nil
}

// GetAgent retrieves an agent by ID
func (s *Store) GetAgent(id string) (*Agent, error) {
	agent := &Agent{}
	err := s.db.QueryRow(`
		SELECT id, mxid, display_name, template, status, last_seen,
		       runtime_version, gosuto_version, created_at, updated_at
		FROM agents
		WHERE id = ?
	`, id).Scan(
		&agent.ID, &agent.MXID, &agent.DisplayName, &agent.Template,
		&agent.Status, &agent.LastSeen, &agent.RuntimeVersion,
		&agent.GosutoVersion, &agent.CreatedAt, &agent.UpdatedAt,
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
func (s *Store) ListAgents() ([]*Agent, error) {
	rows, err := s.db.Query(`
		SELECT id, mxid, display_name, template, status, last_seen,
		       runtime_version, gosuto_version, created_at, updated_at
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
			&agent.GosutoVersion, &agent.CreatedAt, &agent.UpdatedAt,
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
func (s *Store) UpdateAgentStatus(id, status string) error {
	result, err := s.db.Exec(`
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
func (s *Store) UpdateAgentLastSeen(id string) error {
	_, err := s.db.Exec(`
		UPDATE agents
		SET last_seen = ?, updated_at = ?
		WHERE id = ?
	`, time.Now(), time.Now(), id)

	return err
}

// DeleteAgent removes an agent
func (s *Store) DeleteAgent(id string) error {
	result, err := s.db.Exec("DELETE FROM agents WHERE id = ?", id)
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

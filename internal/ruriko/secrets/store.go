// Package secrets manages encrypted secrets stored in SQLite.
package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bdobrica/Ruriko/common/crypto"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// Type enumerates supported secret types.
type Type string

const (
	TypeMatrixToken Type = "matrix_token"
	TypeAPIKey      Type = "api_key"
	TypeGenericJSON Type = "generic_json"
)

// Secret holds decrypted secret metadata (never the raw value in structured form).
type Secret struct {
	Name            string
	Type            Type
	RotationVersion int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Binding represents a grant for an agent to access a secret.
type Binding struct {
	AgentID           string
	SecretName        string
	Scope             string
	LastPushedVersion int
}

// Store handles encrypted secret persistence.
type Store struct {
	db        *store.Store
	masterKey []byte
}

// New creates a new secrets Store using the provided database and master key.
func New(db *store.Store, masterKey []byte) (*Store, error) {
	if len(masterKey) != crypto.KeySize {
		return nil, fmt.Errorf("master key must be %d bytes", crypto.KeySize)
	}
	return &Store{db: db, masterKey: masterKey}, nil
}

// Set encrypts and stores a secret value. Creates or replaces the secret.
// When the secret already exists, its value and type are overwritten and
// rotation_version is incremented so that bound agents detect the change
// and re-pull the updated value. If you need to preserve the rotation_version
// (e.g. for a no-op administrative overwrite), use the database directly.
// To explicitly rotate with version tracking, prefer Rotate.
func (s *Store) Set(ctx context.Context, name string, secretType Type, value []byte) error {
	encrypted, err := crypto.Encrypt(s.masterKey, value)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}

	_, err = s.db.DB().ExecContext(ctx, `
		INSERT INTO secrets (name, type, encrypted_blob, rotation_version, created_at, updated_at)
		VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET
			type = excluded.type,
			encrypted_blob = excluded.encrypted_blob,
			rotation_version = rotation_version + 1,
			updated_at = CURRENT_TIMESTAMP
	`, name, string(secretType), encrypted)
	if err != nil {
		return fmt.Errorf("upsert secret: %w", err)
	}

	return nil
}

// Get retrieves and decrypts a secret value by name.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	var encrypted []byte
	err := s.db.DB().QueryRowContext(ctx,
		`SELECT encrypted_blob FROM secrets WHERE name = ?`, name,
	).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("query secret: %w", err)
	}

	value, err := crypto.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret: %w", err)
	}

	return value, nil
}

// GetMetadata returns the metadata for a secret without decrypting the value.
func (s *Store) GetMetadata(ctx context.Context, name string) (*Secret, error) {
	var sec Secret
	var secType string
	err := s.db.DB().QueryRowContext(ctx, `
		SELECT name, type, rotation_version, created_at, updated_at
		FROM secrets WHERE name = ?
	`, name).Scan(&sec.Name, &secType, &sec.RotationVersion, &sec.CreatedAt, &sec.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("query secret metadata: %w", err)
	}
	sec.Type = Type(secType)
	return &sec, nil
}

// List returns metadata for all secrets (names and types, never values).
func (s *Store) List(ctx context.Context) ([]*Secret, error) {
	rows, err := s.db.DB().QueryContext(ctx, `
		SELECT name, type, rotation_version, created_at, updated_at
		FROM secrets ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()

	var secrets []*Secret
	for rows.Next() {
		var sec Secret
		var secType string
		if err := rows.Scan(&sec.Name, &secType, &sec.RotationVersion, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan secret row: %w", err)
		}
		sec.Type = Type(secType)
		secrets = append(secrets, &sec)
	}
	return secrets, rows.Err()
}

// Rotate replaces the secret value and increments the rotation version.
func (s *Store) Rotate(ctx context.Context, name string, newValue []byte) error {
	var secType string
	err := s.db.DB().QueryRowContext(ctx,
		`SELECT type FROM secrets WHERE name = ?`, name,
	).Scan(&secType)
	if err == sql.ErrNoRows {
		return fmt.Errorf("secret %q not found", name)
	}
	if err != nil {
		return fmt.Errorf("query secret type: %w", err)
	}

	return s.Set(ctx, name, Type(secType), newValue)
}

// Delete removes a secret by name.
func (s *Store) Delete(ctx context.Context, name string) error {
	res, err := s.db.DB().ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("secret %q not found", name)
	}
	return nil
}

// Bind grants an agent access to a secret.
func (s *Store) Bind(ctx context.Context, agentID, secretName, scope string) error {
	_, err := s.db.DB().ExecContext(ctx, `
		INSERT INTO agent_secret_bindings (agent_id, secret_name, scope, last_pushed_version)
		VALUES (?, ?, ?, 0)
		ON CONFLICT(agent_id, secret_name) DO UPDATE SET scope = excluded.scope
	`, agentID, secretName, scope)
	if err != nil {
		return fmt.Errorf("bind secret: %w", err)
	}
	return nil
}

// UnbindAll revokes all secret bindings for the given agent.
// Returns the number of bindings removed.
func (s *Store) UnbindAll(ctx context.Context, agentID string) (int64, error) {
	res, err := s.db.DB().ExecContext(ctx,
		`DELETE FROM agent_secret_bindings WHERE agent_id = ?`,
		agentID,
	)
	if err != nil {
		return 0, fmt.Errorf("unbind all secrets for agent %q: %w", agentID, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Unbind revokes an agent's access to a secret.
func (s *Store) Unbind(ctx context.Context, agentID, secretName string) error {
	res, err := s.db.DB().ExecContext(ctx,
		`DELETE FROM agent_secret_bindings WHERE agent_id = ? AND secret_name = ?`,
		agentID, secretName,
	)
	if err != nil {
		return fmt.Errorf("unbind secret: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("binding for agent %q / secret %q not found", agentID, secretName)
	}
	return nil
}

// ListBindings returns all bindings for a given agent.
func (s *Store) ListBindings(ctx context.Context, agentID string) ([]*Binding, error) {
	rows, err := s.db.DB().QueryContext(ctx, `
		SELECT agent_id, secret_name, scope, last_pushed_version
		FROM agent_secret_bindings WHERE agent_id = ? ORDER BY secret_name
	`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	defer rows.Close()

	var bindings []*Binding
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.AgentID, &b.SecretName, &b.Scope, &b.LastPushedVersion); err != nil {
			return nil, fmt.Errorf("scan binding: %w", err)
		}
		bindings = append(bindings, &b)
	}
	return bindings, rows.Err()
}

// StaleBindings returns bindings where the secret has been rotated since last push.
func (s *Store) StaleBindings(ctx context.Context) ([]*Binding, error) {
	rows, err := s.db.DB().QueryContext(ctx, `
		SELECT b.agent_id, b.secret_name, b.scope, b.last_pushed_version
		FROM agent_secret_bindings b
		JOIN secrets s ON s.name = b.secret_name
		WHERE b.last_pushed_version < s.rotation_version
		ORDER BY b.agent_id, b.secret_name
	`)
	if err != nil {
		return nil, fmt.Errorf("stale bindings query: %w", err)
	}
	defer rows.Close()

	var bindings []*Binding
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.AgentID, &b.SecretName, &b.Scope, &b.LastPushedVersion); err != nil {
			return nil, fmt.Errorf("scan binding: %w", err)
		}
		bindings = append(bindings, &b)
	}
	return bindings, rows.Err()
}

// MarkPushed updates last_pushed_version for a binding to the current secret version.
func (s *Store) MarkPushed(ctx context.Context, agentID, secretName string) error {
	_, err := s.db.DB().ExecContext(ctx, `
		UPDATE agent_secret_bindings
		SET last_pushed_version = (SELECT rotation_version FROM secrets WHERE name = agent_secret_bindings.secret_name)
		WHERE agent_id = ? AND secret_name = ?
	`, agentID, secretName)
	return err
}

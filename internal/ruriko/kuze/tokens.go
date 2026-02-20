package kuze

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// sentinel errors returned by TokenStore methods.
var (
	// ErrTokenNotFound is returned when the requested token does not exist.
	ErrTokenNotFound = errors.New("kuze: token not found")
	// ErrTokenExpired is returned when the token's TTL has elapsed.
	ErrTokenExpired = errors.New("kuze: token expired")
	// ErrTokenUsed is returned when the token has already been redeemed.
	ErrTokenUsed = errors.New("kuze: token already used")
)

// DefaultTTL is the token lifetime when no TTL is specified.
const DefaultTTL = 10 * time.Minute

// AgentTTL is the short lifetime used for agent redemption tokens.
// Agents are expected to redeem immediately after receiving the token, so
// 60 seconds is intentionally tight (matching the threat-model recommendation
// of 30–60 s for minimising exposure window).
const AgentTTL = 60 * time.Second

// PendingToken represents an un-redeemed Kuze token loaded from the store.
type PendingToken struct {
	Token      string
	SecretRef  string
	SecretType string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	Used       bool
	// AgentID, when non-empty, means this is an agent redemption token and may
	// only be redeemed by the identified agent.
	AgentID string
	// Purpose is an optional free-form label (e.g. "initial provisioning").
	Purpose string
}

// TokenStore manages kuze_tokens rows in SQLite.
type TokenStore struct {
	db  *sql.DB
	ttl time.Duration
}

// newTokenStore creates a TokenStore. Pass ttl == 0 to use DefaultTTL.
func newTokenStore(db *sql.DB, ttl time.Duration) *TokenStore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &TokenStore{db: db, ttl: ttl}
}

// Issue creates and persists a new one-time token scoped to secretRef /
// secretType.  Returns the raw token string and the expiry time on success.
// Agent-scoped tokens should use IssueAgent instead.
func (s *TokenStore) Issue(ctx context.Context, secretRef, secretType string) (string, time.Time, error) {
	return s.issue(ctx, secretRef, secretType, "", "")
}

// IssueAgent creates a short-lived agent redemption token.  The token may
// only be redeemed by the agent identified by agentID (matched against the
// X-Agent-ID header on GET /kuze/redeem/<token>).  purpose is optional and
// stored for audit purposes.
//
// The TTL for agent tokens is always AgentTTL (60 s), regardless of the
// TokenStore's configured TTL, to minimise the exposure window per the
// threat model.
func (s *TokenStore) IssueAgent(ctx context.Context, secretRef, secretType, agentID, purpose string) (string, time.Time, error) {
	if agentID == "" {
		return "", time.Time{}, fmt.Errorf("kuze: agentID must not be empty for agent tokens")
	}
	return s.issue(ctx, secretRef, secretType, agentID, purpose)
}

// issue is the shared low-level insert.  agentID and purpose are nullable.
func (s *TokenStore) issue(ctx context.Context, secretRef, secretType, agentID, purpose string) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("kuze: generate token entropy: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()

	// Agent tokens always use the short AgentTTL regardless of the store's
	// configured TTL; human tokens use the store TTL.
	ttl := s.ttl
	if agentID != "" {
		ttl = AgentTTL
	}
	expiresAt := now.Add(ttl)

	var agentIDVal, purposeVal interface{}
	if agentID != "" {
		agentIDVal = agentID
	}
	if purpose != "" {
		purposeVal = purpose
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO kuze_tokens (token, secret_ref, secret_type, created_at, expires_at, used, agent_id, purpose)
VALUES (?, ?, ?, ?, ?, 0, ?, ?)
`, token, secretRef, secretType,
		now.Format(time.RFC3339),
		expiresAt.Format(time.RFC3339),
		agentIDVal,
		purposeVal,
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("kuze: insert token: %w", err)
	}

	return token, expiresAt, nil
}

// ErrAgentIDMismatch is returned when a redemption request carries a different
// agent identity than the one the token was issued for.
var ErrAgentIDMismatch = errors.New("kuze: agent identity does not match token")

// Redeem atomically validates the agent token, enforces agent identity,
// and burns it.  The returned PendingToken contains the secret coords
// (SecretRef, SecretType) that the caller should use to fetch the plaintext
// value from the secrets store.
//
// Redeem fails with:
//   - ErrTokenNotFound  — token does not exist
//   - ErrTokenExpired   — TTL elapsed
//   - ErrTokenUsed      — already burned
//   - ErrAgentIDMismatch — claimedAgentID != token's agent_id
//
// The burn is performed inside the same SQLite transaction as the SELECT to
// prevent TOCTOU races under concurrent requests.
func (s *TokenStore) Redeem(ctx context.Context, token, claimedAgentID string) (*PendingToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("kuze: begin redeem tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var pt PendingToken
	var createdStr, expiresStr string
	var usedInt int
	var agentIDNull sql.NullString
	var purposeNull sql.NullString

	err = tx.QueryRowContext(ctx, `
SELECT token, secret_ref, secret_type, created_at, expires_at, used, agent_id, purpose
FROM kuze_tokens
WHERE token = ?
`, token).Scan(
		&pt.Token, &pt.SecretRef, &pt.SecretType,
		&createdStr, &expiresStr, &usedInt,
		&agentIDNull, &purposeNull,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("kuze: query token for redeem: %w", err)
	}

	pt.Used = usedInt != 0
	pt.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	pt.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
	pt.AgentID = agentIDNull.String
	pt.Purpose = purposeNull.String

	if pt.Used {
		return nil, ErrTokenUsed
	}
	if time.Now().UTC().After(pt.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	if pt.AgentID != claimedAgentID {
		return nil, ErrAgentIDMismatch
	}

	// Burn inside the same transaction to prevent concurrent double-redemption.
	res, err := tx.ExecContext(ctx, `UPDATE kuze_tokens SET used = 1 WHERE token = ? AND used = 0`, token)
	if err != nil {
		return nil, fmt.Errorf("kuze: burn in redeem tx: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrTokenUsed
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("kuze: commit redeem tx: %w", err)
	}
	return &pt, nil
}

// Validate fetches the token and checks that it is still valid (not used, not
// expired).  Returns a populated PendingToken on success.  The token is NOT
// consumed; call Burn after the secret has been persisted.
//
// Note: for agent redemption use Redeem, which combines validate + burn in a
// single atomic transaction.
func (s *TokenStore) Validate(ctx context.Context, token string) (*PendingToken, error) {
	var pt PendingToken
	var createdStr, expiresStr string
	var usedInt int
	var agentIDNull, purposeNull sql.NullString

	err := s.db.QueryRowContext(ctx, `
SELECT token, secret_ref, secret_type, created_at, expires_at, used, agent_id, purpose
FROM kuze_tokens
WHERE token = ?
`, token).Scan(
		&pt.Token, &pt.SecretRef, &pt.SecretType,
		&createdStr, &expiresStr, &usedInt,
		&agentIDNull, &purposeNull,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("kuze: query token: %w", err)
	}

	pt.Used = usedInt != 0
	pt.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	pt.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
	pt.AgentID = agentIDNull.String
	pt.Purpose = purposeNull.String

	if pt.Used {
		return nil, ErrTokenUsed
	}
	if time.Now().UTC().After(pt.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	return &pt, nil
}

// Burn marks a token as used (single-use semantics).  Call this after the
// secret value has been persisted successfully.  Returns ErrTokenUsed if the
// token was already burned (e.g. due to a concurrent request).
func (s *TokenStore) Burn(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE kuze_tokens SET used = 1 WHERE token = ? AND used = 0
`, token)
	if err != nil {
		return fmt.Errorf("kuze: burn token: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTokenUsed
	}
	return nil
}

// ListExpiredUnused returns all pending tokens whose expiry time has passed
// but that have not yet been redeemed.  These are the candidates for
// user-facing expiry notifications before the rows are deleted.
func (s *TokenStore) ListExpiredUnused(ctx context.Context) ([]*PendingToken, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT token, secret_ref, secret_type, created_at, expires_at, used, agent_id, purpose
FROM kuze_tokens
WHERE used = 0 AND expires_at < ?
`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("kuze: query expired unused tokens: %w", err)
	}
	defer rows.Close()

	var result []*PendingToken
	for rows.Next() {
		var pt PendingToken
		var createdStr, expiresStr string
		var usedInt int
		var agentIDNull, purposeNull sql.NullString
		if err := rows.Scan(
			&pt.Token, &pt.SecretRef, &pt.SecretType,
			&createdStr, &expiresStr, &usedInt,
			&agentIDNull, &purposeNull,
		); err != nil {
			return nil, fmt.Errorf("kuze: scan expired token row: %w", err)
		}
		pt.Used = usedInt != 0
		pt.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		pt.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
		pt.AgentID = agentIDNull.String
		pt.Purpose = purposeNull.String
		result = append(result, &pt)
	}
	return result, rows.Err()
}

// PruneExpired deletes tokens that have expired or have already been used.
// Intended to be called periodically (e.g. from a background goroutine).
func (s *TokenStore) PruneExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM kuze_tokens WHERE expires_at < ? OR used = 1
`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("kuze: prune tokens: %w", err)
	}
	return nil
}

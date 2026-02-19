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

// PendingToken represents an un-redeemed Kuze token loaded from the store.
type PendingToken struct {
	Token      string
	SecretRef  string
	SecretType string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	Used       bool
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
func (s *TokenStore) Issue(ctx context.Context, secretRef, secretType string) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("kuze: generate token entropy: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	expiresAt := now.Add(s.ttl)

	_, err := s.db.ExecContext(ctx, `
INSERT INTO kuze_tokens (token, secret_ref, secret_type, created_at, expires_at, used)
VALUES (?, ?, ?, ?, ?, 0)
`, token, secretRef, secretType,
		now.Format(time.RFC3339),
		expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("kuze: insert token: %w", err)
	}

	return token, expiresAt, nil
}

// Validate fetches the token and checks that it is still valid (not used, not
// expired).  Returns a populated PendingToken on success.  The token is NOT
// consumed; call Burn after the secret has been persisted.
func (s *TokenStore) Validate(ctx context.Context, token string) (*PendingToken, error) {
	var pt PendingToken
	var createdStr, expiresStr string
	var usedInt int

	err := s.db.QueryRowContext(ctx, `
SELECT token, secret_ref, secret_type, created_at, expires_at, used
FROM kuze_tokens
WHERE token = ?
`, token).Scan(
		&pt.Token, &pt.SecretRef, &pt.SecretType,
		&createdStr, &expiresStr, &usedInt,
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
SELECT token, secret_ref, secret_type, created_at, expires_at, used
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
		if err := rows.Scan(
			&pt.Token, &pt.SecretRef, &pt.SecretType,
			&createdStr, &expiresStr, &usedInt,
		); err != nil {
			return nil, fmt.Errorf("kuze: scan expired token row: %w", err)
		}
		pt.Used = usedInt != 0
		pt.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		pt.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
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

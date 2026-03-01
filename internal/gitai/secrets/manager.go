// Package secrets — Manager provides a TTL-bounded cache on top of a *Store.
//
// After a Kuze token redemption the ACP handler calls Manager.Apply, which
// stores the secret values in the backing Store and records an expiry time for
// each ref. When GetSecret is called after the TTL, it returns ErrSecretExpired
// so the caller can request a fresh token from Ruriko instead of silently
// using a stale credential.
//
// Security invariants:
//   - Manager NEVER logs plaintext secret values. It logs only ref names.
//   - Evict clears values from the backing Store to reduce in-memory exposure.
//   - IsExpired and Expired return only ref names, never values.
package secrets

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Sentinel errors returned by Manager accessors.
var (
	// ErrSecretExpired is returned when a secret's TTL has elapsed. The agent
	// should request fresh Kuze redemption tokens from Ruriko before retrying.
	ErrSecretExpired = errors.New("secrets: secret has expired; request fresh tokens from Ruriko")

	// ErrSecretNotFound is returned when no secret with the given ref was ever
	// applied to this Manager.
	ErrSecretNotFound = errors.New("secrets: secret not found")
)

// DefaultCacheTTL is how long a redeemed secret remains valid in the Manager
// before it must be refreshed via a new Kuze token. Four hours balances
// security (short enough to limit credential exposure windows as recommended
// by the threat model) with operational simplicity (long enough for overnight
// unattended operation without constant re-provisioning).
const DefaultCacheTTL = 4 * time.Hour

// cacheEntry records when a particular secret ref expires.
type cacheEntry struct {
	expiresAt time.Time
}

// Manager adds TTL-bounded caching on top of a *Store.
//
// Typical usage:
//
//  1. Ruriko sends POST /secrets/token (ACP) with a list of Kuze leases.
//  2. The ACP handler redeems each lease and calls Manager.Apply(redeemed, 0).
//  3. MCP subprocesses receive the secrets as env-var injections via Store.Env.
//  4. Code that needs a secret value at call time (e.g. LLM provider rebuild)
//     calls Manager.GetSecret(ref) — it returns ErrSecretExpired once the TTL
//     elapses, prompting the agent to request new tokens.
//  5. A background goroutine calls Manager.Evict periodically to free memory.
type Manager struct {
	mu      sync.RWMutex
	store   *Store
	entries map[string]cacheEntry
	ttl     time.Duration
}

// NewManager creates a Manager wrapping store with the given default TTL.
// Pass ttl == 0 to use DefaultCacheTTL.
func NewManager(store *Store, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Manager{
		store:   store,
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

// BackingStore returns the underlying *Store. Use this to call Store-only
// methods such as Env() and Names(). Do not call Apply on the returned store
// directly — use Manager.Apply instead so TTL entries are recorded.
func (m *Manager) BackingStore() *Store { return m.store }

// Apply stores sec in the backing Store and records TTL for each ref.
//
// When ttl is 0 the Manager's configured default TTL is used.
//
// Apply is the only correct way to populate secrets through the Manager; it
// keeps the TTL entries consistent with the backing Store contents.
//
// Apply never logs plaintext secret values; only the count, ref names, and
// TTL are logged at debug level.
func (m *Manager) Apply(sec map[string]string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = m.ttl
	}

	// Store first so that a concurrent GetSecret never sees a TTL entry
	// without a corresponding backing-store value.
	if err := m.store.Apply(sec); err != nil {
		return err
	}

	expiresAt := time.Now().Add(ttl)

	m.mu.Lock()
	defer m.mu.Unlock()

	for ref := range sec {
		m.entries[ref] = cacheEntry{expiresAt: expiresAt}
	}

	// Build ref list for debug logging. Values are never included.
	refs := make([]string, 0, len(sec))
	for r := range sec {
		refs = append(refs, r)
	}
	slog.Debug("secrets: manager applied secrets",
		"count", len(sec),
		"refs", refs,
		"ttl", ttl,
	)
	return nil
}

// GetSecret returns the plaintext string value of a secret.
//
//   - ErrSecretNotFound: ref was never applied to this Manager.
//   - ErrSecretExpired:  ref's TTL has elapsed; the caller should request a
//     fresh Kuze token from Ruriko before retrying.
//
// The returned value is NEVER logged by Manager. Callers must not log it.
func (m *Manager) GetSecret(ref string) (string, error) {
	val, err := m.GetSecretBytes(ref)
	if err != nil {
		return "", err
	}
	return string(val), nil
}

// GetSecretBytes returns the raw bytes for a secret. See GetSecret for the
// full semantics and error cases.
func (m *Manager) GetSecretBytes(ref string) ([]byte, error) {
	m.mu.RLock()
	e, ok := m.entries[ref]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSecretNotFound, ref)
	}
	if time.Now().After(e.expiresAt) {
		return nil, fmt.Errorf("%w: %q", ErrSecretExpired, ref)
	}

	return m.store.Get(ref)
}

// IsExpired reports whether the given ref has no cache entry or its TTL has
// elapsed. Callers can use this to proactively check whether a secret needs
// refreshing before a tool call.
func (m *Manager) IsExpired(ref string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[ref]
	return !ok || time.Now().After(e.expiresAt)
}

// Expired returns the names of all secrets whose TTL has elapsed. The list
// contains only ref names — never values. Order is unspecified.
func (m *Manager) Expired() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now()
	var out []string
	for ref, e := range m.entries {
		if now.After(e.expiresAt) {
			out = append(out, ref)
		}
	}
	return out
}

// Evict removes expired entries from the Manager's TTL map and clears the
// corresponding values from the backing Store, freeing memory and reducing
// the in-memory secret exposure window.
//
// It is safe to call from a background goroutine. Returns the number of
// secrets evicted. Ref names (never values) are logged at info level when any
// evictions occur.
func (m *Manager) Evict() int {
	// First pass under read lock to collect candidates.
	candidates := m.Expired()
	if len(candidates) == 0 {
		return 0
	}

	// Second pass under write lock for a consistent snapshot (avoids TOCTOU
	// where a concurrent Apply refreshes a TTL between the two checks).
	m.mu.Lock()
	now := time.Now()
	var toEvict []string
	for _, ref := range candidates {
		if e, ok := m.entries[ref]; ok && now.After(e.expiresAt) {
			toEvict = append(toEvict, ref)
			delete(m.entries, ref)
		}
	}
	m.mu.Unlock()

	if len(toEvict) == 0 {
		return 0
	}

	// Clear from the backing store outside the Manager lock to avoid
	// holding two locks simultaneously.
	m.store.Clear(toEvict)
	slog.Info("secrets: evicted expired secrets",
		"count", len(toEvict),
		"refs", toEvict,
	)
	return len(toEvict)
}

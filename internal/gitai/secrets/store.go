// Package secrets manages the agent's in-memory secret store.
//
// Ruriko pushes secrets to the agent via the ACP POST /secrets/apply endpoint.
// The agent stores them in memory only — nothing is written to disk —
// and injects them into MCP process environments and LLM requests as needed.
package secrets

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
)

// Store holds the current set of secrets in memory.
// All writes go through Apply so that callers are notified.
type Store struct {
	mu      sync.RWMutex
	secrets map[string][]byte
}

// New returns an empty Store.
func New() *Store {
	return &Store{secrets: make(map[string][]byte)}
}

// Apply replaces the entire secret set with the provided map.
// Keys are secret names; values are base64-encoded secret bytes (as received
// from Ruriko's ACP /secrets/apply payload).
func (s *Store) Apply(incoming map[string]string) error {
	decoded := make(map[string][]byte, len(incoming))
	for name, b64val := range incoming {
		val, err := base64.StdEncoding.DecodeString(b64val)
		if err != nil {
			return fmt.Errorf("decode secret %q: %w", name, err)
		}
		decoded[name] = val
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = decoded
	slog.Info("secrets applied", "count", len(decoded))
	return nil
}

// Get retrieves a secret value by name.
// Returns an error when the secret is not present.
func (s *Store) Get(name string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.secrets[name]
	if !ok {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	return val, nil
}

// GetString retrieves a secret as a UTF-8 string.
func (s *Store) GetString(name string) (string, error) {
	val, err := s.Get(name)
	if err != nil {
		return "", err
	}
	return string(val), nil
}

// Names returns the names of all stored secrets (no values).
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.secrets))
	for k := range s.secrets {
		names = append(names, k)
	}
	return names
}

// Clear removes the named secrets from the store. Refs that do not exist are
// silently skipped. It is called by Manager.Evict to free memory once a
// secret's TTL has elapsed. It is safe to call concurrently.
func (s *Store) Clear(refs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range refs {
		delete(s.secrets, ref)
	}
}

// Env returns a map of ENV_VAR_NAME → secret value string for the provided
// mapping of envVarName → secretName.  Missing secrets are silently skipped.
func (s *Store) Env(mapping map[string]string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(mapping))
	for envKey, secretName := range mapping {
		if val, ok := s.secrets[secretName]; ok {
			out[envKey] = string(val)
		}
	}
	return out
}

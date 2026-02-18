// Package gosuto handles loading, validation, and hot-reloading of the agent's
// Gosuto configuration. The Loader is the authoritative source of the current
// live config inside the agent.
package gosuto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// Loader holds the current Gosuto configuration and allows hot-reloads.
type Loader struct {
	mu     sync.RWMutex
	config *gosutospec.Config
	hash   string
	yaml   string
}

// New creates an empty Loader with no configuration loaded yet.
func New() *Loader {
	return &Loader{}
}

// LoadFile reads a YAML file from disk, validates it, and applies it.
func (l *Loader) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read gosuto file: %w", err)
	}
	return l.Apply(data)
}

// Apply parses and validates a raw YAML payload, then atomically replaces the
// current config. It returns an error without modifying the live config if
// validation fails (safe hot-reload).
func (l *Loader) Apply(data []byte) error {
	var cfg gosutospec.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse gosuto yaml: %w", err)
	}
	if err := gosutospec.Validate(&cfg); err != nil {
		return fmt.Errorf("invalid gosuto config: %w", err)
	}

	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])

	l.mu.Lock()
	defer l.mu.Unlock()

	l.config = &cfg
	l.hash = hash
	l.yaml = string(data)

	slog.Info("gosuto config applied",
		"agent", cfg.Metadata.Name,
		"hash", hash[:12],
	)
	return nil
}

// Config returns the current live Gosuto config.
// Returns nil if no config has been loaded yet.
func (l *Loader) Config() *gosutospec.Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.config
}

// Hash returns the SHA-256 hex digest of the current applied YAML.
// Returns "" when no config is loaded.
func (l *Loader) Hash() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.hash
}

// YAML returns the raw YAML text of the current applied config.
func (l *Loader) YAML() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.yaml
}

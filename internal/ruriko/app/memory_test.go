package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/matrix"
)

// minimalConfig returns the smallest valid Config that can be passed to New()
// without a real Matrix homeserver or Docker daemon. The Matrix client is
// created (mautrix just allocates a struct) but never started, so no network
// calls are made during the test.
func minimalConfig(t *testing.T) *Config {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "ruriko-test.db")
	return &Config{
		DatabasePath: dbPath,
		MasterKey:    make([]byte, 32), // AES-256 zero key — fine for tests
		Matrix: matrix.Config{
			Homeserver:  "https://localhost",
			UserID:      "@test:localhost",
			AccessToken: "test-token",
		},
		EnableDocker: false,
	}
}

// TestAppNew_MemoryEnabled_NoopBackends verifies that App.New() succeeds and
// wires the conversation memory subsystem (with noop backends) when
// MemoryEnabled is explicitly set to true. The handlers must report memory as
// enabled and the sealRunner must be non-nil.
func TestAppNew_MemoryEnabled_NoopBackends(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.MemoryEnabled = true

	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer a.Stop()

	if a.sealRunner == nil {
		t.Error("expected sealRunner to be non-nil when MemoryEnabled=true, got nil")
	}
	if !a.handlers.MemoryEnabled() {
		t.Error("expected handlers.MemoryEnabled() to be true when MemoryEnabled=true, got false")
	}
}

// TestAppNew_MemoryDisabled_NilAssembler verifies that the conversation memory
// subsystem is NOT wired when neither MemoryEnabled nor an NLP API key is
// configured. The handlers must report memory as disabled and the sealRunner
// must be nil (so the seal-check goroutine is not started).
func TestAppNew_MemoryDisabled_NilAssembler(t *testing.T) {
	// Ensure the NLP env var is unset so auto-detection does not fire.
	t.Setenv("RURIKO_NLP_API_KEY", "")

	cfg := minimalConfig(t)
	// MemoryEnabled defaults to false; no NLPProvider set; no env key.

	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer a.Stop()

	if a.sealRunner != nil {
		t.Error("expected sealRunner to be nil when memory is disabled, got non-nil")
	}
	if a.handlers.MemoryEnabled() {
		t.Error("expected handlers.MemoryEnabled() to be false when memory is disabled, got true")
	}
}

// TestAppNew_MemoryAutoEnabled_WithNLPKey verifies the auto-detection path:
// when an NLP API key is present in the environment, memory is enabled
// automatically even though MemoryEnabled is false (zero value).
func TestAppNew_MemoryAutoEnabled_WithNLPKey(t *testing.T) {
	t.Setenv("RURIKO_NLP_API_KEY", "sk-test-key")

	cfg := minimalConfig(t)
	// MemoryEnabled is false (zero value) — auto-detect should kick in.

	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer a.Stop()

	if a.sealRunner == nil {
		t.Error("expected sealRunner to be non-nil when NLP key is present, got nil")
	}
	if !a.handlers.MemoryEnabled() {
		t.Error("expected handlers.MemoryEnabled() to be true when NLP key is present, got false")
	}
}

// TestAppNew_MemoryCustomConfig verifies that custom memory tuning parameters
// (MemoryCooldown, MemorySTMMaxMessages, MemorySTMMaxTokens, MemoryLTMTopK)
// are accepted and the app starts cleanly without error.
func TestAppNew_MemoryCustomConfig(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.MemoryEnabled = true
	cfg.MemoryCooldown = 5 * time.Minute
	cfg.MemorySTMMaxMessages = 20
	cfg.MemorySTMMaxTokens = 3000
	cfg.MemoryLTMTopK = 5

	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New() with custom memory config error = %v", err)
	}
	defer a.Stop()

	if a.sealRunner == nil {
		t.Error("expected sealRunner to be non-nil with custom config, got nil")
	}
	if !a.handlers.MemoryEnabled() {
		t.Error("expected handlers.MemoryEnabled() to be true with custom config")
	}
}

package commands

// nlp_provider_test.go — unit tests for R9.7 NLP provider resolution:
//   - Secret takes precedence over env var
//   - Absent key degrades gracefully to keyword-matching mode
//   - Provider is rebuilt when model or endpoint changes
//   - Provider is NOT rebuilt when configuration is unchanged (cache hit)
//   - Concurrent calls do not race (run with -race)

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/config"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// masterKey26 is a deterministic 32-byte master key for test secrets stores.
var masterKey26 = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}()

// newProviderFixture creates a Handlers instance wired with a real SQLite
// secrets store and config store.  nlpEnvAPIKey is the bootstrap env-var key.
func newProviderFixture(t *testing.T, nlpEnvAPIKey string) (*Handlers, *secrets.Store, config.Store) {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-nlp-provider-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sec, err := secrets.New(s, masterKey26)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	cs := config.New(s)

	h := NewHandlers(HandlersConfig{
		Store:        s,
		Secrets:      sec,
		ConfigStore:  cs,
		NLPEnvAPIKey: nlpEnvAPIKey,
	})
	return h, sec, cs
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestResolveNLPProvider_NoKey_ReturnsNil verifies that when neither the
// secrets store nor the env var holds a key, resolveNLPProvider returns nil
// so the NL layer stays in keyword-matching mode.
func TestResolveNLPProvider_NoKey_ReturnsNil(t *testing.T) {
	h, _, _ := newProviderFixture(t, "" /* no env key */)

	provider := h.resolveNLPProvider(context.Background())
	if provider != nil {
		t.Errorf("expected nil provider when no key is configured, got non-nil")
	}
}

// TestResolveNLPProvider_EnvKey_BuildsProvider verifies that a non-empty
// nlpEnvAPIKey results in a usable provider even before any secret is stored.
func TestResolveNLPProvider_EnvKey_BuildsProvider(t *testing.T) {
	h, _, _ := newProviderFixture(t, "env-only-key")

	provider := h.resolveNLPProvider(context.Background())
	if provider == nil {
		t.Fatal("expected non-nil provider for env-var key, got nil")
	}
	if h.nlpProviderCache.apiKey != "env-only-key" {
		t.Errorf("cache.apiKey = %q, want %q", h.nlpProviderCache.apiKey, "env-only-key")
	}
}

// TestResolveNLPProvider_SecretTakesPrecedenceOverEnv verifies that the
// "ruriko.nlp-api-key" secret is preferred over the env-var bootstrap key.
func TestResolveNLPProvider_SecretTakesPrecedenceOverEnv(t *testing.T) {
	h, sec, _ := newProviderFixture(t, "env-fallback-key")

	// Store a different key in the secrets store.
	if err := sec.Set(context.Background(), nlpSecretName, secrets.TypeAPIKey, []byte("secret-key")); err != nil {
		t.Fatalf("store secret: %v", err)
	}

	provider := h.resolveNLPProvider(context.Background())
	if provider == nil {
		t.Fatal("expected non-nil provider, got nil")
	}
	// The cache must reflect the secret key, not the env-var key.
	if h.nlpProviderCache.apiKey != "secret-key" {
		t.Errorf("cache.apiKey = %q, want %q (secret should take precedence)",
			h.nlpProviderCache.apiKey, "secret-key")
	}
}

// TestResolveNLPProvider_SecretOnly_NoEnvKey verifies that having only the
// secret (and no env var) produces a valid provider.
func TestResolveNLPProvider_SecretOnly_NoEnvKey(t *testing.T) {
	h, sec, _ := newProviderFixture(t, "" /* no env key */)

	if err := sec.Set(context.Background(), nlpSecretName, secrets.TypeAPIKey, []byte("only-secret-key")); err != nil {
		t.Fatalf("store secret: %v", err)
	}

	provider := h.resolveNLPProvider(context.Background())
	if provider == nil {
		t.Fatal("expected non-nil provider when secret is set, got nil")
	}
	if h.nlpProviderCache.apiKey != "only-secret-key" {
		t.Errorf("cache.apiKey = %q, want %q", h.nlpProviderCache.apiKey, "only-secret-key")
	}
}

// TestResolveNLPProvider_ProviderRebuildOnModelChange verifies that when
// nlp.model changes in the config store the provider is rebuilt and the cache
// is updated.
func TestResolveNLPProvider_ProviderRebuildOnModelChange(t *testing.T) {
	h, _, cs := newProviderFixture(t, "test-api-key")

	// First call — no model configured, cache is empty.
	p1 := h.resolveNLPProvider(context.Background())
	if p1 == nil {
		t.Fatal("first call: expected provider, got nil")
	}
	if h.nlpProviderCache.model != "" {
		t.Errorf("initial model = %q, want empty", h.nlpProviderCache.model)
	}

	// Change the model via the config store.
	if err := cs.Set(context.Background(), "nlp.model", "gpt-4o"); err != nil {
		t.Fatalf("config.Set: %v", err)
	}

	// Second call — model has changed; provider must be rebuilt.
	p2 := h.resolveNLPProvider(context.Background())
	if p2 == nil {
		t.Fatal("second call: expected provider, got nil")
	}
	if h.nlpProviderCache.model != "gpt-4o" {
		t.Errorf("after model change cache.model = %q, want %q", h.nlpProviderCache.model, "gpt-4o")
	}

	// p1 and p2 must be different objects (a real rebuild happened).
	// We compare via a nil sentinel trick: if p1 == p2 they share the same
	// memory address.  nlp.New always allocates a new struct so they will
	// differ unless the cache wrongly reuses the old one.
	if p1 == p2 {
		t.Error("provider should have been rebuilt after model change, but got same instance")
	}
}

// TestResolveNLPProvider_NoCacheHitWhenUnchanged verifies that calling
// resolveNLPProvider twice without changing any configuration returns the same
// cached provider and does NOT rebuild.
func TestResolveNLPProvider_NoCacheHitWhenUnchanged(t *testing.T) {
	h, _, _ := newProviderFixture(t, "stable-api-key")

	p1 := h.resolveNLPProvider(context.Background())
	if p1 == nil {
		t.Fatal("first call: expected provider, got nil")
	}

	p2 := h.resolveNLPProvider(context.Background())
	if p2 == nil {
		t.Fatal("second call: expected provider, got nil")
	}

	if p1 != p2 {
		t.Error("provider should NOT be rebuilt when configuration is unchanged, but got different instance")
	}
}

// TestResolveNLPProvider_ProviderRebuildOnEndpointChange verifies that
// changing nlp.endpoint also triggers a rebuild.
func TestResolveNLPProvider_ProviderRebuildOnEndpointChange(t *testing.T) {
	h, _, cs := newProviderFixture(t, "test-api-key")

	p1 := h.resolveNLPProvider(context.Background())
	if p1 == nil {
		t.Fatal("first call: expected provider, got nil")
	}

	if err := cs.Set(context.Background(), "nlp.endpoint", "http://localhost:11434/v1"); err != nil {
		t.Fatalf("config.Set: %v", err)
	}

	p2 := h.resolveNLPProvider(context.Background())
	if p2 == nil {
		t.Fatal("second call: expected provider, got nil")
	}
	if h.nlpProviderCache.endpoint != "http://localhost:11434/v1" {
		t.Errorf("cache.endpoint = %q, want %q",
			h.nlpProviderCache.endpoint, "http://localhost:11434/v1")
	}
	if p1 == p2 {
		t.Error("provider should have been rebuilt after endpoint change")
	}
}

// TestResolveNLPProvider_SecretRotation verifies that rotating the secret
// (replacing the stored value) causes the next resolveNLPProvider call to
// produce a new provider with the updated key.
func TestResolveNLPProvider_SecretRotation(t *testing.T) {
	h, sec, _ := newProviderFixture(t, "" /* no env var */)

	// Initial secret.
	if err := sec.Set(context.Background(), nlpSecretName, secrets.TypeAPIKey, []byte("old-key")); err != nil {
		t.Fatalf("store initial secret: %v", err)
	}
	p1 := h.resolveNLPProvider(context.Background())
	if p1 == nil {
		t.Fatal("first call: expected provider")
	}
	if h.nlpProviderCache.apiKey != "old-key" {
		t.Errorf("initial cache.apiKey = %q, want %q", h.nlpProviderCache.apiKey, "old-key")
	}

	// Rotate the secret.
	if err := sec.Set(context.Background(), nlpSecretName, secrets.TypeAPIKey, []byte("new-key")); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}

	p2 := h.resolveNLPProvider(context.Background())
	if p2 == nil {
		t.Fatal("second call after rotation: expected provider")
	}
	if h.nlpProviderCache.apiKey != "new-key" {
		t.Errorf("post-rotation cache.apiKey = %q, want %q", h.nlpProviderCache.apiKey, "new-key")
	}
	if p1 == p2 {
		t.Error("provider should have been rebuilt after secret rotation")
	}
}

// TestResolveNLPProvider_ConcurrentCallsDoNotRace verifies that concurrent
// calls to resolveNLPProvider from many goroutines are free of data races.
// Run with: go test -race ./internal/ruriko/commands/...
func TestResolveNLPProvider_ConcurrentCallsDoNotRace(t *testing.T) {
	h, _, cs := newProviderFixture(t, "concurrent-key")

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			ctx := context.Background()
			// Half the goroutines also mutate the config to trigger rebuilds
			// in the middle of concurrent reads.
			if n%5 == 0 {
				_ = cs.Set(ctx, "nlp.model", fmt.Sprintf("model-%d", n))
			}
			p := h.resolveNLPProvider(ctx)
			if p == nil {
				t.Errorf("goroutine %d: expected provider, got nil", n)
			}
		}(i)
	}
	wg.Wait()
}

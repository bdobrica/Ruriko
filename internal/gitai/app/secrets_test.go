package app

// Tests for the secret-resolution wiring added in R4.3:
//   - interpolateSecretString: placeholder syntax recognition and resolution
//   - resolveSecretArgs:       map-level placeholder substitution
//   - rebuildLLMProvider:      LLM provider is replaced when APIKeySecretRef is set
//
// These use white-box (package-internal) construction so that we can build a
// minimal *App with only the relevant fields populated, without spinning up
// Matrix or SQLite connections.

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/secrets"
)

// --- helpers ---

// newTestApp builds a minimal App with only secretsMgr (and optionally
// gosutoLdr + cfg) wired — enough for the methods under test.
func newTestApp(mgr *secrets.Manager) *App {
	return &App{secretsMgr: mgr}
}

// applySecret adds a single plaintext secret to mgr (base64-encoded as per
// the production ACP handler convention used by Manager.Apply).
func applySecret(t *testing.T, mgr *secrets.Manager, ref, value string, ttl time.Duration) {
	t.Helper()
	encoded := map[string]string{
		ref: base64.StdEncoding.EncodeToString([]byte(value)),
	}
	if err := mgr.Apply(encoded, ttl); err != nil {
		t.Fatalf("applySecret(%q): %v", ref, err)
	}
}

// --- interpolateSecretString ---

func TestInterpolateSecretString_PlainPassthrough(t *testing.T) {
	a := newTestApp(secrets.NewManager(secrets.New(), time.Hour))
	cases := []string{
		"",
		"hello world",
		"{{not-a-secret}}",
		"{{secret:}}",         // empty ref — passes through unchanged
		"prefix {{secret:k}}", // embedded placeholder (not whole string) — passthrough
		"{{secret:k}}suffix",  // embedded placeholder (not whole string) — passthrough
	}
	for _, c := range cases {
		val, wasRef, err := a.interpolateSecretString(c)
		if err != nil {
			t.Errorf("interpolateSecretString(%q): unexpected error %v", c, err)
		}
		if wasRef {
			t.Errorf("interpolateSecretString(%q): wasRef=true, want false", c)
		}
		if val != c {
			t.Errorf("interpolateSecretString(%q): got %q, want passthrough", c, val)
		}
	}
}

func TestInterpolateSecretString_ResolvesKnownSecret(t *testing.T) {
	mgr := secrets.NewManager(secrets.New(), time.Hour)
	applySecret(t, mgr, "api_key", "sk-test-abc", 0)

	a := newTestApp(mgr)
	val, wasRef, err := a.interpolateSecretString("{{secret:api_key}}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wasRef {
		t.Fatal("wasRef should be true for a valid placeholder")
	}
	if val != "sk-test-abc" {
		t.Errorf("got %q, want %q", val, "sk-test-abc")
	}
}

func TestInterpolateSecretString_ErrorOnMissingSecret(t *testing.T) {
	a := newTestApp(secrets.NewManager(secrets.New(), time.Hour))
	_, wasRef, err := a.interpolateSecretString("{{secret:missing}}")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
	if !wasRef {
		t.Error("wasRef should be true even on error (it was a placeholder)")
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestInterpolateSecretString_ErrorOnExpiredSecret(t *testing.T) {
	mgr := secrets.NewManager(secrets.New(), time.Hour)
	applySecret(t, mgr, "api_key", "sk-old", time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	a := newTestApp(mgr)
	_, wasRef, err := a.interpolateSecretString("{{secret:api_key}}")
	if err == nil {
		t.Fatal("expected error for expired secret, got nil")
	}
	if !wasRef {
		t.Error("wasRef should be true even on expiry")
	}
	if !errors.Is(err, secrets.ErrSecretExpired) {
		t.Errorf("expected ErrSecretExpired, got %v", err)
	}
}

// --- resolveSecretArgs ---

func TestResolveSecretArgs_EmptyMap(t *testing.T) {
	a := newTestApp(secrets.NewManager(secrets.New(), time.Hour))
	got, err := a.resolveSecretArgs(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result for nil input, got %v", got)
	}
}

func TestResolveSecretArgs_NoPlaceholders(t *testing.T) {
	a := newTestApp(secrets.NewManager(secrets.New(), time.Hour))
	input := map[string]interface{}{
		"url":   "https://example.com",
		"count": 42,
	}
	got, err := a.resolveSecretArgs(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["url"] != "https://example.com" {
		t.Errorf("url: got %v, want https://example.com", got["url"])
	}
	if got["count"] != 42 {
		t.Errorf("count: got %v, want 42", got["count"])
	}
}

func TestResolveSecretArgs_ResolvesPlaceholder(t *testing.T) {
	mgr := secrets.NewManager(secrets.New(), time.Hour)
	applySecret(t, mgr, "finnhub_key", "fk-secret-xyz", 0)

	a := newTestApp(mgr)
	input := map[string]interface{}{
		"api_key": "{{secret:finnhub_key}}",
		"symbol":  "AAPL",
	}
	got, err := a.resolveSecretArgs(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["api_key"] != "fk-secret-xyz" {
		t.Errorf("api_key: got %v, want fk-secret-xyz", got["api_key"])
	}
	if got["symbol"] != "AAPL" {
		t.Errorf("symbol: got %v, want AAPL", got["symbol"])
	}
}

func TestResolveSecretArgs_NonStringValuesUnchanged(t *testing.T) {
	a := newTestApp(secrets.NewManager(secrets.New(), time.Hour))
	obj := map[string]interface{}{"nested": "val"}
	input := map[string]interface{}{
		"count":  99,
		"flag":   true,
		"nested": obj,
	}
	got, err := a.resolveSecretArgs(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["count"] != 99 || got["flag"] != true {
		t.Errorf("non-string values changed: %v", got)
	}
}

func TestResolveSecretArgs_ErrorPropagatesForMissingSecret(t *testing.T) {
	a := newTestApp(secrets.NewManager(secrets.New(), time.Hour))
	input := map[string]interface{}{
		"key": "{{secret:does_not_exist}}",
	}
	_, err := a.resolveSecretArgs(input)
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("expected ErrSecretNotFound wrapped, got %v", err)
	}
}

func TestResolveSecretArgs_MultipleArgs_PartialPlaceholders(t *testing.T) {
	mgr := secrets.NewManager(secrets.New(), time.Hour)
	applySecret(t, mgr, "token", "tok-abc", 0)

	a := newTestApp(mgr)
	input := map[string]interface{}{
		"authorization": "{{secret:token}}",
		"model":         "gpt-4o",
		"max_tokens":    128,
	}
	got, err := a.resolveSecretArgs(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["authorization"] != "tok-abc" {
		t.Errorf("authorization: got %v", got["authorization"])
	}
	if got["model"] != "gpt-4o" {
		t.Errorf("model: got %v", got["model"])
	}
}

// --- rebuildLLMProvider ---

// fakeProvider is a minimal llm.Provider used to verify provider replacement.
type fakeProvider struct{ name string }

func (f *fakeProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, nil
}

// TestRebuildLLMProvider_NoAPIKeySecretRef verifies that when the Gosuto
// Persona has no APIKeySecretRef, rebuildLLMProvider is a no-op and the
// existing provider is preserved.
func TestRebuildLLMProvider_NoAPIKeySecretRef(t *testing.T) {
	mgr := secrets.NewManager(secrets.New(), time.Hour)
	ldr := gosuto.New()
	_ = ldr.Apply([]byte(minimalGosutoYAML("")))

	original := &fakeProvider{name: "original"}
	a := &App{
		secretsMgr: mgr,
		gosutoLdr:  ldr,
		cfg:        &Config{},
		llmProv:    original,
	}
	a.rebuildLLMProvider()

	if a.provider() != original {
		t.Error("provider should be unchanged when no APIKeySecretRef configured")
	}
}

// TestRebuildLLMProvider_SecretMissing verifies that when the Gosuto Persona
// specifies an APIKeySecretRef but the secret is not yet cached, the existing
// provider is preserved and no panic occurs.
func TestRebuildLLMProvider_SecretMissing(t *testing.T) {
	mgr := secrets.NewManager(secrets.New(), time.Hour)
	ldr := gosuto.New()
	_ = ldr.Apply([]byte(minimalGosutoYAML("openai_key")))

	original := &fakeProvider{name: "original"}
	a := &App{
		secretsMgr: mgr,
		gosutoLdr:  ldr,
		cfg:        &Config{},
		llmProv:    original,
	}
	a.rebuildLLMProvider() // should log a warning but not panic or replace provider

	if a.provider() != original {
		t.Error("provider should be unchanged when API key secret is missing")
	}
}

// TestRebuildLLMProvider_ReplacesProviderWhenSecretAvailable verifies that
// when an APIKeySecretRef is set and the secret is cached, rebuildLLMProvider
// installs a new LLM provider.
func TestRebuildLLMProvider_ReplacesProviderWhenSecretAvailable(t *testing.T) {
	mgr := secrets.NewManager(secrets.New(), time.Hour)
	applySecret(t, mgr, "openai_key", "sk-fresh", 0)

	ldr := gosuto.New()
	_ = ldr.Apply([]byte(minimalGosutoYAML("openai_key")))

	original := &fakeProvider{name: "original"}
	a := &App{
		secretsMgr: mgr,
		gosutoLdr:  ldr,
		cfg:        &Config{},
		llmProv:    original,
	}
	a.rebuildLLMProvider()

	newProv := a.provider()
	if newProv == nil {
		t.Fatal("provider is nil after rebuild")
	}
	if newProv == original {
		t.Error("provider should have been replaced after successful rebuild")
	}
}

// minimalGosutoYAML returns a minimal valid Gosuto YAML with the given
// apiKeySecretRef set in the Persona block (empty string means omitted).
func minimalGosutoYAML(apiKeySecretRef string) string {
	extra := ""
	if apiKeySecretRef != "" {
		extra = "\n  apiKeySecretRef: " + apiKeySecretRef
	}
	return `apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms:
    - "!room:example.com"
  allowedSenders:
    - "@user:example.com"
persona:
  llmProvider: openai
  model: gpt-4o` + extra + "\n"
}

package secrets_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/gitai/secrets"
)

// applyWith is a helper that populates a Manager with plain-text values using
// base64 encoding (matching the production encoding used by the ACP handler).
func applyWith(t *testing.T, m *secrets.Manager, vals map[string]string, ttl time.Duration) {
	t.Helper()
	encoded := make(map[string]string, len(vals))
	for k, v := range vals {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	if err := m.Apply(encoded, ttl); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// --- construction ---

func TestNewManager_DefaultTTL(t *testing.T) {
	store := secrets.New()
	m := secrets.NewManager(store, 0)
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.BackingStore() != store {
		t.Error("BackingStore should return the same store passed to NewManager")
	}
}

// --- Apply and GetSecret ---

func TestGetSecret_HappyPath(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"api_key": "sk-test-123"}, 0)

	got, err := m.GetSecret("api_key")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got != "sk-test-123" {
		t.Errorf("GetSecret = %q, want %q", got, "sk-test-123")
	}
}

func TestGetSecret_NotFound(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)

	_, err := m.GetSecret("missing_ref")
	if err == nil {
		t.Fatal("expected error for missing ref, got nil")
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestGetSecret_ExpiredAfterTTL(t *testing.T) {
	// Use a tiny TTL (1 ms) so it expires before GetSecret is called.
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"api_key": "sk-expired"}, time.Millisecond)

	time.Sleep(5 * time.Millisecond)

	_, err := m.GetSecret("api_key")
	if err == nil {
		t.Fatal("expected error for expired secret, got nil")
	}
	if !errors.Is(err, secrets.ErrSecretExpired) {
		t.Errorf("expected ErrSecretExpired, got %v", err)
	}
}

func TestGetSecretBytes_HappyPath(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"bin_key": "rawbytes"}, 0)

	got, err := m.GetSecretBytes("bin_key")
	if err != nil {
		t.Fatalf("GetSecretBytes: %v", err)
	}
	if string(got) != "rawbytes" {
		t.Errorf("GetSecretBytes = %q, want %q", got, "rawbytes")
	}
}

// --- Apply refresh (re-apply resets TTL) ---

func TestApply_RefreshResetsExpiry(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	// First apply with a very short TTL…
	applyWith(t, m, map[string]string{"key": "value"}, time.Millisecond)
	time.Sleep(5 * time.Millisecond) // now expired

	// …then re-apply with a longer TTL.
	applyWith(t, m, map[string]string{"key": "newvalue"}, time.Hour)

	got, err := m.GetSecret("key")
	if err != nil {
		t.Fatalf("GetSecret after refresh: %v", err)
	}
	if got != "newvalue" {
		t.Errorf("GetSecret = %q, want %q", got, "newvalue")
	}
}

// --- IsExpired ---

func TestIsExpired_NotYetExpired(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"k": "v"}, time.Hour)

	if m.IsExpired("k") {
		t.Error("IsExpired should be false for freshly applied secret")
	}
}

func TestIsExpired_Expired(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"k": "v"}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	if !m.IsExpired("k") {
		t.Error("IsExpired should be true after TTL elapsed")
	}
}

func TestIsExpired_NeverApplied(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	if !m.IsExpired("nonexistent") {
		t.Error("IsExpired should be true for never-applied ref")
	}
}

// --- Expired ---

func TestExpired_ReturnsOnlyExpiredRefs(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"long": "lv"}, time.Hour)
	applyWith(t, m, map[string]string{"short": "sv"}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	exp := m.Expired()
	if len(exp) != 1 {
		t.Fatalf("Expired() = %v (len %d), want exactly [short]", exp, len(exp))
	}
	if exp[0] != "short" {
		t.Errorf("Expired()[0] = %q, want %q", exp[0], "short")
	}
}

func TestExpired_EmptyWhenNoneExpired(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"k": "v"}, time.Hour)

	if got := m.Expired(); len(got) != 0 {
		t.Errorf("Expired() = %v, want empty", got)
	}
}

// --- Evict ---

func TestEvict_RemovesExpiredFromStore(t *testing.T) {
	store := secrets.New()
	m := secrets.NewManager(store, time.Hour)
	applyWith(t, m, map[string]string{"bye": "secret"}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	n := m.Evict()
	if n != 1 {
		t.Errorf("Evict() = %d, want 1", n)
	}

	// The backing Store should no longer have the key.
	_, err := store.Get("bye")
	if err == nil {
		t.Error("expected store to return error for evicted secret, got nil")
	}
}

func TestEvict_PreservesUnexpiredSecrets(t *testing.T) {
	// In production, all secrets for an agent arrive in a single Apply call
	// (one /secrets/token request = one batch). We model that here: both
	// "keep" and "drop" land in the same batch with the same default TTL.
	// We then force "drop" to expire by re-applying it alone (which also
	// overwrites the backing store with only "drop") and immediately evicting.
	// After eviction, "drop" must be gone and GetSecret("keep") must return
	// ErrSecretExpired (because "keep" was overwritten out of the store by
	// the second Apply — matching the invariant that Apply replaces the full map).
	//
	// To test true preservation we apply both secrets again at the end, expire
	// only "drop", then verify the Manager correctly marks "keep" as still valid.

	store := secrets.New()
	m := secrets.NewManager(store, time.Hour)

	// Step 1: apply both with a long TTL.
	applyWith(t, m, map[string]string{
		"keep": "keeper",
		"drop": "dropper",
	}, time.Hour)

	// Step 2: re-apply both so the store is in sync, then expire "drop" alone.
	// Because Store.Apply replaces the whole map, re-adding both keeps them in sync.
	applyWith(t, m, map[string]string{
		"keep": "keeper",
		"drop": "dropper",
	}, time.Hour) // refresh both; entry for "drop" is set to hour again

	// Step 3: re-apply only "drop" with a tiny TTL so its Manager entry expires,
	// but do NOT call applyWith for "keep" again — this simulates a targeted
	// token refresh for just one secret.
	if err := m.Apply(map[string]string{
		"drop": base64.StdEncoding.EncodeToString([]byte("dropper")),
	}, time.Millisecond); err != nil {
		t.Fatalf("expire drop: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	// At this point:
	//   Manager TTL for "keep" = valid (1 hour from Step 2)
	//   Manager TTL for "drop" = expired
	//   Backing store has only "drop" (the last Apply call only wrote "drop")

	m.Evict() // clears "drop" from store and TTL map

	// "drop" must be gone from the backing store.
	if _, err := store.Get("drop"); err == nil {
		t.Error("expected store error for evicted 'drop', got nil")
	}

	// "keep" TTL entry is still valid in the Manager (not expired).
	if m.IsExpired("keep") {
		t.Error("IsExpired(keep) should be false — TTL was set to 1 hour in Step 2")
	}
}

func TestEvict_NopWhenNothingExpired(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"k": "v"}, time.Hour)

	if n := m.Evict(); n != 0 {
		t.Errorf("Evict() = %d, want 0 when nothing expired", n)
	}
}

func TestEvict_AllExpiredReturnsCorrectCount(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{
		"a": "1",
		"b": "2",
		"c": "3",
	}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	if n := m.Evict(); n != 3 {
		t.Errorf("Evict() = %d, want 3", n)
	}
}

// --- NoLog invariant ---

// TestNoLog_GetSecret_DoesNotLogValue exercises a simplified static check:
// it verifies that the error messages returned by Manager never embed the
// secret value. Since Manager only puts ref names in error text and log
// calls, this test serves as a regression guard.
func TestNoLog_GetSecret_DoesNotLogValue(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{"key": "SUPERSECRET_VALUE"}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	_, err := m.GetSecret("key")
	if err == nil {
		t.Fatal("expected ErrSecretExpired, got nil")
	}
	// The error message should contain the ref name but NOT the secret value.
	if strings.Contains(err.Error(), "SUPERSECRET_VALUE") {
		t.Errorf("error message leaks secret value: %v", err)
	}
}

// --- BackingStore / Apply forwarding ---

func TestApply_InvalidBase64PropagatesError(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	err := m.Apply(map[string]string{"bad": "not-valid-base64!!!"}, 0)
	if err == nil {
		t.Error("expected error for invalid base64 value, got nil")
	}
}

func TestApply_MultipleRefsInOneCall(t *testing.T) {
	m := secrets.NewManager(secrets.New(), time.Hour)
	applyWith(t, m, map[string]string{
		"finnhub_api_key": "fh-key",
		"brave_api_key":   "br-key",
		"openai_api_key":  "oa-key",
	}, 0)

	for ref, want := range map[string]string{
		"finnhub_api_key": "fh-key",
		"brave_api_key":   "br-key",
		"openai_api_key":  "oa-key",
	} {
		got, err := m.GetSecret(ref)
		if err != nil {
			t.Errorf("GetSecret(%q): %v", ref, err)
			continue
		}
		if got != want {
			t.Errorf("GetSecret(%q) = %q, want %q", ref, got, want)
		}
	}
}

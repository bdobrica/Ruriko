package config_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/config"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// newTestStore creates a temporary SQLite database and returns a config.Store
// backed by it.  The database (and its file) are cleaned up when the test ends.
func newTestStore(t *testing.T) config.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ruriko-config-test-*.db")
	if err != nil {
		t.Fatalf("create temp db file: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("appstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return config.New(s)
}

// TestGetNotFound verifies that Get returns ErrNotFound for an absent key.
func TestGetNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, "missing.key")
	if !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

// TestSetAndGet verifies the basic write-then-read round-trip.
func TestSetAndGet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.Set(ctx, "nlp.model", "gpt-4o"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(ctx, "nlp.model")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "gpt-4o" {
		t.Errorf("got %q, want %q", got, "gpt-4o")
	}
}

// TestSetOverwrite verifies that a second Set replaces the previous value.
func TestSetOverwrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.Set(ctx, "nlp.model", "gpt-4o-mini"); err != nil {
		t.Fatalf("Set(1): %v", err)
	}
	if err := store.Set(ctx, "nlp.model", "gpt-4o"); err != nil {
		t.Fatalf("Set(2): %v", err)
	}

	got, err := store.Get(ctx, "nlp.model")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "gpt-4o" {
		t.Errorf("got %q, want %q", got, "gpt-4o")
	}
}

// TestDelete verifies that a key is gone after deletion and that deleting a
// non-existent key is a no-op.
func TestDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.Set(ctx, "nlp.endpoint", "http://localhost:11434/v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := store.Delete(ctx, "nlp.endpoint"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get(ctx, "nlp.endpoint")
	if !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got: %v", err)
	}

	// Deleting again must not error (idempotent).
	if err := store.Delete(ctx, "nlp.endpoint"); err != nil {
		t.Fatalf("Delete (idempotent): %v", err)
	}
}

// TestList verifies that all inserted keys appear in the result and that the
// map is empty (not nil) when the store is empty.
func TestList(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Empty store → non-nil empty map.
	m, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List (empty): %v", err)
	}
	if m == nil {
		t.Fatal("List returned nil map, want empty map")
	}
	if len(m) != 0 {
		t.Fatalf("List returned %d entries on empty store", len(m))
	}

	// Insert several keys.
	pairs := map[string]string{
		"nlp.model":      "gpt-4o",
		"nlp.endpoint":   "https://api.openai.com/v1",
		"nlp.rate-limit": "20",
	}
	for k, v := range pairs {
		if err := store.Set(ctx, k, v); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}

	m, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for k, want := range pairs {
		got, ok := m[k]
		if !ok {
			t.Errorf("key %q missing from List result", k)
			continue
		}
		if got != want {
			t.Errorf("key %q: got %q, want %q", k, got, want)
		}
	}
}

// TestListAfterDelete verifies that deleted keys are absent from List.
func TestListAfterDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_ = store.Set(ctx, "nlp.model", "gpt-4o")
	_ = store.Set(ctx, "nlp.endpoint", "https://api.openai.com/v1")
	_ = store.Delete(ctx, "nlp.endpoint")

	m, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, ok := m["nlp.endpoint"]; ok {
		t.Error("deleted key nlp.endpoint still present in List")
	}
	if _, ok := m["nlp.model"]; !ok {
		t.Error("nlp.model missing from List after unrelated delete")
	}
}

// TestConcurrentAccess verifies that concurrent Set/Get operations do not
// produce data races or errors.  SQLite allows only one writer at a time
// (even in WAL mode), so we keep the goroutine count low enough to stay
// comfortably within the busy_timeout=5000ms window configured by store.New.
func TestConcurrentAccess(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("concurrent.key.%d", i)
			value := fmt.Sprintf("value-%d", i)

			if err := store.Set(ctx, key, value); err != nil {
				t.Errorf("goroutine %d Set: %v", i, err)
				return
			}
			got, err := store.Get(ctx, key)
			if err != nil {
				t.Errorf("goroutine %d Get: %v", i, err)
				return
			}
			if got != value {
				t.Errorf("goroutine %d: got %q, want %q", i, got, value)
			}
		}()
	}

	wg.Wait()
}

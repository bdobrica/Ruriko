package commands_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/config"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// newConfigHandlerFixture creates a Handlers instance wired with a real
// configStore backed by a temporary SQLite database.
func newConfigHandlerFixture(t *testing.T) (*commands.Handlers, config.Store) {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-config-handlers-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	sec, err := secrets.New(s, masterKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	cs := config.New(s)
	h := commands.NewHandlers(commands.HandlersConfig{
		Store:       s,
		Secrets:     sec,
		ConfigStore: cs,
	})
	return h, cs
}

// --- HandleConfigSet -------------------------------------------------------

func TestHandleConfigSet_Success(t *testing.T) {
	h, cs := newConfigHandlerFixture(t)
	ctx := context.Background()

	cmd := parseCmd(t, "/ruriko config set nlp.model gpt-4o")
	resp, err := h.HandleConfigSet(ctx, cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleConfigSet: %v", err)
	}
	if !strings.Contains(resp, "nlp.model") || !strings.Contains(resp, "gpt-4o") {
		t.Errorf("unexpected response: %q", resp)
	}

	// Verify value was actually persisted.
	got, err := cs.Get(ctx, "nlp.model")
	if err != nil {
		t.Fatalf("config.Get: %v", err)
	}
	if got != "gpt-4o" {
		t.Errorf("stored value: got %q, want %q", got, "gpt-4o")
	}
}

func TestHandleConfigSet_UnknownKey(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	cmd := parseCmd(t, "/ruriko config set unknown.key somevalue")
	_, err := h.HandleConfigSet(ctx, cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown config key") {
		t.Errorf("error should mention unknown key: %v", err)
	}
}

func TestHandleConfigSet_MissingArgs(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	cmd := parseCmd(t, "/ruriko config set nlp.model")
	_, err := h.HandleConfigSet(ctx, cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for missing value arg, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("error should contain usage hint: %v", err)
	}
}

func TestHandleConfigSet_AllPermittedKeys(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{"nlp.model", "gpt-4o"},
		{"nlp.endpoint", "http://localhost:11434/v1"},
		{"nlp.rate-limit", "30"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			h, _ := newConfigHandlerFixture(t)
			ctx := context.Background()

			cmd := parseCmd(t, "/ruriko config set "+tc.key+" "+tc.value)
			resp, err := h.HandleConfigSet(ctx, cmd, fakeEvent("@alice:example.com"))
			if err != nil {
				t.Fatalf("HandleConfigSet(%q): %v", tc.key, err)
			}
			// Response should confirm both key and value.
			if !strings.Contains(resp, tc.key) {
				t.Errorf("response missing key %q: %q", tc.key, resp)
			}
			if !strings.Contains(resp, tc.value) {
				t.Errorf("response missing value %q: %q", tc.value, resp)
			}
		})
	}
}

// --- HandleConfigGet -------------------------------------------------------

func TestHandleConfigGet_SetValue(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	// Set then get.
	setCmnd := parseCmd(t, "/ruriko config set nlp.endpoint http://localhost:11434/v1")
	if _, err := h.HandleConfigSet(ctx, setCmnd, fakeEvent("@alice:example.com")); err != nil {
		t.Fatalf("set: %v", err)
	}

	getCmd := parseCmd(t, "/ruriko config get nlp.endpoint")
	resp, err := h.HandleConfigGet(ctx, getCmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleConfigGet: %v", err)
	}
	if !strings.Contains(resp, "http://localhost:11434/v1") {
		t.Errorf("response missing stored value: %q", resp)
	}
}

func TestHandleConfigGet_NotSet(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	cmd := parseCmd(t, "/ruriko config get nlp.model")
	resp, err := h.HandleConfigGet(ctx, cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleConfigGet: %v", err)
	}
	if !strings.Contains(resp, "not set") {
		t.Errorf("response should mention 'not set': %q", resp)
	}
}

func TestHandleConfigGet_UnknownKey(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	cmd := parseCmd(t, "/ruriko config get bad.key")
	_, err := h.HandleConfigGet(ctx, cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

// --- HandleConfigList ------------------------------------------------------

func TestHandleConfigList_Empty(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	cmd := parseCmd(t, "/ruriko config list")
	resp, err := h.HandleConfigList(ctx, cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleConfigList: %v", err)
	}
	if !strings.Contains(resp, "defaults") {
		t.Errorf("empty-store response should mention defaults: %q", resp)
	}
}

func TestHandleConfigList_WithValues(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	for _, pair := range []struct{ k, v string }{
		{"nlp.model", "gpt-4o"},
		{"nlp.rate-limit", "30"},
	} {
		cmd := parseCmd(t, "/ruriko config set "+pair.k+" "+pair.v)
		if _, err := h.HandleConfigSet(ctx, cmd, fakeEvent("@alice:example.com")); err != nil {
			t.Fatalf("set %q: %v", pair.k, err)
		}
	}

	listCmd := parseCmd(t, "/ruriko config list")
	resp, err := h.HandleConfigList(ctx, listCmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleConfigList: %v", err)
	}
	for _, want := range []string{"nlp.model", "gpt-4o", "nlp.rate-limit", "30"} {
		if !strings.Contains(resp, want) {
			t.Errorf("list response missing %q: %q", want, resp)
		}
	}
}

// --- HandleConfigUnset -----------------------------------------------------

func TestHandleConfigUnset_RoundTrip(t *testing.T) {
	h, cs := newConfigHandlerFixture(t)
	ctx := context.Background()

	// Set a value.
	setCmnd := parseCmd(t, "/ruriko config set nlp.model gpt-4o")
	if _, err := h.HandleConfigSet(ctx, setCmnd, fakeEvent("@alice:example.com")); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Unset it.
	unsetCmd := parseCmd(t, "/ruriko config unset nlp.model")
	resp, err := h.HandleConfigUnset(ctx, unsetCmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleConfigUnset: %v", err)
	}
	if !strings.Contains(resp, "unset") && !strings.Contains(resp, "default") {
		t.Errorf("unset response should mention unset/default: %q", resp)
	}

	// Verify it is now gone.
	_, err = cs.Get(ctx, "nlp.model")
	if !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after unset, got: %v", err)
	}
}

func TestHandleConfigUnset_UnknownKey(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	cmd := parseCmd(t, "/ruriko config unset bad.key")
	_, err := h.HandleConfigUnset(ctx, cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestHandleConfigUnset_Idempotent(t *testing.T) {
	h, _ := newConfigHandlerFixture(t)
	ctx := context.Background()

	// Unsetting a key that was never set must not error.
	cmd := parseCmd(t, "/ruriko config unset nlp.model")
	if _, err := h.HandleConfigUnset(ctx, cmd, fakeEvent("@alice:example.com")); err != nil {
		t.Fatalf("HandleConfigUnset (not-set): %v", err)
	}
}

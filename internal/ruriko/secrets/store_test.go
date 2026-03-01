package secrets_test

import (
	"context"
	"os"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

func makeKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func newTestSecrets(t *testing.T) (*secrets.Store, *appstore.Store) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ruriko-secrets-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sec, err := secrets.New(s, makeKey())
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	return sec, s
}

func TestSecretsSetAndGet(t *testing.T) {
	sec, _ := newTestSecrets(t)
	ctx := context.Background()

	err := sec.Set(ctx, "my-api-key", secrets.TypeAPIKey, []byte("sk-abc123"))
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	value, err := sec.Get(ctx, "my-api-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if string(value) != "sk-abc123" {
		t.Errorf("got %q, want %q", value, "sk-abc123")
	}
}

func TestSecretsGet_NotFound(t *testing.T) {
	sec, _ := newTestSecrets(t)

	_, err := sec.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

func TestSecretsSet_UpdatesExisting(t *testing.T) {
	sec, _ := newTestSecrets(t)
	ctx := context.Background()

	if err := sec.Set(ctx, "my-key", secrets.TypeAPIKey, []byte("v1")); err != nil {
		t.Fatalf("first Set: %v", err)
	}

	if err := sec.Set(ctx, "my-key", secrets.TypeAPIKey, []byte("v2")); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	value, err := sec.Get(ctx, "my-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if string(value) != "v2" {
		t.Errorf("got %q, want %q", value, "v2")
	}

	meta, err := sec.GetMetadata(ctx, "my-key")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if meta.RotationVersion != 2 {
		t.Errorf("RotationVersion: got %d, want 2", meta.RotationVersion)
	}
}

func TestSecretsList(t *testing.T) {
	sec, _ := newTestSecrets(t)
	ctx := context.Background()

	names := []struct {
		name string
		typ  secrets.Type
	}{
		{"alpha", secrets.TypeAPIKey},
		{"beta", secrets.TypeMatrixToken},
		{"gamma", secrets.TypeGenericJSON},
	}

	for _, n := range names {
		if err := sec.Set(ctx, n.name, n.typ, []byte("value")); err != nil {
			t.Fatalf("Set(%s): %v", n.name, err)
		}
	}

	list, err := sec.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 secrets, got %d", len(list))
	}

	// Verify no values are exposed via metadata
	for _, s := range list {
		if s.Name == "" {
			t.Error("secret metadata has empty name")
		}
	}
}

func TestSecretsRotate(t *testing.T) {
	sec, _ := newTestSecrets(t)
	ctx := context.Background()

	if err := sec.Set(ctx, "rot-test", secrets.TypeAPIKey, []byte("original")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := sec.Rotate(ctx, "rot-test", []byte("rotated")); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	value, err := sec.Get(ctx, "rot-test")
	if err != nil {
		t.Fatalf("Get after rotate: %v", err)
	}
	if string(value) != "rotated" {
		t.Errorf("got %q, want %q", value, "rotated")
	}

	meta, err := sec.GetMetadata(ctx, "rot-test")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if meta.RotationVersion != 2 {
		t.Errorf("RotationVersion: got %d, want 2", meta.RotationVersion)
	}
}

func TestSecretsRotate_NotFound(t *testing.T) {
	sec, _ := newTestSecrets(t)

	err := sec.Rotate(context.Background(), "nonexistent", []byte("value"))
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

func TestSecretsDelete(t *testing.T) {
	sec, _ := newTestSecrets(t)
	ctx := context.Background()

	if err := sec.Set(ctx, "to-delete", secrets.TypeAPIKey, []byte("val")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := sec.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := sec.Get(ctx, "to-delete")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestSecretsDelete_NotFound(t *testing.T) {
	sec, _ := newTestSecrets(t)

	err := sec.Delete(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error deleting nonexistent secret, got nil")
	}
}

func TestSecretsBindUnbind(t *testing.T) {
	sec, s := newTestSecrets(t)
	ctx := context.Background()

	// Agent must exist to satisfy FK constraint
	if err := s.CreateAgent(ctx, &appstore.Agent{
		ID: "agent-1", DisplayName: "Agent 1", Template: "cron", Status: "stopped",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if err := sec.Set(ctx, "shared-key", secrets.TypeAPIKey, []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := sec.Bind(ctx, "agent-1", "shared-key", "read"); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	bindings, err := sec.ListBindings(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListBindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}
	if bindings[0].SecretName != "shared-key" {
		t.Errorf("SecretName: got %q, want %q", bindings[0].SecretName, "shared-key")
	}
	if bindings[0].Scope != "read" {
		t.Errorf("Scope: got %q, want %q", bindings[0].Scope, "read")
	}

	if err := sec.Unbind(ctx, "agent-1", "shared-key"); err != nil {
		t.Fatalf("Unbind: %v", err)
	}

	bindings, err = sec.ListBindings(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListBindings after unbind: %v", err)
	}
	if len(bindings) != 0 {
		t.Errorf("expected 0 bindings after unbind, got %d", len(bindings))
	}
}

func TestSecretsStaleBindings(t *testing.T) {
	sec, s := newTestSecrets(t)
	ctx := context.Background()

	// Agent must exist to satisfy FK constraint
	if err := s.CreateAgent(ctx, &appstore.Agent{
		ID: "agent-stale", DisplayName: "Stale Agent", Template: "cron", Status: "stopped",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Create secret and bind an agent
	if err := sec.Set(ctx, "stale-key", secrets.TypeAPIKey, []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := sec.Bind(ctx, "agent-stale", "stale-key", "read"); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// No stale bindings yet (last_pushed_version=0, rotation_version=1 → stale immediately)
	stale, err := sec.StaleBindings(ctx)
	if err != nil {
		t.Fatalf("StaleBindings: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale binding, got %d", len(stale))
	}

	// Mark as pushed
	if err := sec.MarkPushed(ctx, "agent-stale", "stale-key"); err != nil {
		t.Fatalf("MarkPushed: %v", err)
	}

	stale, err = sec.StaleBindings(ctx)
	if err != nil {
		t.Fatalf("StaleBindings after push: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected 0 stale bindings after MarkPushed, got %d", len(stale))
	}

	// Rotate the secret — binding becomes stale again
	if err := sec.Rotate(ctx, "stale-key", []byte("v2")); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	stale, err = sec.StaleBindings(ctx)
	if err != nil {
		t.Fatalf("StaleBindings after rotate: %v", err)
	}
	if len(stale) != 1 {
		t.Errorf("expected 1 stale binding after rotate, got %d", len(stale))
	}
}

func TestSecrets_WrongKey(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "ruriko-wrongkey-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer s.Close()

	key1 := makeKey()
	key2 := make([]byte, 32)
	for i := range key2 {
		key2[i] = byte(255 - i)
	}

	sec1, _ := secrets.New(s, key1)
	sec2, _ := secrets.New(s, key2)

	if err := sec1.Set(context.Background(), "k", secrets.TypeAPIKey, []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, err = sec2.Get(context.Background(), "k")
	if err == nil {
		t.Fatal("expected decryption error with wrong key, got nil")
	}
}

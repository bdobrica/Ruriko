package secrets_test

import (
	"encoding/base64"
	"testing"

	"github.com/bdobrica/Ruriko/internal/gitai/secrets"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestApplyAndGet(t *testing.T) {
	s := secrets.New()
	if err := s.Apply(map[string]string{
		"db_password": b64("supersecret"),
		"api_key":     b64("abc123"),
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := s.GetString("db_password")
	if err != nil {
		t.Fatalf("GetString(db_password): %v", err)
	}
	if got != "supersecret" {
		t.Errorf("GetString(db_password) = %q, want %q", got, "supersecret")
	}

	got2, err := s.GetString("api_key")
	if err != nil {
		t.Fatalf("GetString(api_key): %v", err)
	}
	if got2 != "abc123" {
		t.Errorf("GetString(api_key) = %q, want %q", got2, "abc123")
	}
}

func TestGetMissing(t *testing.T) {
	s := secrets.New()
	got, err := s.GetString("nonexistent")
	if err == nil {
		t.Error("expected error for missing secret, got nil")
	}
	if got != "" {
		t.Errorf("expected empty string for missing secret, got %q", got)
	}
}

func TestNames(t *testing.T) {
	s := secrets.New()
	_ = s.Apply(map[string]string{
		"a": b64("1"),
		"b": b64("2"),
	})

	names := s.Names()
	if len(names) != 2 {
		t.Errorf("expected 2 names, got %d: %v", len(names), names)
	}
}

func TestEnv(t *testing.T) {
	s := secrets.New()
	_ = s.Apply(map[string]string{
		"db_pass": b64("hunter2"),
	})

	// mapping: envVarName -> secretName
	env := s.Env(map[string]string{"DB_PASSWORD": "db_pass"})
	if env["DB_PASSWORD"] != "hunter2" {
		t.Errorf("Env mapping failed, got %v", env)
	}
}

func TestApplyInvalidBase64(t *testing.T) {
	s := secrets.New()
	err := s.Apply(map[string]string{"bad": "not-valid-base64!!!"})
	if err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}

func TestApplyOverwrites(t *testing.T) {
	s := secrets.New()
	_ = s.Apply(map[string]string{"key": b64("old")})
	_ = s.Apply(map[string]string{"key": b64("new")})
	got, err := s.GetString("key")
	if err != nil {
		t.Fatalf("GetString: %v", err)
	}
	if got != "new" {
		t.Errorf("expected updated value %q, got %q", "new", got)
	}
}

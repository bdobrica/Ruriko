package redact
package redact_test

import (
	"testing"

	"github.com/bdobrica/Ruriko/common/redact"
)

func TestString_RedactsSensitiveValues(t *testing.T) {
	secret := "super-secret-token-12345"
	line := "Authorization: Bearer super-secret-token-12345 (some log)"
	got := redact.String(line, secret)
	if got == line {
		t.Fatal("expected redaction, got unchanged string")
	}
	const want = "Authorization: Bearer [REDACTED] (some log)"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestString_SkipsShortValues(t *testing.T) {
	line := "abc token"
	// "abc" is only 3 chars — should not be redacted
	got := redact.String(line, "abc")
	if got != line {
		t.Fatalf("short value should not be redacted; got %q", got)
	}
}

func TestString_MultipleValues(t *testing.T) {
	password := "hunter2secret"
	token := "tok_live_xxx"
	line := "pw=hunter2secret tok=tok_live_xxx end"
	got := redact.String(line, password, token)
	if got == line {
		t.Fatal("expected redaction")
	}
	// Both values should be replaced
	if got != "pw=[REDACTED] tok=[REDACTED] end" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestMap_RedactsSensitiveKeys(t *testing.T) {
	m := map[string]any{
		"username":     "alice",
		"password":     "s3cr3t",
		"api_key":      "key_abc",
		"access_token": "tok_123",
		"count":        42,
	}
	out := redact.Map(m)

	if out["username"] != "alice" {
		t.Errorf("username should not be redacted, got %v", out["username"])
	}
	if out["password"] != "[REDACTED]" {
		t.Errorf("password should be redacted, got %v", out["password"])
	}
	if out["api_key"] != "[REDACTED]" {
		t.Errorf("api_key should be redacted, got %v", out["api_key"])
	}
	if out["access_token"] != "[REDACTED]" {
		t.Errorf("access_token should be redacted, got %v", out["access_token"])
	}
	if out["count"] != 42 {
		t.Errorf("non-string count should be unchanged, got %v", out["count"])
	}
}

func TestMap_DoesNotMutateOriginal(t *testing.T) {
	m := map[string]any{"password": "secret"}
	redact.Map(m)
	if m["password"] != "secret" {
		t.Error("Map mutated the original; expected shallow copy")
	}
}

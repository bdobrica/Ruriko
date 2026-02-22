package gateway

// Tests for the built-in webhook gateway (R12.4):
//   - ValidateHMACSHA256: correct signature passes, wrong/malformed fail
//   - WrapRawWebhookBody: JSON body, non-JSON body, empty body, GitHub-like fields

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────────────────
// ValidateHMACSHA256 tests
// ────────────────────────────────────────────────────────────────────────────

// computeHMACSHA256Header constructs a "sha256=<hex>" header value for
// the given secret and body, matching the format used by GitHub and others.
func computeHMACSHA256Header(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateHMACSHA256_ValidSignature(t *testing.T) {
	secret := []byte("my-webhook-secret")
	body := []byte(`{"action":"opened","number":42}`)
	sig := computeHMACSHA256Header(secret, body)

	if !ValidateHMACSHA256(secret, body, sig) {
		t.Error("expected valid HMAC signature to pass validation")
	}
}

func TestValidateHMACSHA256_WrongSignature(t *testing.T) {
	secret := []byte("my-webhook-secret")
	body := []byte(`{"action":"opened","number":42}`)

	// Compute signature with a different secret.
	wrongSig := computeHMACSHA256Header([]byte("wrong-secret"), body)

	if ValidateHMACSHA256(secret, body, wrongSig) {
		t.Error("expected wrong HMAC signature to fail validation")
	}
}

func TestValidateHMACSHA256_TamperedBody(t *testing.T) {
	secret := []byte("my-webhook-secret")
	body := []byte(`{"action":"opened"}`)
	sig := computeHMACSHA256Header(secret, body)

	// Submit a different body but the original signature — must fail.
	tamperedBody := []byte(`{"action":"deleted"}`)
	if ValidateHMACSHA256(secret, tamperedBody, sig) {
		t.Error("expected tampered body to fail HMAC validation")
	}
}

func TestValidateHMACSHA256_EmptyHeader(t *testing.T) {
	secret := []byte("my-webhook-secret")
	body := []byte(`{"action":"opened"}`)

	if ValidateHMACSHA256(secret, body, "") {
		t.Error("expected empty signature header to fail validation")
	}
}

func TestValidateHMACSHA256_MissingPrefix(t *testing.T) {
	secret := []byte("my-webhook-secret")
	body := []byte(`{"action":"opened"}`)
	// Raw hex without the "sha256=" prefix.
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	rawHex := hex.EncodeToString(mac.Sum(nil))

	if ValidateHMACSHA256(secret, body, rawHex) {
		t.Error("expected header without sha256= prefix to fail validation")
	}
}

func TestValidateHMACSHA256_MalformedHex(t *testing.T) {
	secret := []byte("my-webhook-secret")
	body := []byte(`{"action":"opened"}`)

	if ValidateHMACSHA256(secret, body, "sha256=not-valid-hex!!!") {
		t.Error("expected malformed hex to fail validation")
	}
}

func TestValidateHMACSHA256_EmptySecret(t *testing.T) {
	// Edge case: empty secret is technically valid but unusual.
	body := []byte(`{"action":"pushed"}`)
	sig := computeHMACSHA256Header([]byte{}, body)

	if !ValidateHMACSHA256([]byte{}, body, sig) {
		t.Error("expected correct HMAC with empty secret to pass validation")
	}
}

func TestValidateHMACSHA256_EmptyBody(t *testing.T) {
	secret := []byte("my-webhook-secret")
	sig := computeHMACSHA256Header(secret, []byte{})

	if !ValidateHMACSHA256(secret, []byte{}, sig) {
		t.Error("expected correct HMAC over empty body to pass validation")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// WrapRawWebhookBody tests
// ────────────────────────────────────────────────────────────────────────────

func TestWrapRawWebhookBody_JSONBody(t *testing.T) {
	source := "github-push"
	body := []byte(`{"action":"pushed","ref":"refs/heads/main"}`)

	evt := WrapRawWebhookBody(source, body)

	if evt == nil {
		t.Fatal("expected non-nil event")
	}
	if evt.Source != source {
		t.Errorf("Source: got %q, want %q", evt.Source, source)
	}
	if evt.Type != "webhook.delivery" {
		t.Errorf("Type: got %q, want %q", evt.Type, "webhook.delivery")
	}
	if evt.TS.IsZero() {
		t.Error("TS must not be zero")
	}
	if evt.Payload.Message == "" {
		t.Error("Payload.Message must not be empty")
	}
	if evt.Payload.Data == nil {
		t.Error("Payload.Data must not be nil for JSON body")
	}
	if _, ok := evt.Payload.Data["ref"]; !ok {
		t.Error("Payload.Data must contain 'ref' key from JSON body")
	}
}

func TestWrapRawWebhookBody_NonJSONBody(t *testing.T) {
	source := "legacy-hook"
	body := []byte(`payload=value&other=123`)

	evt := WrapRawWebhookBody(source, body)

	if evt.Payload.Data == nil {
		t.Fatal("Payload.Data must not be nil even for non-JSON body")
	}
	raw, ok := evt.Payload.Data["raw"].(string)
	if !ok {
		t.Fatal("non-JSON body should be stored under 'raw' key as string")
	}
	if raw != string(body) {
		t.Errorf("raw body: got %q, want %q", raw, string(body))
	}
}

func TestWrapRawWebhookBody_EmptyBody(t *testing.T) {
	evt := WrapRawWebhookBody("empty-hook", []byte{})

	if evt == nil {
		t.Fatal("expected non-nil event for empty body")
	}
	if evt.Payload.Message == "" {
		t.Error("Payload.Message must not be empty even for empty body")
	}
	// Data may be nil or empty for an empty body — either is acceptable.
}

func TestWrapRawWebhookBody_GitHubPushFields(t *testing.T) {
	source := "github"
	body, _ := json.Marshal(map[string]interface{}{
		"ref": "refs/heads/main",
		"repository": map[string]interface{}{
			"full_name": "acme/my-repo",
		},
		"pusher": map[string]interface{}{
			"name": "alice",
		},
	})

	evt := WrapRawWebhookBody(source, body)

	// The summary should mention the source, ref, and repository.
	if !strings.Contains(evt.Payload.Message, "github") {
		t.Errorf("summary should mention source; got: %q", evt.Payload.Message)
	}
	if !strings.Contains(evt.Payload.Message, "refs/heads/main") {
		t.Errorf("summary should mention ref; got: %q", evt.Payload.Message)
	}
	if !strings.Contains(evt.Payload.Message, "acme/my-repo") {
		t.Errorf("summary should mention repository; got: %q", evt.Payload.Message)
	}
}

func TestWrapRawWebhookBody_StripeEventFields(t *testing.T) {
	source := "stripe"
	body, _ := json.Marshal(map[string]interface{}{
		"type": "payment_intent.succeeded",
		"id":   "evt_123",
	})

	evt := WrapRawWebhookBody(source, body)

	if !strings.Contains(evt.Payload.Message, "payment_intent.succeeded") {
		t.Errorf("summary should mention event type; got: %q", evt.Payload.Message)
	}
}

func TestWrapRawWebhookBody_ActionField(t *testing.T) {
	source := "github-pr"
	body, _ := json.Marshal(map[string]interface{}{
		"action": "closed",
		"number": 99,
	})

	evt := WrapRawWebhookBody(source, body)

	if !strings.Contains(evt.Payload.Message, "closed") {
		t.Errorf("summary should mention action; got: %q", evt.Payload.Message)
	}
}

func TestWrapRawWebhookBody_EnvelopeValidates(t *testing.T) {
	// Ensure the produced event passes envelope.Validate (Source, Type, TS must be set).
	source := "my-hook"
	body := []byte(`{"event":"test"}`)

	evt := WrapRawWebhookBody(source, body)

	if err := evt.Validate(); err != nil {
		t.Errorf("WrapRawWebhookBody produced an invalid event envelope: %v", err)
	}
}

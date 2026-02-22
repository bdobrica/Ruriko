// Package gateway implements built-in inbound event gateways for Gitai agents.
//
// This file implements the built-in webhook gateway (R12.4).  A webhook
// gateway receives raw HTTP POST deliveries from external services (GitHub,
// Stripe, custom tooling, etc.) on the ACP POST /events/{source} endpoint.
// The handler is responsible for:
//
//  1. Authenticating the delivery — either via the ACP bearer token (default)
//     or HMAC-SHA256 signature (X-Hub-Signature-256 header, same scheme used
//     by GitHub, Gitea, and many other webhook providers).
//  2. Wrapping the raw body into a normalised Event envelope so it can flow
//     through the same turn engine as cron events and Matrix messages.
//  3. Auto-generating a human-readable Payload.Message summary from the body
//     so the agent LLM has useful context without needing to decode raw JSON.
//
// HMAC validation uses constant-time comparison (crypto/hmac.Equal) to
// prevent timing side-channel attacks.
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
)

// ValidateHMACSHA256 checks whether sigHeader matches the HMAC-SHA256
// signature of body computed with secret.
//
// sigHeader is expected to be in the format "sha256=<lowercase-hex>", which
// is the scheme used by GitHub, Gitea, and most webhook providers.
// An empty or malformed sigHeader always returns false.
// Comparison is performed using hmac.Equal (constant-time) to prevent timing
// side-channel attacks.
func ValidateHMACSHA256(secret, body []byte, sigHeader string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	expectedHex := sigHeader[len(prefix):]
	expected, err := hex.DecodeString(expectedHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	computed := mac.Sum(nil)

	return hmac.Equal(computed, expected)
}

// WrapRawWebhookBody wraps a raw webhook POST body in a normalised Event
// envelope ready for the turn engine.
//
// The raw body is parsed as JSON and stored in Payload.Data so agents can
// access structured fields.  Non-JSON bodies are stored verbatim under the
// "raw" key.
//
// Payload.Message is auto-generated as a human-readable summary so the LLM
// gets context without needing to decode the data map.  The summary tries
// common webhook fields (action, event, type, ref, repository.full_name) but
// degrades gracefully to a generic description.
//
// The Event.Type is always "webhook.delivery" so agents can distinguish
// webhook turns from cron (cron.tick) turns in their audit or routing logic.
func WrapRawWebhookBody(source string, rawBody []byte) *envelope.Event {
	evt := &envelope.Event{
		Source: source,
		Type:   "webhook.delivery",
		TS:     time.Now().UTC(),
	}

	var data map[string]interface{}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &data); err != nil {
			// Non-JSON body — store raw text so it is still accessible.
			data = map[string]interface{}{"raw": string(rawBody)}
		}
	}

	evt.Payload = envelope.EventPayload{
		Message: summariseWebhookData(source, data),
		Data:    data,
	}

	return evt
}

// summariseWebhookData produces a concise human-readable description of the
// webhook payload.  It recognises common envelope fields that most webhook
// providers include.
func summariseWebhookData(source string, data map[string]interface{}) string {
	if len(data) == 0 {
		return fmt.Sprintf("Webhook delivery received from gateway %q.", source)
	}

	parts := []string{fmt.Sprintf("Webhook delivery from %q.", source)}

	// GitHub / Gitea / generic: "action"
	if action, ok := data["action"].(string); ok && action != "" {
		parts = append(parts, fmt.Sprintf("Action: %q.", action))
	}

	// Generic: "event"
	if evt, ok := data["event"].(string); ok && evt != "" {
		parts = append(parts, fmt.Sprintf("Event type: %q.", evt))
	}

	// GitHub push: "ref"
	if ref, ok := data["ref"].(string); ok && ref != "" {
		parts = append(parts, fmt.Sprintf("Ref: %q.", ref))
	}

	// GitHub: "repository.full_name"
	if repo, ok := data["repository"].(map[string]interface{}); ok {
		if name, ok := repo["full_name"].(string); ok && name != "" {
			parts = append(parts, fmt.Sprintf("Repository: %q.", name))
		}
	}

	// Stripe / generic: "type"
	if typ, ok := data["type"].(string); ok && typ != "" {
		parts = append(parts, fmt.Sprintf("Type: %q.", typ))
	}

	return strings.Join(parts, " ")
}

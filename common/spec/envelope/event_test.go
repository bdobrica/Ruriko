package envelope_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func validEvent() *envelope.Event {
	return &envelope.Event{
		Source: "scheduler",
		Type:   "cron.tick",
		TS:     time.Date(2026, 2, 22, 12, 0, 0, 0, time.UTC),
		Payload: envelope.EventPayload{
			Message: "Trigger scheduled check",
		},
	}
}

// ── marshal / unmarshal ───────────────────────────────────────────────────────

func TestEvent_MarshalUnmarshal_Basic(t *testing.T) {
	original := validEvent()

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: unexpected error: %v", err)
	}

	got, err := envelope.ParseEvent(data)
	if err != nil {
		t.Fatalf("ParseEvent: unexpected error: %v", err)
	}

	if got.Source != original.Source {
		t.Errorf("Source: got %q, want %q", got.Source, original.Source)
	}
	if got.Type != original.Type {
		t.Errorf("Type: got %q, want %q", got.Type, original.Type)
	}
	if !got.TS.Equal(original.TS) {
		t.Errorf("TS: got %v, want %v", got.TS, original.TS)
	}
	if got.Payload.Message != original.Payload.Message {
		t.Errorf("Payload.Message: got %q, want %q", got.Payload.Message, original.Payload.Message)
	}
}

func TestEvent_MarshalUnmarshal_WithData(t *testing.T) {
	original := &envelope.Event{
		Source: "github",
		Type:   "webhook.delivery",
		TS:     time.Now().UTC(),
		Payload: envelope.EventPayload{
			Message: "PR #42 opened",
			Data: map[string]interface{}{
				"action": "opened",
				"number": float64(42),
				"repo":   "acme/backend",
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: unexpected error: %v", err)
	}

	got, err := envelope.ParseEvent(data)
	if err != nil {
		t.Fatalf("ParseEvent: unexpected error: %v", err)
	}

	if len(got.Payload.Data) != 3 {
		t.Errorf("Payload.Data length: got %d, want 3", len(got.Payload.Data))
	}
	if got.Payload.Data["repo"] != "acme/backend" {
		t.Errorf("Payload.Data[repo]: got %v, want %q", got.Payload.Data["repo"], "acme/backend")
	}
}

func TestEvent_MarshalUnmarshal_EmptyData(t *testing.T) {
	// When Data is nil it should be omitted from JSON (omitempty).
	evt := validEvent()
	evt.Payload.Data = nil

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal: unexpected error: %v", err)
	}

	// Ensure "data" key is absent.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: unexpected error: %v", err)
	}
	payload, ok := raw["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload is not an object")
	}
	if _, present := payload["data"]; present {
		t.Errorf("expected 'data' key to be omitted when nil, but it was present")
	}
}

// ── Validate ──────────────────────────────────────────────────────────────────

func TestEvent_Validate_Valid(t *testing.T) {
	if err := validEvent().Validate(); err != nil {
		t.Errorf("Validate: unexpected error: %v", err)
	}
}

func TestEvent_Validate_EmptySource(t *testing.T) {
	evt := validEvent()
	evt.Source = ""
	if err := evt.Validate(); err == nil {
		t.Error("Validate: expected error for empty Source, got nil")
	}
}

func TestEvent_Validate_EmptyType(t *testing.T) {
	evt := validEvent()
	evt.Type = ""
	if err := evt.Validate(); err == nil {
		t.Error("Validate: expected error for empty Type, got nil")
	}
}

func TestEvent_Validate_ZeroTS(t *testing.T) {
	evt := validEvent()
	evt.TS = time.Time{}
	if err := evt.Validate(); err == nil {
		t.Error("Validate: expected error for zero TS, got nil")
	}
}

func TestEvent_Validate_Nil(t *testing.T) {
	var evt *envelope.Event
	if err := evt.Validate(); err == nil {
		t.Error("Validate: expected error for nil event, got nil")
	}
}

// ── ParseEvent ────────────────────────────────────────────────────────────────

func TestParseEvent_MalformedJSON(t *testing.T) {
	_, err := envelope.ParseEvent([]byte(`{not json`))
	if err == nil {
		t.Error("ParseEvent: expected error for malformed JSON, got nil")
	}
}

func TestParseEvent_MissingSource(t *testing.T) {
	data := []byte(`{"type":"cron.tick","ts":"2026-02-22T12:00:00Z","payload":{"message":"hi"}}`)
	_, err := envelope.ParseEvent(data)
	if err == nil {
		t.Error("ParseEvent: expected error for missing source, got nil")
	}
}

func TestParseEvent_MissingType(t *testing.T) {
	data := []byte(`{"source":"scheduler","ts":"2026-02-22T12:00:00Z","payload":{"message":"hi"}}`)
	_, err := envelope.ParseEvent(data)
	if err == nil {
		t.Error("ParseEvent: expected error for missing type, got nil")
	}
}

func TestParseEvent_MissingTS(t *testing.T) {
	data := []byte(`{"source":"scheduler","type":"cron.tick","payload":{"message":"hi"}}`)
	_, err := envelope.ParseEvent(data)
	if err == nil {
		t.Error("ParseEvent: expected error for missing/zero ts, got nil")
	}
}

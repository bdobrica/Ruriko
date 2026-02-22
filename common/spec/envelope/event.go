// Package envelope defines the event envelope types used for inbound gateway
// event ingress. Gateways POST a normalised Event to the agent's local ACP
// endpoint (POST /events/{source}) when they receive an inbound event.
package envelope

import (
	"encoding/json"
	"fmt"
	"time"
)

// Event is the normalised envelope that gateways POST to an agent's ACP
// endpoint. It carries a machine-readable source/type classification plus a
// payload that is forwarded to the agent's LLM turn engine.
type Event struct {
	// Source is the gateway name as declared in the Gosuto config.
	// It must match one of the agent's configured gateways.
	Source string `json:"source"`

	// Type classifies the event (e.g. "cron.tick", "webhook.delivery").
	// Consumers may use this to filter or format the event payload.
	Type string `json:"type"`

	// TS is the UTC timestamp at which the event was generated.
	TS time.Time `json:"ts"`

	// Payload carries the human-readable message and optional structured data.
	Payload EventPayload `json:"payload"`
}

// EventPayload holds the content of an inbound event.
type EventPayload struct {
	// Message is a human-readable description of the event that is handed
	// directly to the agent's LLM as the user turn. Required for LLM-driven
	// agents; may be auto-generated from Data when empty.
	Message string `json:"message"`

	// Data holds optional structured metadata for the event. It is not sent
	// to the LLM directly but may be referenced in prompt templates or logged
	// for debugging purposes.
	Data map[string]interface{} `json:"data,omitempty"`
}

// Validate checks that an Event is structurally valid.
// It returns a descriptive error if any invariant is violated, or nil if the
// event may be safely dispatched to an agent.
func (e *Event) Validate() error {
	if e == nil {
		return fmt.Errorf("event must not be nil")
	}
	if e.Source == "" {
		return fmt.Errorf("source must not be empty")
	}
	if e.Type == "" {
		return fmt.Errorf("type must not be empty")
	}
	if e.TS.IsZero() {
		return fmt.Errorf("ts must not be zero")
	}
	return nil
}

// ParseEvent decodes a JSON-encoded Event and validates it.
// It is the canonical entry point for deserialising events from ACP bodies.
func ParseEvent(data []byte) (*Event, error) {
	var evt Event
	if err := json.Unmarshal(data, &evt); err != nil {
		return nil, fmt.Errorf("envelope parse: %w", err)
	}
	if err := evt.Validate(); err != nil {
		return nil, fmt.Errorf("envelope validate: %w", err)
	}
	return &evt, nil
}

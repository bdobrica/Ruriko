// Package workflow provides deterministic workflow-trigger parsing,
// trust gating, schema checks, and execution context primitives.
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// ErrorCode is a deterministic classifier for workflow foundation errors.
type ErrorCode string

const (
	CodeInvalidProtocolMessage ErrorCode = "invalid_protocol_message"
	CodeSchemaValidation       ErrorCode = "schema_validation"
	CodeTrustMismatch          ErrorCode = "trust_mismatch"
)

// Error represents a deterministic workflow foundation error.
type Error struct {
	Code       ErrorCode
	ProtocolID string
	Message    string
	Cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.ProtocolID != "" {
		return fmt.Sprintf("workflow protocol %s: %s", e.ProtocolID, e.Message)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// HasCode reports whether err is a workflow Error with the given code.
func HasCode(err error, code ErrorCode) bool {
	var werr *Error
	if !errors.As(err, &werr) {
		return false
	}
	return werr.Code == code
}

// InboundProtocolMatch is a successful protocol-trigger match and parse result.
type InboundProtocolMatch struct {
	Protocol gosutospec.WorkflowProtocol
	Payload  map[string]interface{}
	Prefix   string
	RawText  string
}

// ExecutionContext is the deterministic state container for workflow execution.
type ExecutionContext struct {
	TraceID    string
	RoomID     string
	SenderMXID string
	ProtocolID string
	Input      map[string]interface{}
	State      map[string]interface{}
}

// NewExecutionContext builds an execution context from a matched inbound protocol.
func NewExecutionContext(traceID, roomID, senderMXID string, match *InboundProtocolMatch) *ExecutionContext {
	if match == nil {
		return &ExecutionContext{
			TraceID:    traceID,
			RoomID:     roomID,
			SenderMXID: senderMXID,
			State:      make(map[string]interface{}),
		}
	}
	state := make(map[string]interface{}, 2)
	for k, v := range match.Payload {
		state[k] = v
	}
	return &ExecutionContext{
		TraceID:    traceID,
		RoomID:     roomID,
		SenderMXID: senderMXID,
		ProtocolID: match.Protocol.ID,
		Input:      match.Payload,
		State:      state,
	}
}

// MatchInboundProtocol parses a matrix message against configured protocol
// triggers, validates schema refs/payload shape, and enforces trusted peer
// gating using trust.trustedPeers.
//
// Returns (nil, nil) when no protocol trigger matches the message.
func MatchInboundProtocol(cfg *gosutospec.Config, roomID, senderMXID, text string) (*InboundProtocolMatch, error) {
	if cfg == nil {
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" || len(cfg.Workflow.Protocols) == 0 {
		return nil, nil
	}

	for _, protocol := range cfg.Workflow.Protocols {
		if protocol.Trigger.Type != "matrix.protocol_message" {
			continue
		}
		prefix := strings.TrimSpace(protocol.Trigger.Prefix)
		if prefix == "" {
			continue
		}

		payload, matched, err := parseProtocolMessage(prefix, trimmedText)
		if err != nil {
			return nil, &Error{
				Code:       CodeInvalidProtocolMessage,
				ProtocolID: protocol.ID,
				Message:    fmt.Sprintf("invalid protocol payload for prefix %q", prefix),
				Cause:      err,
			}
		}
		if !matched {
			continue
		}

		if err := validateProtocolInputSchema(cfg, protocol, payload); err != nil {
			return nil, err
		}

		if !isTrustedProtocolPeer(cfg.Trust, senderMXID, roomID, protocol.ID) {
			return nil, &Error{
				Code:       CodeTrustMismatch,
				ProtocolID: protocol.ID,
				Message: fmt.Sprintf(
					"protocol trigger rejected by trustedPeers (sender=%s room=%s protocol=%s)",
					senderMXID,
					roomID,
					protocol.ID,
				),
			}
		}

		return &InboundProtocolMatch{
			Protocol: protocol,
			Payload:  payload,
			Prefix:   prefix,
			RawText:  trimmedText,
		}, nil
	}

	return nil, nil
}

func parseProtocolMessage(prefix, text string) (map[string]interface{}, bool, error) {
	if !strings.HasPrefix(text, prefix) {
		return nil, false, nil
	}
	remainder := strings.TrimSpace(text[len(prefix):])
	if remainder == "" {
		return nil, true, fmt.Errorf("missing JSON payload after protocol prefix")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(remainder), &payload); err != nil {
		return nil, true, fmt.Errorf("decode JSON payload: %w", err)
	}
	if payload == nil {
		return nil, true, fmt.Errorf("payload must be a JSON object")
	}
	return payload, true, nil
}

func isTrustedProtocolPeer(trust gosutospec.Trust, senderMXID, roomID, protocolID string) bool {
	for _, peer := range trust.TrustedPeers {
		if peer.MXID != senderMXID || peer.RoomID != roomID {
			continue
		}
		for _, protocol := range peer.Protocols {
			if protocol == protocolID {
				return true
			}
		}
	}
	return false
}

func validateProtocolInputSchema(cfg *gosutospec.Config, protocol gosutospec.WorkflowProtocol, payload map[string]interface{}) error {
	if strings.TrimSpace(protocol.InputSchemaRef) == "" {
		return nil
	}

	schemaAny, ok := cfg.Workflow.Schemas.Definitions[protocol.InputSchemaRef]
	if !ok {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocol.ID,
			Message:    fmt.Sprintf("input schema ref %s not found", protocol.InputSchemaRef),
		}
	}

	schema, ok := schemaAny.(map[string]interface{})
	if !ok {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocol.ID,
			Message:    fmt.Sprintf("schema %s is not an object", protocol.InputSchemaRef),
		}
	}

	if err := validatePayloadAgainstSchema(schema, payload); err != nil {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocol.ID,
			Message:    err.Error(),
		}
	}
	return nil
}

func validatePayloadAgainstSchema(schema, payload map[string]interface{}) error {
	typeName, _ := schema["type"].(string)
	if typeName != "" && typeName != "object" {
		return fmt.Errorf("schema type %q is not supported for protocol input", typeName)
	}

	if requiredAny, ok := schema["required"]; ok {
		required, ok := requiredAny.([]interface{})
		if !ok {
			return fmt.Errorf("schema.required must be an array")
		}
		for _, raw := range required {
			key, ok := raw.(string)
			if !ok {
				return fmt.Errorf("schema.required entries must be strings")
			}
			if _, exists := payload[key]; !exists {
				return fmt.Errorf("payload missing required field %q", key)
			}
		}
	}

	propertiesAny, hasProperties := schema["properties"]
	if !hasProperties {
		return nil
	}
	properties, ok := propertiesAny.(map[string]interface{})
	if !ok {
		return fmt.Errorf("schema.properties must be an object")
	}

	for key, propertySchemaAny := range properties {
		value, exists := payload[key]
		if !exists {
			continue
		}
		propertySchema, ok := propertySchemaAny.(map[string]interface{})
		if !ok {
			continue
		}
		expectedType, _ := propertySchema["type"].(string)
		if expectedType == "" {
			continue
		}
		if !matchesJSONSchemaType(expectedType, value) {
			return fmt.Errorf("payload field %q has wrong type; expected %s", key, expectedType)
		}
	}

	return nil
}

func matchesJSONSchemaType(expected string, value interface{}) bool {
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		if !ok {
			return false
		}
		return number == float64(int64(number))
	case "array":
		_, ok := value.([]interface{})
		return ok
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

// RunStepWithRetryThenRefuse executes a workflow step with deterministic retry
// semantics for schema-sensitive steps (parse_input, summarize).
//
// Behavior:
//   - parse_input / summarize: retries up to retries+1 total attempts.
//   - all other step types: executes exactly once.
//   - after retry exhaustion for parse_input/summarize: returns a
//     CodeSchemaValidation error (fail-safe refuse).
func RunStepWithRetryThenRefuse(protocolID, stepType string, retries int, run func() error) error {
	if run == nil {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocolID,
			Message:    fmt.Sprintf("step %s execution failed: runner is nil", stepType),
		}
	}

	if retries < 0 {
		retries = 0
	}

	attempts := 1
	if isSchemaRetryStep(stepType) {
		attempts += retries
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := run()
		if err == nil {
			return nil
		}
		lastErr = err
	}

	if !isSchemaRetryStep(stepType) {
		return lastErr
	}

	return &Error{
		Code:       CodeSchemaValidation,
		ProtocolID: protocolID,
		Message:    fmt.Sprintf("step %s failed after %d attempt(s)", stepType, attempts),
		Cause:      lastErr,
	}
}

func isSchemaRetryStep(stepType string) bool {
	switch strings.TrimSpace(stepType) {
	case "parse_input", "summarize":
		return true
	default:
		return false
	}
}

package workflow

import (
	"fmt"
	"strings"
	"testing"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

func baseConfig() *gosutospec.Config {
	return &gosutospec.Config{
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"*"},
			AllowedSenders: []string{"*"},
			TrustedPeers: []gosutospec.TrustedPeer{
				{
					MXID:      "@kairo:example.com",
					RoomID:    "!kumo-room:example.com",
					Protocols: []string{"kairo.news.request.v1"},
				},
			},
		},
		Workflow: gosutospec.Workflow{
			Schemas: gosutospec.WorkflowSchemas{
				Definitions: map[string]interface{}{
					"kairoNewsRequest": map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"run_id", "tickers"},
						"properties": map[string]interface{}{
							"run_id":  map[string]interface{}{"type": "integer"},
							"tickers": map[string]interface{}{"type": "array"},
						},
					},
				},
			},
			Protocols: []gosutospec.WorkflowProtocol{
				{
					ID:             "kairo.news.request.v1",
					Trigger:        gosutospec.WorkflowTrigger{Type: "matrix.protocol_message", Prefix: "KAIRO_NEWS_REQUEST"},
					InputSchemaRef: "kairoNewsRequest",
				},
			},
		},
	}
}

func TestMatchInboundProtocol_TrustedPeer_AllowsExecution(t *testing.T) {
	cfg := baseConfig()
	text := `KAIRO_NEWS_REQUEST {"run_id": 42, "tickers": ["AAPL", "MSFT"]}`

	match, err := MatchInboundProtocol(cfg, "!kumo-room:example.com", "@kairo:example.com", text)
	if err != nil {
		t.Fatalf("MatchInboundProtocol returned error: %v", err)
	}
	if match == nil {
		t.Fatal("expected protocol match, got nil")
	}
	if match.Protocol.ID != "kairo.news.request.v1" {
		t.Fatalf("protocol id = %q, want kairo.news.request.v1", match.Protocol.ID)
	}
	if got := match.Payload["run_id"]; got != float64(42) {
		t.Fatalf("run_id = %#v, want 42", got)
	}
}

func TestMatchInboundProtocol_UntrustedPeer_BlockedWithTrustMismatch(t *testing.T) {
	cfg := baseConfig()
	text := `KAIRO_NEWS_REQUEST {"run_id": 42, "tickers": ["AAPL"]}`

	match, err := MatchInboundProtocol(cfg, "!kumo-room:example.com", "@eve:example.com", text)
	if match != nil {
		t.Fatalf("expected no match on trust mismatch, got %+v", match)
	}
	if err == nil {
		t.Fatal("expected trust mismatch error, got nil")
	}
	if !HasCode(err, CodeTrustMismatch) {
		t.Fatalf("expected trust mismatch code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "trustedPeers") {
		t.Fatalf("error should mention trustedPeers, got: %v", err)
	}
}

func TestMatchInboundProtocol_NonProtocolMessage_NoMatch(t *testing.T) {
	cfg := baseConfig()
	match, err := MatchInboundProtocol(cfg, "!kumo-room:example.com", "@kairo:example.com", "hello there")
	if err != nil {
		t.Fatalf("expected nil error for non-protocol message, got: %v", err)
	}
	if match != nil {
		t.Fatalf("expected nil match for non-protocol message, got: %+v", match)
	}
}

func TestMatchInboundProtocol_InvalidPayload_ReturnsDeterministicError(t *testing.T) {
	cfg := baseConfig()
	text := `KAIRO_NEWS_REQUEST not-json`

	match, err := MatchInboundProtocol(cfg, "!kumo-room:example.com", "@kairo:example.com", text)
	if match != nil {
		t.Fatalf("expected nil match on parse error, got: %+v", match)
	}
	if err == nil {
		t.Fatal("expected invalid payload error, got nil")
	}
	if !HasCode(err, CodeInvalidProtocolMessage) {
		t.Fatalf("expected invalid protocol message code, got: %v", err)
	}
}

func TestMatchInboundProtocol_SchemaValidationFailure(t *testing.T) {
	cfg := baseConfig()
	text := `KAIRO_NEWS_REQUEST {"tickers": ["AAPL"]}`

	match, err := MatchInboundProtocol(cfg, "!kumo-room:example.com", "@kairo:example.com", text)
	if match != nil {
		t.Fatalf("expected nil match on schema error, got: %+v", match)
	}
	if err == nil {
		t.Fatal("expected schema validation error, got nil")
	}
	if !HasCode(err, CodeSchemaValidation) {
		t.Fatalf("expected schema validation code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("error should mention missing required field, got: %v", err)
	}
}

func TestNewExecutionContext_FromMatch(t *testing.T) {
	match := &InboundProtocolMatch{
		Protocol: gosutospec.WorkflowProtocol{ID: "kairo.news.request.v1"},
		Payload: map[string]interface{}{
			"run_id": float64(7),
		},
	}
	ctx := NewExecutionContext("trace-1", "!room:example.com", "@kairo:example.com", match)
	if ctx.ProtocolID != "kairo.news.request.v1" {
		t.Fatalf("ProtocolID = %q, want kairo.news.request.v1", ctx.ProtocolID)
	}
	if got := ctx.Input["run_id"]; got != float64(7) {
		t.Fatalf("Input.run_id = %#v, want 7", got)
	}
	if got := ctx.State["run_id"]; got != float64(7) {
		t.Fatalf("State.run_id = %#v, want 7", got)
	}
}

func TestRunStepWithRetryThenRefuse_ParseInput_SucceedsAfterRetry(t *testing.T) {
	attempts := 0
	err := RunStepWithRetryThenRefuse("kairo.news.request.v1", "parse_input", 2, func() error {
		attempts++
		if attempts < 2 {
			return fmt.Errorf("schema parse failed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunStepWithRetryThenRefuse returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRunStepWithRetryThenRefuse_Summarize_FailsAfterExhaustion(t *testing.T) {
	attempts := 0
	err := RunStepWithRetryThenRefuse("kairo.news.request.v1", "summarize", 1, func() error {
		attempts++
		return fmt.Errorf("summary schema mismatch")
	})
	if err == nil {
		t.Fatal("expected error after retry exhaustion, got nil")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !HasCode(err, CodeSchemaValidation) {
		t.Fatalf("expected schema validation error code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "failed after 2 attempt(s)") {
		t.Fatalf("expected retry exhaustion message, got: %v", err)
	}
}

func TestRunStepWithRetryThenRefuse_NonSchemaStep_NoRetry(t *testing.T) {
	attempts := 0
	err := RunStepWithRetryThenRefuse("kairo.news.request.v1", "tool", 3, func() error {
		attempts++
		return fmt.Errorf("tool failed")
	})
	if err == nil {
		t.Fatal("expected error for failed tool step, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if HasCode(err, CodeSchemaValidation) {
		t.Fatalf("tool step error should not be reclassified as schema validation: %v", err)
	}
}

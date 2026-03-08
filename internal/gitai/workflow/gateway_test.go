package workflow

import (
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

func TestMatchGatewayProtocol_MatchesByType(t *testing.T) {
	cfg := &gosutospec.Config{
		Workflow: gosutospec.Workflow{
			Protocols: []gosutospec.WorkflowProtocol{
				{
					ID: "gateway.tick.v1",
					Trigger: gosutospec.WorkflowTrigger{
						Type:   "gateway.event",
						Prefix: "cron.tick",
					},
				},
			},
		},
	}

	evt := &envelope.Event{Source: "scheduler", Type: "cron.tick", TS: time.Now()}
	match, err := MatchGatewayProtocol(cfg, evt)
	if err != nil {
		t.Fatalf("MatchGatewayProtocol() unexpected error: %v", err)
	}
	if match == nil {
		t.Fatal("MatchGatewayProtocol() expected match")
	}
	if match.Protocol.ID != "gateway.tick.v1" {
		t.Fatalf("matched protocol = %q, want gateway.tick.v1", match.Protocol.ID)
	}
	gatewayAny, ok := match.Payload["gateway"]
	if !ok {
		t.Fatal("expected payload.gateway context")
	}
	gatewayCtx, ok := gatewayAny.(map[string]interface{})
	if !ok {
		t.Fatalf("payload.gateway type = %T, want map[string]interface{}", gatewayAny)
	}
	if got := gatewayCtx["source"]; got != "scheduler" {
		t.Fatalf("payload.gateway.source = %#v, want scheduler", got)
	}
	if got := gatewayCtx["event_type"]; got != "cron.tick" {
		t.Fatalf("payload.gateway.event_type = %#v, want cron.tick", got)
	}
}

func TestMatchGatewayProtocol_NoMatchForType(t *testing.T) {
	cfg := &gosutospec.Config{
		Workflow: gosutospec.Workflow{
			Protocols: []gosutospec.WorkflowProtocol{
				{
					ID: "gateway.tick.v1",
					Trigger: gosutospec.WorkflowTrigger{
						Type:   "gateway.event",
						Prefix: "cron.tick",
					},
				},
			},
		},
	}

	evt := &envelope.Event{Source: "scheduler", Type: "webhook.push", TS: time.Now()}
	match, err := MatchGatewayProtocol(cfg, evt)
	if err != nil {
		t.Fatalf("MatchGatewayProtocol() unexpected error: %v", err)
	}
	if match != nil {
		t.Fatal("MatchGatewayProtocol() expected no match")
	}
}

func TestMatchGatewayProtocol_SchemaValidationErrorIsReturned(t *testing.T) {
	cfg := &gosutospec.Config{
		Workflow: gosutospec.Workflow{
			Schemas: gosutospec.WorkflowSchemas{
				Definitions: map[string]interface{}{
					"gatewayInput": map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"message"},
					},
				},
			},
			Protocols: []gosutospec.WorkflowProtocol{
				{
					ID:             "gateway.tick.v1",
					InputSchemaRef: "gatewayInput",
					Trigger: gosutospec.WorkflowTrigger{
						Type:   "gateway.event",
						Prefix: "cron.tick",
					},
				},
			},
		},
	}

	evt := &envelope.Event{Source: "scheduler", Type: "cron.tick", TS: time.Now()}
	match, err := MatchGatewayProtocol(cfg, evt)
	if match != nil {
		t.Fatalf("expected nil match on schema validation error, got %+v", match)
	}
	if err == nil {
		t.Fatal("expected schema validation error, got nil")
	}
	if !HasCode(err, CodeSchemaValidation) {
		t.Fatalf("expected schema validation error code, got: %v", err)
	}
}

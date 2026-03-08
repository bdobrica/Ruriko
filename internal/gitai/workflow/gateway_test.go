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
	match, ok := MatchGatewayProtocol(cfg, evt)
	if !ok || match == nil {
		t.Fatal("MatchGatewayProtocol() expected match")
	}
	if match.Protocol.ID != "gateway.tick.v1" {
		t.Fatalf("matched protocol = %q, want gateway.tick.v1", match.Protocol.ID)
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
	if _, ok := MatchGatewayProtocol(cfg, evt); ok {
		t.Fatal("MatchGatewayProtocol() expected no match")
	}
}

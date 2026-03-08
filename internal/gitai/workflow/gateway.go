package workflow

import (
	"strings"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// GatewayProtocolMatch is a successful workflow protocol match for a gateway event.
// MatchGatewayProtocol matches gateway events to workflow protocols using
// trigger.type="gateway.event" and trigger.prefix as the exact event type.
//
// A protocol with empty trigger.prefix matches all gateway event types.
func MatchGatewayProtocol(cfg *gosutospec.Config, evt *envelope.Event) (*InboundProtocolMatch, error) {
	if cfg == nil || evt == nil || len(cfg.Workflow.Protocols) == 0 {
		return nil, nil
	}

	for _, protocol := range cfg.Workflow.Protocols {
		if protocol.Trigger.Type != "gateway.event" {
			continue
		}
		prefix := strings.TrimSpace(protocol.Trigger.Prefix)
		if prefix != "" && prefix != evt.Type {
			continue
		}

		ts := evt.TS.Format("2006-01-02T15:04:05.999999999Z07:00")
		payload := map[string]interface{}{
			"source": evt.Source,
			"type":   evt.Type,
			"ts":     ts,
			"payload": map[string]interface{}{
				"message": evt.Payload.Message,
				"data":    evt.Payload.Data,
			},
			"gateway": map[string]interface{}{
				"source":     evt.Source,
				"event_type": evt.Type,
				"timestamp":  ts,
				"message":    evt.Payload.Message,
				"data":       evt.Payload.Data,
			},
		}
		if evt.Payload.Message != "" {
			payload["message"] = evt.Payload.Message
		}
		if len(evt.Payload.Data) > 0 {
			payload["data"] = evt.Payload.Data
		}
		if err := validateProtocolInputSchema(cfg, protocol, payload); err != nil {
			return nil, err
		}
		return &InboundProtocolMatch{
			Protocol:          protocol,
			Payload:           payload,
			SchemaDefinitions: cloneSchemaDefinitions(cfg.Workflow.Schemas.Definitions),
			Prefix:            prefix,
			RawText:           "gateway:" + evt.Source + "/" + evt.Type,
		}, nil
	}

	return nil, nil
}

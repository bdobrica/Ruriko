package workflow

import (
	"fmt"
	"strings"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// GatewayProtocolMatch is a successful workflow protocol match for a gateway event.
type GatewayProtocolMatch struct {
	Protocol gosutospec.WorkflowProtocol
	Payload  map[string]interface{}
}

// MatchGatewayProtocol matches gateway events to workflow protocols using
// trigger.type="gateway.event" and trigger.prefix as the exact event type.
//
// A protocol with empty trigger.prefix matches all gateway event types.
func MatchGatewayProtocol(cfg *gosutospec.Config, evt *envelope.Event) (*InboundProtocolMatch, bool) {
	if cfg == nil || evt == nil || len(cfg.Workflow.Protocols) == 0 {
		return nil, false
	}

	for _, protocol := range cfg.Workflow.Protocols {
		if protocol.Trigger.Type != "gateway.event" {
			continue
		}
		prefix := strings.TrimSpace(protocol.Trigger.Prefix)
		if prefix != "" && prefix != evt.Type {
			continue
		}

		payload := map[string]interface{}{
			"source": evt.Source,
			"type":   evt.Type,
			"ts":     evt.TS.Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		if evt.Payload.Message != "" {
			payload["message"] = evt.Payload.Message
		}
		if len(evt.Payload.Data) > 0 {
			payload["data"] = evt.Payload.Data
		}
		if err := validateProtocolInputSchema(cfg, protocol, payload); err != nil {
			return nil, false
		}
		return &InboundProtocolMatch{
			Protocol: protocol,
			Payload:  payload,
			Prefix:   prefix,
			RawText:  fmt.Sprintf("gateway:%s/%s", evt.Source, evt.Type),
		}, true
	}

	return nil, false
}

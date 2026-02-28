package app

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const workflowIntegrationCanonicalYAML = `apiVersion: gosuto/v1
metadata:
  name: kairo
  canonicalName: kairo
trust:
  allowedRooms:
    - "!chat-room:example.com"
  allowedSenders:
    - "*"
  adminRoom: "!admin-room:example.com"
persona:
  llmProvider: openai
  model: gpt-4o-mini
  systemPrompt: "You are Kairo."
`

const workflowIntegrationTrustGateYAML = `apiVersion: gosuto/v1
metadata:
  name: kumo
  canonicalName: kumo
trust:
  allowedRooms:
    - "!kumo-room:example.com"
  allowedSenders:
    - "*"
  adminRoom: "!admin-room:example.com"
  trustedPeers:
    - mxid: "@kairo:example.com"
      roomId: "!kumo-room:example.com"
      protocols:
        - "kairo.news.request.v1"
workflow:
  schemas:
    kairoNewsRequest:
      type: object
      required: [run_id, tickers]
      properties:
        run_id:
          type: integer
        tickers:
          type: array
  protocols:
    - id: "kairo.news.request.v1"
      trigger:
        type: "matrix.protocol_message"
        prefix: "KAIRO_NEWS_REQUEST"
      inputSchemaRef: "kairoNewsRequest"
persona:
  llmProvider: openai
  model: gpt-4o-mini
  systemPrompt: "You are Kumo."
`

func makeMessageEvent(roomID, sender, eventID, body string) *event.Event {
	return &event.Event{
		ID:     id.EventID(eventID),
		RoomID: id.RoomID(roomID),
		Sender: id.UserID(sender),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    body,
			},
		},
	}
}

func TestHandleMessage_CanonicalAgent_NoHardcodedBranch_UsesTurnPipeline(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationCanonicalYAML, prov)

	evt := makeMessageEvent(
		"!chat-room:example.com",
		"@saito:example.com",
		"$evt-canonical",
		"Saito scheduled trigger: run cycle",
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(3 * time.Second); !ok {
		t.Fatal("expected LLM call for canonical agent message; got none")
	}
}

func TestHandleMessage_ProtocolFromUntrustedPeer_RejectedWithAllowedSendersWildcard(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationTrustGateYAML, prov)

	evt := makeMessageEvent(
		"!kumo-room:example.com",
		"@mallory:example.com",
		"$evt-untrusted",
		`KAIRO_NEWS_REQUEST {"run_id": 42, "tickers": ["AAPL"]}`,
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(300 * time.Millisecond); ok {
		t.Fatal("LLM should not be called for protocol message from untrusted peer")
	}

	var count int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM turn_log").Scan(&count); err != nil {
		t.Fatalf("count turn_log: %v", err)
	}
	if count != 0 {
		t.Fatalf("turn_log count = %d, want 0 when trust gate rejects inbound protocol message", count)
	}
}

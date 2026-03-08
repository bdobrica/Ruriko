package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
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

const workflowIntegrationRecipientGateYAML = `apiVersion: gosuto/v1
metadata:
  name: kumo
  canonicalName: kumo
trust:
  allowedRooms:
    - "!shared-admin-room:example.com"
  allowedSenders:
    - "*"
  adminRoom: "!shared-admin-room:example.com"
  trustedPeers:
    - mxid: "@kairo:example.com"
      roomId: "!shared-admin-room:example.com"
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

const workflowIntegrationDeterministicStepYAML = `apiVersion: gosuto/v1
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
      required: [run_id]
      properties:
        run_id:
          type: integer
  protocols:
    - id: "kairo.news.request.v1"
      trigger:
        type: "matrix.protocol_message"
        prefix: "KAIRO_NEWS_REQUEST"
      inputSchemaRef: "kairoNewsRequest"
      steps:
        - type: "parse_input"
        - type: "persist"
          persistKey: "last_run_id"
          persistValue: "{{input.run_id}}"
persona:
  llmProvider: openai
  model: gpt-4o-mini
  systemPrompt: "You are Kumo."
`

const workflowIntegrationSchemaStrictYAML = `apiVersion: gosuto/v1
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
      steps:
        - type: "parse_input"
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

func TestShouldSendTurnErrorReply_TrustedPeerLLMError_IsSuppressed(t *testing.T) {
	ldr := gosuto.New()
	if err := ldr.Apply([]byte(workflowIntegrationTrustGateYAML)); err != nil {
		t.Fatalf("apply gosuto: %v", err)
	}
	cfg := ldr.Config()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if shouldSendTurnErrorReply(cfg, "@kairo:example.com", fmt.Errorf("LLM provider not configured")) {
		t.Fatal("trusted peer LLM provider errors should not send room replies")
	}
	if shouldSendTurnErrorReply(cfg, "@kairo:example.com", fmt.Errorf("LLM call failed: openai: rate limit")) {
		t.Fatal("trusted peer LLM call failures should not send room replies")
	}
}

func TestShouldSendTurnErrorReply_OperatorLLMError_IsNotSuppressed(t *testing.T) {
	ldr := gosuto.New()
	if err := ldr.Apply([]byte(workflowIntegrationTrustGateYAML)); err != nil {
		t.Fatalf("apply gosuto: %v", err)
	}
	cfg := ldr.Config()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if !shouldSendTurnErrorReply(cfg, "@operator:example.com", fmt.Errorf("LLM provider not configured")) {
		t.Fatal("non-peer sender should still receive turn error replies")
	}
}
func TestHandleMessage_KairoReceivesDirectedFromSaito_ProcessesExactlyOneLLMCall(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationCanonicalYAML, prov)

	evt := makeMessageEvent(
		"!chat-room:example.com",
		"@saito:example.com",
		"$evt-kairo-directed",
		`Hey kairo, KAIRO_TRIGGER {"trigger":"scheduled","source":"saito","event":"cron.tick"}`,
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(3 * time.Second); !ok {
		t.Fatal("expected Kairo to call LLM for directed message from Saito")
	}
	if _, ok := prov.waitForCall(300 * time.Millisecond); ok {
		t.Fatal("expected exactly one LLM call for a single directed inbound message")
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

func TestHandleMessage_ProtocolLikeMessage_NotTargetedForAgent_IsIgnored(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationRecipientGateYAML, prov)

	evt := makeMessageEvent(
		"!shared-admin-room:example.com",
		"@saito:example.com",
		"$evt-not-targeted",
		`Hey saito, KAIRO_TRIGGER {"trigger": "scheduled", "source": "saito", "event": "cron.tick"}`,
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(300 * time.Millisecond); ok {
		t.Fatal("LLM should not be called for protocol-like message that does not match this agent's protocols")
	}

	var count int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM turn_log").Scan(&count); err != nil {
		t.Fatalf("count turn_log: %v", err)
	}
	if count != 0 {
		t.Fatalf("turn_log count = %d, want 0 for non-target protocol-like message", count)
	}
}

func TestHandleMessage_ProtocolLikeMessage_TargetedForAgent_StillProcesses(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationRecipientGateYAML, prov)

	evt := makeMessageEvent(
		"!shared-admin-room:example.com",
		"@kairo:example.com",
		"$evt-targeted",
		`Hey @kumo, KAIRO_NEWS_REQUEST {"run_id": 42, "tickers": ["AAPL"]}`,
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(3 * time.Second); !ok {
		t.Fatal("expected LLM call for targeted protocol message")
	}

	var count int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM turn_log").Scan(&count); err != nil {
		t.Fatalf("count turn_log: %v", err)
	}
	if count != 1 {
		t.Fatalf("turn_log count = %d, want 1 for targeted protocol message", count)
	}
}

func TestHandleMessage_ProtocolWithDeterministicSteps_DoesNotInvokeLLM(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationDeterministicStepYAML, prov)

	evt := makeMessageEvent(
		"!kumo-room:example.com",
		"@kairo:example.com",
		"$evt-deterministic",
		`KAIRO_NEWS_REQUEST {"run_id": 42}`,
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(300 * time.Millisecond); ok {
		t.Fatal("LLM should not be called when deterministic workflow steps handle the protocol")
	}

	var count int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM turn_log").Scan(&count); err != nil {
		t.Fatalf("count turn_log: %v", err)
	}
	if count != 1 {
		t.Fatalf("turn_log count = %d, want 1 for deterministic workflow execution", count)
	}
}

func TestHandleMessage_InvalidProtocolPayload_DoesNotFallBackToLLM(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationSchemaStrictYAML, prov)

	evt := makeMessageEvent(
		"!kumo-room:example.com",
		"@kairo:example.com",
		"$evt-invalid-protocol-payload",
		`KAIRO_NEWS_REQUEST {"run_id": 42}`,
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(300 * time.Millisecond); ok {
		t.Fatal("LLM should not be called when protocol payload fails schema validation")
	}

	var count int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM turn_log").Scan(&count); err != nil {
		t.Fatalf("count turn_log: %v", err)
	}
	if count != 0 {
		t.Fatalf("turn_log count = %d, want 0 when invalid protocol payload is rejected", count)
	}
}

func TestHandleMessage_FreeFormMessage_StillProcessesWithWildcardTrust(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationRecipientGateYAML, prov)

	evt := makeMessageEvent(
		"!shared-admin-room:example.com",
		"@operator:example.com",
		"$evt-free-form",
		"Can you summarize market sentiment for today?",
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(3 * time.Second); !ok {
		t.Fatal("expected LLM call for free-form message that is not protocol-like")
	}
}

func TestHandleMessage_LegacyDirectedPrefix_IsNotSpecialCased(t *testing.T) {
	prov := newCapturingLLM("")
	a := newEventApp(t, workflowIntegrationRecipientGateYAML, prov)

	evt := makeMessageEvent(
		"!shared-admin-room:example.com",
		"@operator:example.com",
		"$evt-legacy-directed",
		"Hey, kumo, KAIRO_NEWS_REQUEST {\"run_id\": 42, \"tickers\": [\"AAPL\"]}",
	)
	a.handleMessage(context.Background(), evt)

	if _, ok := prov.waitForCall(3 * time.Second); !ok {
		t.Fatal("expected LLM call: legacy 'Hey, <agent>, ...' must not be interpreted as directed addressing")
	}
}

func TestRunTurn_LLMHardLimit_TriggersTerminationAndSkipsProviderCall(t *testing.T) {
	prov := newCapturingLLM("ok")
	a := newEventApp(t, workflowIntegrationCanonicalYAML, prov)
	a.cfg = &Config{AgentID: "kairo", LLMCallHardLimit: 1, LLM: LLMConfig{Provider: "openai"}}

	terminated := false
	exitCode := 0
	a.terminateProcess = func(code int) {
		terminated = true
		exitCode = code
	}

	if _, _, err := a.runTurn(context.Background(), "!chat-room:example.com", "@saito:example.com", "first", ""); err != nil {
		t.Fatalf("first runTurn returned unexpected error: %v", err)
	}
	if _, ok := prov.waitForCall(3 * time.Second); !ok {
		t.Fatal("expected first runTurn to call provider")
	}

	if _, _, err := a.runTurn(context.Background(), "!chat-room:example.com", "@saito:example.com", "second", ""); err == nil {
		t.Fatal("second runTurn should fail when hard LLM call limit is exceeded")
	}
	if !terminated {
		t.Fatal("expected hard limit to trigger terminateProcess")
	}
	if exitCode != 1 {
		t.Fatalf("terminateProcess exit code = %d, want 1", exitCode)
	}
	if _, ok := prov.waitForCall(300 * time.Millisecond); ok {
		t.Fatal("provider should not be called after hard limit is exceeded")
	}
}

func TestBuildLLMProvider_OpenAIWithoutAPIKey_ReturnsNil(t *testing.T) {
	prov := buildLLMProvider(LLMConfig{
		Provider: "openai",
		APIKey:   "   ",
		Model:    "gpt-4o",
	})
	if prov != nil {
		t.Fatal("expected nil provider when OpenAI API key is missing")
	}
}

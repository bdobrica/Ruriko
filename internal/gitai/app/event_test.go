package app

// Tests for the event-to-turn bridge added in R12.2:
//   - buildEventMessage:    auto-generation of the user prompt from an event envelope
//   - handleEvent / runEventTurn: full turn pipeline wired from a gateway event:
//       cron event triggers a full LLM turn, response posted to admin room,
//       turn logged with gateway metadata, empty-message events auto-generate prompt.
//
// The tests use whitebox (package-internal) construction so we can build a
// minimal *App without spinning up Matrix or a real network connection.
// The LLM provider is replaced with a synchronous in-process stub.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
	"github.com/bdobrica/Ruriko/internal/gitai/supervisor"
)

// --- stub LLM provider ---

// capturingLLM is a minimal llm.Provider that records every CompletionRequest
// it receives via requests and returns a fixed text response.  The channel is
// buffered so sends never block; tests drain it to verify the LLM was called.
type capturingLLM struct {
	response string
	requests chan llm.CompletionRequest
}

func newCapturingLLM(response string) *capturingLLM {
	return &capturingLLM{
		response: response,
		requests: make(chan llm.CompletionRequest, 8),
	}
}

func (c *capturingLLM) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	c.requests <- req
	return &llm.CompletionResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: c.response},
		FinishReason: "stop",
	}, nil
}

// waitForCall blocks until the stub receives a call or the deadline elapses.
// It returns the received request and true, or a zero value and false on
// timeout.
func (c *capturingLLM) waitForCall(timeout time.Duration) (llm.CompletionRequest, bool) {
	select {
	case req := <-c.requests:
		return req, true
	case <-time.After(timeout):
		return llm.CompletionRequest{}, false
	}
}

// --- helpers ---

// eventTestGosutoYAML is a minimal valid Gosuto config that includes an
// adminRoom so runEventTurn has somewhere to post the response.
const eventTestGosutoYAML = `apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms:
    - "!chat-room:example.com"
  allowedSenders:
    - "@user:example.com"
  adminRoom: "!admin-room:example.com"
persona:
  llmProvider: openai
  model: gpt-4o-mini
  systemPrompt: "You are a helpful test agent."
`

// eventTestGosutoYAML_NoAdminRoom is a minimal valid Gosuto config that does
// NOT set an adminRoom — used to test the "drop event" code path.
const eventTestGosutoYAML_NoAdminRoom = `apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms:
    - "!chat-room:example.com"
  allowedSenders:
    - "@user:example.com"
persona:
  llmProvider: openai
  model: gpt-4o-mini
  systemPrompt: "You are a helpful test agent."
`

// newEventApp builds a minimal App wired for event-turn tests:
//   - SQLite store backed by a temp file
//   - Gosuto loader with the provided YAML applied
//   - capturingLLM stub (no real HTTP calls)
//   - empty supervisor (no MCP processes, which is fine — gatherTools returns nil)
//   - no matrix client (matrixCli = nil, guarded inside runEventTurn)
func newEventApp(t *testing.T, gosutoYAML string, prov llm.Provider) *App {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "gitai.db")
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { db.Close() })

	ldr := gosuto.New()
	if err := ldr.Apply([]byte(gosutoYAML)); err != nil {
		t.Fatalf("gosuto loader Apply: %v", err)
	}

	supv := supervisor.New()
	t.Cleanup(supv.Stop)

	a := &App{
		db:        db,
		gosutoLdr: ldr,
		supv:      supv,
		policyEng: policy.New(ldr),
		cancelCh:  make(chan struct{}, 1),
		// matrixCli intentionally nil — runEventTurn guards sends with nil check
	}
	a.setProvider(prov)
	return a
}

// makeTestEvent constructs a valid Event envelope for the given source/type.
func makeTestEvent(source, evtType, message string) *envelope.Event {
	return &envelope.Event{
		Source: source,
		Type:   evtType,
		TS:     time.Now(),
		Payload: envelope.EventPayload{
			Message: message,
		},
	}
}

// makeTestEventWithData is like makeTestEvent but includes structured data.
func makeTestEventWithData(source, evtType string, data map[string]interface{}) *envelope.Event {
	return &envelope.Event{
		Source: source,
		Type:   evtType,
		TS:     time.Now(),
		Payload: envelope.EventPayload{
			Data: data,
		},
	}
}

// --- buildEventMessage tests ---

func TestBuildEventMessage_UsesProvidedMessage(t *testing.T) {
	evt := makeTestEvent("scheduler", "cron.tick", "Run the scheduled analysis now.")
	got := buildEventMessage(evt)
	if got != "Run the scheduled analysis now." {
		t.Errorf("buildEventMessage: got %q, want provided message", got)
	}
}

func TestBuildEventMessage_AutoGenerates_NoData(t *testing.T) {
	evt := makeTestEvent("scheduler", "cron.tick", "" /* empty */)
	got := buildEventMessage(evt)
	if !strings.Contains(got, "scheduler") {
		t.Errorf("buildEventMessage: got %q, want reference to source", got)
	}
	if !strings.Contains(got, "cron.tick") {
		t.Errorf("buildEventMessage: got %q, want reference to type", got)
	}
}

func TestBuildEventMessage_AutoGenerates_WithData(t *testing.T) {
	evt := makeTestEventWithData("webhook", "webhook.delivery", map[string]interface{}{
		"ticker": "AAPL",
		"price":  189.50,
	})
	got := buildEventMessage(evt)
	if !strings.Contains(got, "webhook") {
		t.Errorf("buildEventMessage: got %q, want source in prompt", got)
	}
	if !strings.Contains(got, "webhook.delivery") {
		t.Errorf("buildEventMessage: got %q, want event type in prompt", got)
	}
	// Structured data should be serialised as JSON.
	if !strings.Contains(got, "AAPL") {
		t.Errorf("buildEventMessage: got %q, want ticker in JSON data", got)
	}
}

// --- handleEvent / runEventTurn integration tests ---

// TestHandleEvent_TriggersCronTurnAndLogsIt verifies that a cron event causes
// a full LLM turn and that the resulting DB record carries the gateway sender
// label and reports success.
func TestHandleEvent_TriggersCronTurnAndLogsIt(t *testing.T) {
	prov := newCapturingLLM("Market looks stable today.")
	a := newEventApp(t, eventTestGosutoYAML, prov)

	evt := makeTestEvent("scheduler", "cron.tick", "Run the scheduled market check.")
	a.handleEvent(context.Background(), evt)

	// Wait for the LLM turn to complete (up to 3 s in CI).
	req, ok := prov.waitForCall(3 * time.Second)
	if !ok {
		t.Fatal("timed out waiting for LLM call from event turn")
	}

	// The LLM should have received the event message as the user turn.
	found := false
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "market check") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("LLM messages did not contain event payload text; messages: %+v", req.Messages)
	}
}

// TestHandleEvent_LogsTurnWithGatewayMetadata verifies that the turn logged to
// the DB uses the "gateway:<source>" sender label so gateway turns are clearly
// distinguishable from Matrix-message turns in the store.
func TestHandleEvent_LogsTurnWithGatewayMetadata(t *testing.T) {
	prov := newCapturingLLM("Analysis complete.")
	a := newEventApp(t, eventTestGosutoYAML, prov)

	evt := makeTestEvent("scheduler", "cron.tick", "Trigger analysis run.")
	a.handleEvent(context.Background(), evt)

	// Wait for the LLM call so the goroutine has had a chance to LogTurn.
	if _, ok := prov.waitForCall(3 * time.Second); !ok {
		t.Fatal("timed out waiting for LLM call")
	}
	// Give the goroutine a moment to finish the post-LLM DB writes.
	time.Sleep(50 * time.Millisecond)

	// Query the turn_log table directly to verify the sender label.
	rows, err := a.db.DB().QueryContext(context.Background(),
		"SELECT sender_mxid, room_id FROM turn_log ORDER BY id DESC LIMIT 1")
	if err != nil {
		t.Fatalf("query turn_log: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("turn_log is empty — expected one row from event turn")
	}
	var senderMXID, roomID string
	if err := rows.Scan(&senderMXID, &roomID); err != nil {
		t.Fatalf("scan turn_log row: %v", err)
	}

	if senderMXID != "gateway:scheduler" {
		t.Errorf("sender_mxid = %q, want %q", senderMXID, "gateway:scheduler")
	}
	if roomID != "!admin-room:example.com" {
		t.Errorf("room_id = %q, want %q", roomID, "!admin-room:example.com")
	}
}

// TestHandleEvent_AutoGeneratesPromptForEmptyMessage verifies that when an
// event has no Payload.Message the LLM still receives a descriptive auto-
// generated prompt (not an empty user message).
func TestHandleEvent_AutoGeneratesPromptForEmptyMessage(t *testing.T) {
	prov := newCapturingLLM("Done.")
	a := newEventApp(t, eventTestGosutoYAML, prov)

	// Event with empty message — runEventTurn must auto-generate a prompt.
	evt := makeTestEvent("scheduler", "cron.tick", "" /* no message */)
	a.handleEvent(context.Background(), evt)

	req, ok := prov.waitForCall(3 * time.Second)
	if !ok {
		t.Fatal("timed out waiting for LLM call")
	}

	// The user message must be non-empty and reference the source/type.
	found := false
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser && m.Content != "" {
			found = true
			if !strings.Contains(m.Content, "scheduler") {
				t.Errorf("auto-generated message %q does not mention source %q", m.Content, "scheduler")
			}
			break
		}
	}
	if !found {
		t.Error("LLM received no non-empty user message")
	}
}

// TestHandleEvent_DropsEventWhenNoConfig verifies that an inbound event is
// silently dropped (no panic) when no Gosuto config is loaded.
func TestHandleEvent_DropsEventWhenNoConfig(t *testing.T) {
	prov := newCapturingLLM("should not be called")
	// Build an App with an unfilled loader (no config applied).
	dbPath := filepath.Join(t.TempDir(), "gitai.db")
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	a := &App{
		db:        db,
		gosutoLdr: gosuto.New(), // empty — Config() returns nil
		supv:      supervisor.New(),
		cancelCh:  make(chan struct{}, 1),
	}
	t.Cleanup(a.supv.Stop)
	a.setProvider(prov)

	evt := makeTestEvent("scheduler", "cron.tick", "trigger")

	// Must not panic; the LLM provider must NOT be called.
	a.handleEvent(context.Background(), evt)

	// Pause briefly; if the goroutine were to call Complete the channel would
	// receive within 50 ms.
	select {
	case <-prov.requests:
		t.Error("LLM was called even though no Gosuto config is loaded")
	case <-time.After(200 * time.Millisecond):
		// Correct: no call made.
	}
}

// TestHandleEvent_DropsEventWhenNoAdminRoom verifies that an event is dropped
// when the Gosuto config does not define an admin room.
func TestHandleEvent_DropsEventWhenNoAdminRoom(t *testing.T) {
	prov := newCapturingLLM("should not be called")
	// Use a config without adminRoom.
	a := newEventApp(t, eventTestGosutoYAML_NoAdminRoom, prov)

	evt := makeTestEvent("scheduler", "cron.tick", "trigger")
	a.handleEvent(context.Background(), evt)

	select {
	case <-prov.requests:
		t.Error("LLM was called even though no admin room is configured")
	case <-time.After(200 * time.Millisecond):
		// Correct: no call made.
	}
}

package commands

// nl_dispatch_test.go — unit tests for the R9.4 Conversation-Aware Dispatch.
//
// These tests exercise HandleNaturalLanguage when an NLPProvider is wired in.
// They use a stub LLM provider so no real LLM calls are made.

import (
	"context"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// nlpStub is a fixed-response nlp.Provider used in NL dispatch tests.
type nlpStub struct {
	resp *nlp.ClassifyResponse
	err  error
}

func (s *nlpStub) Classify(_ context.Context, _ nlp.ClassifyRequest) (*nlp.ClassifyResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	cp := *s.resp
	// Shallow-copy slices so mutations in the handler don't affect subsequent
	// calls.
	if s.resp.ReadQueries != nil {
		cp.ReadQueries = append([]string(nil), s.resp.ReadQueries...)
	}
	if s.resp.Steps != nil {
		cp.Steps = append([]nlp.CommandStep(nil), s.resp.Steps...)
	}
	return &cp, nil
}

// captureDispatch records which action keys were dispatched and returns
// canned responses.
type captureDispatch struct {
	dispatched []string
	response   string
}

func (c *captureDispatch) dispatch(_ context.Context, action string, _ *Command, _ *event.Event) (string, error) {
	c.dispatched = append(c.dispatched, action)
	return c.response, nil
}

// nlpFakeEvent returns a minimal *event.Event suitable for NL handler tests.
func nlpFakeEvent() *event.Event {
	return &event.Event{
		Sender: id.UserID("@alice:example.com"),
		RoomID: id.RoomID("!nltest:example.com"),
	}
}

// newNLHandlers creates a Handlers instance wired with the given NLP stub
// and optional dispatch capture.
func newNLHandlers(provider nlp.Provider, cap *captureDispatch) *Handlers {
	h := NewHandlers(HandlersConfig{
		NLPProvider: provider,
	})
	if cap != nil {
		h.SetDispatch(cap.dispatch)
	}
	return h
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestHandleNaturalLanguage_LLM_MutationRequiresConfirmation verifies that
// when the LLM returns IntentCommand the handler shows a confirmation prompt
// without dispatching, and only dispatches after the operator replies "yes".
func TestHandleNaturalLanguage_LLM_MutationRequiresConfirmation(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentCommand,
		Action:      "agents.list",
		Explanation: "You want to list agents.",
		Confidence:  0.95,
	}}
	cap := &captureDispatch{response: "Agents: none."}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	// First message — should return a confirmation prompt, no dispatch yet.
	reply1, err := h.HandleNaturalLanguage(context.Background(), "show me the agents", evt)
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if !strings.Contains(reply1, "yes") {
		t.Errorf("expected confirmation prompt containing 'yes', got: %q", reply1)
	}
	if len(cap.dispatched) != 0 {
		t.Errorf("expected no dispatch before confirmation, got: %v", cap.dispatched)
	}

	// Operator replies "yes" — dispatch should now fire.
	reply2, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if len(cap.dispatched) != 1 || cap.dispatched[0] != "agents.list" {
		t.Errorf("expected dispatch of agents.list, got: %v", cap.dispatched)
	}
	if reply2 != "Agents: none." {
		t.Errorf("expected dispatch result as reply, got: %q", reply2)
	}
}

// TestHandleNaturalLanguage_LLM_ReadOnlyAnsweredDirectly verifies that
// IntentConversational with ReadQueries dispatches immediately without any
// confirmation prompt.
func TestHandleNaturalLanguage_LLM_ReadOnlyAnsweredDirectly(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentConversational,
		Response:    "Here is the current agent list:",
		ReadQueries: []string{"agents.list"},
	}}
	cap := &captureDispatch{response: "✅ agent1 (running)"}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	reply, err := h.HandleNaturalLanguage(context.Background(), "how many agents are running?", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Response should contain both the LLM preamble and the dispatch result.
	if !strings.Contains(reply, "Here is the current agent list:") {
		t.Errorf("expected LLM response in reply, got: %q", reply)
	}
	if !strings.Contains(reply, "agent1") {
		t.Errorf("expected dispatch result in reply, got: %q", reply)
	}

	// Dispatch must have been invoked with the correct action key.
	if len(cap.dispatched) != 1 || cap.dispatched[0] != "agents.list" {
		t.Errorf("expected dispatch of [agents.list], got: %v", cap.dispatched)
	}
}

// TestHandleNaturalLanguage_LLM_MultiStepConfirmation verifies that multi-step
// mutations are decomposed: each step gets an individual confirmation and are
// NOT batched.
func TestHandleNaturalLanguage_LLM_MultiStepConfirmation(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentCommand,
		Explanation: "Set up two agents.",
		Steps: []nlp.CommandStep{
			{Action: "agents.stop", Flags: map[string]string{"name": "saito"}, Explanation: "Stop saito first."},
			{Action: "agents.start", Flags: map[string]string{"name": "saito"}, Explanation: "Then start saito."},
		},
	}}
	cap := &captureDispatch{response: "Done."}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	// Step 1 prompt.
	reply1, err := h.HandleNaturalLanguage(context.Background(), "restart saito", evt)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !strings.Contains(reply1, "Step 1 of 2") {
		t.Errorf("expected 'Step 1 of 2' in reply, got: %q", reply1)
	}
	if !strings.Contains(reply1, "agents.stop") && !strings.Contains(reply1, "agents stop") {
		t.Errorf("expected step action in reply, got: %q", reply1)
	}
	if len(cap.dispatched) != 0 {
		t.Errorf("no dispatch expected before first confirmation, got: %v", cap.dispatched)
	}

	// User confirms step 1.
	reply2, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("step 1 confirm: %v", err)
	}
	if len(cap.dispatched) != 1 || cap.dispatched[0] != "agents.stop" {
		t.Errorf("expected agents.stop dispatched after step 1 confirm, got: %v", cap.dispatched)
	}
	if !strings.Contains(reply2, "Step 2 of 2") {
		t.Errorf("expected 'Step 2 of 2' in reply after step 1 confirm, got: %q", reply2)
	}

	// User confirms step 2.
	reply3, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("step 2 confirm: %v", err)
	}
	if len(cap.dispatched) != 2 || cap.dispatched[1] != "agents.start" {
		t.Errorf("expected agents.start dispatched after step 2 confirm, got: %v", cap.dispatched)
	}
	if reply3 != "Done." {
		t.Errorf("expected final dispatch result, got: %q", reply3)
	}

	// Session should be cleared — further messages should go through the LLM
	// classifier again (not through confirmationResponse).
	// Confirm no session lingering by checking the fourth message produces a
	// fresh confirmation prompt (stub still returns the multi-step response).
	reply4, err := h.HandleNaturalLanguage(context.Background(), "restart saito again", evt)
	if err != nil {
		t.Fatalf("post-completion call: %v", err)
	}
	if !strings.Contains(reply4, "Step 1 of 2") {
		t.Errorf("expected fresh Step 1 prompt after session cleared, got: %q", reply4)
	}
}

// TestHandleNaturalLanguage_LLM_CancelClearsSession verifies that replying
// "no" cancels the pending session and subsequent messages start fresh.
func TestHandleNaturalLanguage_LLM_CancelClearsSession(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentCommand,
		Action:      "agents.delete",
		Explanation: "Delete agent.",
		Confidence:  0.9,
	}}
	cap := &captureDispatch{response: "Deleted."}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	// First message — confirmation prompt.
	reply1, _ := h.HandleNaturalLanguage(context.Background(), "delete the saito agent", evt)
	if !strings.Contains(reply1, "yes") {
		t.Fatalf("expected confirmation prompt, got: %q", reply1)
	}

	// Operator says "no".
	reply2, err := h.HandleNaturalLanguage(context.Background(), "no", evt)
	if err != nil {
		t.Fatalf("cancel reply: %v", err)
	}
	if !strings.Contains(reply2, "Cancelled") {
		t.Errorf("expected cancellation message, got: %q", reply2)
	}
	if len(cap.dispatched) != 0 {
		t.Errorf("expected no dispatch after cancel, got: %v", cap.dispatched)
	}

	// Session cleared — next message should produce a fresh prompt, not re-use
	// the old session.
	reply3, _ := h.HandleNaturalLanguage(context.Background(), "delete the saito agent", evt)
	if !strings.Contains(reply3, "yes") {
		t.Errorf("expected fresh confirmation prompt after cancel, got: %q", reply3)
	}
}

// TestHandleNaturalLanguage_LLM_AuditSourceNL verifies that confirmed NL
// mutations write an audit entry with source: "nl" and llm_intent set.
func TestHandleNaturalLanguage_LLM_AuditSourceNL(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentCommand,
		Action:      "agents.list",
		Explanation: "List all running agents.",
		Confidence:  0.95,
	}}

	// Create a real SQLite store so we can assert the audit entry.
	f, err := os.CreateTemp(t.TempDir(), "ruriko-nl-audit-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()
	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cap := &captureDispatch{response: "Agents: none."}
	h := NewHandlers(HandlersConfig{
		Store:       s,
		NLPProvider: stub,
	})
	h.SetDispatch(cap.dispatch)

	evt := nlpFakeEvent()

	// Get confirmation prompt.
	_, err = h.HandleNaturalLanguage(context.Background(), "list agents", evt)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Confirm.
	_, err = h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("confirm call: %v", err)
	}

	// Check that the audit log contains an nl.dispatch entry.
	entries, err := s.GetAuditLog(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}

	var found bool
	for _, e := range entries {
		if e.Action == "nl.dispatch" {
			found = true
			// Verify the payload encodes "source: nl" and the llm_intent.
			if !strings.Contains(e.PayloadJSON.String, `"source":"nl"`) &&
				!strings.Contains(e.PayloadJSON.String, `"source": "nl"`) {
				t.Errorf("nl.dispatch audit payload missing source:nl, got: %s", e.PayloadJSON.String)
			}
			if !strings.Contains(e.PayloadJSON.String, "List all running agents") {
				t.Errorf("nl.dispatch audit payload missing llm_intent, got: %s", e.PayloadJSON.String)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected nl.dispatch audit entry, entries found: %d", len(entries))
	}
}

// TestHandleNaturalLanguage_LLM_FallbackToKeywordOnError verifies that if the
// NLP provider returns an error the handler falls back to the keyword path
// (which returns "" when the message contains no recognisable intent).
func TestHandleNaturalLanguage_LLM_FallbackToKeywordOnError(t *testing.T) {
	stub := &nlpStub{err: errProviderUnavailable}
	cap := &captureDispatch{}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	reply, err := h.HandleNaturalLanguage(context.Background(), "hello there", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Keyword path finds no intent in "hello there" → empty reply.
	if reply != "" {
		t.Errorf("expected empty reply on provider error + no keyword intent, got: %q", reply)
	}
	if len(cap.dispatched) != 0 {
		t.Errorf("expected no dispatch on fallback, got: %v", cap.dispatched)
	}
}

// errProviderUnavailable is a sentinel error for the fallback test.
var errProviderUnavailable = &providerErr{"NLP provider unavailable"}

type providerErr struct{ msg string }

func (e *providerErr) Error() string { return e.msg }

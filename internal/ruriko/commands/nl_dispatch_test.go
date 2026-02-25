package commands

// nl_dispatch_test.go — unit tests for the R9.4 Conversation-Aware Dispatch.
//
// These tests exercise HandleNaturalLanguage when an NLPProvider is wired in.
// They use a stub LLM provider so no real LLM calls are made.

import (
	"context"
	"fmt"
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

// ---------------------------------------------------------------------------
// R16.2 — IntentPlan multi-agent workflow decomposition tests
// ---------------------------------------------------------------------------

// TestHandleNaturalLanguage_PlanIntent_ShowsOverviewAndDecomposesSteps
// verifies that an IntentPlan response:
//  1. Shows the plan overview (explanation) before the first step prompt.
//  2. Shows "Step 1 of N" header for each step.
//  3. Does NOT dispatch until the operator confirms each step individually.
//  4. Dispatches all steps in order after consecutive confirmations.
func TestHandleNaturalLanguage_PlanIntent_ShowsOverviewAndDecomposesSteps(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentPlan,
		Explanation: "I'll create Saito (cron agent) and Kumo (news agent).",
		Confidence:  0.92,
		Steps: []nlp.CommandStep{
			{
				Action:      "agents.create",
				Flags:       map[string]string{"name": "saito", "template": "saito-agent"},
				Explanation: "Create Saito cron/trigger agent.",
			},
			{
				Action:      "agents.create",
				Flags:       map[string]string{"name": "kumo", "template": "kumo-agent"},
				Explanation: "Create Kumo news search agent.",
			},
		},
	}}
	cap := &captureDispatch{response: "Done."}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	// --- Initial message: should show plan overview + first step prompt -------
	reply1, err := h.HandleNaturalLanguage(context.Background(), "set up Saito and Kumo", evt)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Plan header must be present.
	if !strings.Contains(reply1, "Plan") {
		t.Errorf("expected plan header in first reply, got: %q", reply1)
	}
	// Overall plan explanation must appear.
	if !strings.Contains(reply1, "Saito") {
		t.Errorf("expected plan explanation mentioning 'Saito' in first reply, got: %q", reply1)
	}
	// Step progress indicator.
	if !strings.Contains(reply1, "Step 1 of 2") {
		t.Errorf("expected 'Step 1 of 2' in first reply, got: %q", reply1)
	}
	// Confirmation words.
	if !strings.Contains(reply1, "yes") {
		t.Errorf("expected 'yes' option in first reply, got: %q", reply1)
	}

	// No dispatch yet.
	if len(cap.dispatched) != 0 {
		t.Errorf("expected no dispatch before step 1 confirmation, got: %v", cap.dispatched)
	}

	// --- Confirm step 1 -------------------------------------------------------
	reply2, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("step 1 confirm: %v", err)
	}
	if len(cap.dispatched) != 1 || cap.dispatched[0] != "agents.create" {
		t.Errorf("expected agents.create dispatched after step 1 confirm, got: %v", cap.dispatched)
	}
	if !strings.Contains(reply2, "Step 2 of 2") {
		t.Errorf("expected 'Step 2 of 2' after step 1 confirm, got: %q", reply2)
	}

	// --- Confirm step 2 -------------------------------------------------------
	reply3, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("step 2 confirm: %v", err)
	}
	if len(cap.dispatched) != 2 || cap.dispatched[1] != "agents.create" {
		t.Errorf("expected second agents.create dispatched after step 2 confirm, got: %v", cap.dispatched)
	}
	if reply3 != "Done." {
		t.Errorf("expected final dispatch result after last step, got: %q", reply3)
	}
}

// TestHandleNaturalLanguage_PlanIntent_CancelAtFirstStep verifies that
// replying "no" to the first step of a plan cancels the entire plan and
// clears the session.
func TestHandleNaturalLanguage_PlanIntent_CancelAtFirstStep(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentPlan,
		Explanation: "Create Saito and Kumo.",
		Confidence:  0.9,
		Steps: []nlp.CommandStep{
			{Action: "agents.create", Flags: map[string]string{"name": "saito", "template": "saito-agent"}},
			{Action: "agents.create", Flags: map[string]string{"name": "kumo", "template": "kumo-agent"}},
		},
	}}
	cap := &captureDispatch{response: "Done."}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	// Trigger the plan prompt.
	_, err := h.HandleNaturalLanguage(context.Background(), "set up Saito and Kumo", evt)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Operator cancels at step 1.
	reply, err := h.HandleNaturalLanguage(context.Background(), "no", evt)
	if err != nil {
		t.Fatalf("cancel reply: %v", err)
	}
	if !strings.Contains(reply, "Cancelled") {
		t.Errorf("expected cancellation message, got: %q", reply)
	}
	if len(cap.dispatched) != 0 {
		t.Errorf("expected no dispatch after cancel, got: %v", cap.dispatched)
	}

	// Session must be cleared — the next message goes through the LLM again
	// (stub still returns plan), producing a fresh plan prompt.
	reply2, err := h.HandleNaturalLanguage(context.Background(), "set up Saito and Kumo again", evt)
	if err != nil {
		t.Fatalf("post-cancel call: %v", err)
	}
	if !strings.Contains(reply2, "Step 1 of 2") {
		t.Errorf("expected fresh 'Step 1 of 2' after cancel+restart, got: %q", reply2)
	}
}

// TestHandleNaturalLanguage_PlanIntent_CancelMidPlan verifies that replying
// "no" mid-plan (after the first step dispatches) halts remaining steps and
// clears the session.
func TestHandleNaturalLanguage_PlanIntent_CancelMidPlan(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentPlan,
		Explanation: "Create Saito, Kumo, and Kairo.",
		Confidence:  0.91,
		Steps: []nlp.CommandStep{
			{Action: "agents.create", Flags: map[string]string{"name": "saito", "template": "saito-agent"}},
			{Action: "agents.create", Flags: map[string]string{"name": "kumo", "template": "kumo-agent"}},
			{Action: "agents.create", Flags: map[string]string{"name": "kairo", "template": "kairo-agent"}},
		},
	}}
	cap := &captureDispatch{response: "Created."}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	// Initial plan prompt.
	_, _ = h.HandleNaturalLanguage(context.Background(), "set up three agents", evt)

	// Confirm step 1.
	_, _ = h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if len(cap.dispatched) != 1 {
		t.Fatalf("expected 1 dispatch after step 1, got %d", len(cap.dispatched))
	}

	// Cancel at step 2.
	cancelReply, err := h.HandleNaturalLanguage(context.Background(), "no", evt)
	if err != nil {
		t.Fatalf("cancel at step 2: %v", err)
	}
	if !strings.Contains(cancelReply, "Cancelled") {
		t.Errorf("expected cancellation message after mid-plan cancel, got: %q", cancelReply)
	}
	// Step 3 must NOT have dispatched.
	if len(cap.dispatched) != 1 {
		t.Errorf("expected exactly 1 dispatch (only step 1), got %d: %v", len(cap.dispatched), cap.dispatched)
	}
}

// ---------------------------------------------------------------------------
// R9.5 Graceful Degradation and Fallbacks
// ---------------------------------------------------------------------------

// TestHandleNaturalLanguage_LLM_APIRateLimitReturnsMessage verifies that when
// the NLP provider returns ErrRateLimit the handler surfaces a user-visible
// rate-limit message rather than silently falling back to keyword matching.
func TestHandleNaturalLanguage_LLM_APIRateLimitReturnsMessage(t *testing.T) {
	stub := &nlpStub{err: fmt.Errorf("api 429: %w", nlp.ErrRateLimit)}
	cap := &captureDispatch{}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	reply, err := h.HandleNaturalLanguage(context.Background(), "list all agents", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != nlp.APIRateLimitMessage {
		t.Errorf("expected APIRateLimitMessage, got: %q", reply)
	}
	if len(cap.dispatched) != 0 {
		t.Errorf("expected no dispatch on rate limit, got: %v", cap.dispatched)
	}
}

// TestHandleNaturalLanguage_LLM_MalformedOutputReturnsMessage verifies that
// when the LLM returns content that cannot be parsed the handler replies with
// a clarification prompt instead of silently falling back.
func TestHandleNaturalLanguage_LLM_MalformedOutputReturnsMessage(t *testing.T) {
	stub := &nlpStub{err: fmt.Errorf("bad json: %w", nlp.ErrMalformedOutput)}
	cap := &captureDispatch{}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	reply, err := h.HandleNaturalLanguage(context.Background(), "do the thing", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != nlp.MalformedOutputMessage {
		t.Errorf("expected MalformedOutputMessage, got: %q", reply)
	}
	if len(cap.dispatched) != 0 {
		t.Errorf("expected no dispatch on malformed output, got: %v", cap.dispatched)
	}
}

// TestHandleNaturalLanguage_RawCommandsBypassNL verifies that messages
// starting with "/ruriko" are immediately rejected by HandleNaturalLanguage so
// that the NL layer never intercepts /ruriko-prefixed messages (which are the
// command router's responsibility).
func TestHandleNaturalLanguage_RawCommandsBypassNL(t *testing.T) {
	// Even with a valid NLP provider wired in, a /ruriko-prefixed message
	// must return ("", nil) without ever calling the provider.
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:      nlp.IntentCommand,
		Action:      "agents.list",
		Explanation: "List agents.",
		Confidence:  0.99,
	}}
	cap := &captureDispatch{response: "agents: none"}
	h := newNLHandlers(stub, cap)
	evt := nlpFakeEvent()

	cases := []string{
		"/ruriko agents list",
		"/ruriko help",
		"  /ruriko ping", // leading space
		"/ruriko secrets set foo bar",
	}

	for _, msg := range cases {
		reply, err := h.HandleNaturalLanguage(context.Background(), msg, evt)
		if err != nil {
			t.Errorf("msg %q: unexpected error: %v", msg, err)
		}
		if reply != "" {
			t.Errorf("msg %q: expected empty reply (bypass), got: %q", msg, reply)
		}
		if len(cap.dispatched) != 0 {
			t.Errorf("msg %q: expected no dispatch, got: %v", msg, cap.dispatched)
		}
	}
}

// TestHandleNaturalLanguage_NLPHealthState verifies that NLPProviderStatus
// transitions correctly in response to different error types.
func TestHandleNaturalLanguage_NLPHealthState(t *testing.T) {
	t.Run("ok on successful classify", func(t *testing.T) {
		stub := &nlpStub{resp: &nlp.ClassifyResponse{
			Intent:     nlp.IntentConversational,
			Response:   "Here you go.",
			Confidence: 0.95,
		}}
		h := newNLHandlers(stub, nil)
		evt := nlpFakeEvent()

		_, _ = h.HandleNaturalLanguage(context.Background(), "how are you?", evt)
		if got := h.NLPProviderStatus(); got != "ok" {
			t.Errorf("expected status 'ok', got %q", got)
		}
	})

	t.Run("degraded on api rate limit", func(t *testing.T) {
		stub := &nlpStub{err: fmt.Errorf("429: %w", nlp.ErrRateLimit)}
		h := newNLHandlers(stub, nil)
		evt := nlpFakeEvent()

		_, _ = h.HandleNaturalLanguage(context.Background(), "list agents", evt)
		if got := h.NLPProviderStatus(); got != "degraded" {
			t.Errorf("expected status 'degraded', got %q", got)
		}
	})

	t.Run("degraded on malformed output", func(t *testing.T) {
		stub := &nlpStub{err: fmt.Errorf("parse: %w", nlp.ErrMalformedOutput)}
		h := newNLHandlers(stub, nil)
		evt := nlpFakeEvent()

		_, _ = h.HandleNaturalLanguage(context.Background(), "do stuff", evt)
		if got := h.NLPProviderStatus(); got != "degraded" {
			t.Errorf("expected status 'degraded', got %q", got)
		}
	})

	t.Run("unavailable on generic error", func(t *testing.T) {
		stub := &nlpStub{err: errProviderUnavailable}
		h := newNLHandlers(stub, nil)
		evt := nlpFakeEvent()

		_, _ = h.HandleNaturalLanguage(context.Background(), "help me", evt)
		if got := h.NLPProviderStatus(); got != "unavailable" {
			t.Errorf("expected status 'unavailable', got %q", got)
		}
	})

	t.Run("recovers to ok after error", func(t *testing.T) {
		var callCount int
		// First call returns a rate limit error; second call succeeds.
		stub := &nlpStub{}
		h := NewHandlers(HandlersConfig{NLPProvider: stub})
		evt := nlpFakeEvent()

		stub.err = fmt.Errorf("429: %w", nlp.ErrRateLimit)
		_, _ = h.HandleNaturalLanguage(context.Background(), "list agents", evt)
		if got := h.NLPProviderStatus(); got != "degraded" {
			t.Errorf("after error: expected 'degraded', got %q", got)
		}
		callCount++

		stub.err = nil
		stub.resp = &nlp.ClassifyResponse{Intent: nlp.IntentConversational, Response: "hi", Confidence: 0.9}
		_, _ = h.HandleNaturalLanguage(context.Background(), "hi", evt)
		if got := h.NLPProviderStatus(); got != "ok" {
			t.Errorf("after recovery (call %d): expected 'ok', got %q", callCount, got)
		}
	})

	t.Run("unavailable when no provider", func(t *testing.T) {
		h := NewHandlers(HandlersConfig{})
		if got := h.NLPProviderStatus(); got != "unavailable" {
			t.Errorf("expected 'unavailable' without provider, got %q", got)
		}
	})
}

// TestHandleNaturalLanguage_TokenBudgetEnforced verifies that when a sender
// has exhausted their daily token budget the handler returns the
// TokenBudgetExceededMessage without calling the LLM provider.
func TestHandleNaturalLanguage_TokenBudgetEnforced(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:     nlp.IntentConversational,
		Response:   "Hello from the LLM.",
		Confidence: 0.9,
	}}
	budget := nlp.NewTokenBudget(100)
	// Exhaust the budget before the handler is called.
	budget.RecordUsage("@alice:example.com", 100)

	h := NewHandlers(HandlersConfig{
		NLPProvider:    stub,
		NLPTokenBudget: budget,
	})
	evt := nlpFakeEvent()

	reply, err := h.HandleNaturalLanguage(context.Background(), "hello", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != nlp.TokenBudgetExceededMessage {
		t.Errorf("expected budget-exceeded message, got %q", reply)
	}
}

// TestHandleNaturalLanguage_TokenUsageRecorded verifies that after a
// successful classify call the consumed tokens are recorded in the budget
// tracker so subsequent calls can enforce the limit correctly.
func TestHandleNaturalLanguage_TokenUsageRecorded(t *testing.T) {
	const tokensUsed = 42
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:     nlp.IntentConversational,
		Response:   "Hello.",
		Confidence: 0.9,
		Usage:      &nlp.TokenUsage{TotalTokens: tokensUsed, PromptTokens: 30, CompletionTokens: 12},
	}}
	budget := nlp.NewTokenBudget(1000)

	h := NewHandlers(HandlersConfig{
		NLPProvider:    stub,
		NLPTokenBudget: budget,
	})
	evt := nlpFakeEvent()

	_, err := h.HandleNaturalLanguage(context.Background(), "hello", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := budget.Used("@alice:example.com"); got != tokensUsed {
		t.Errorf("budget.Used after classify: got %d, want %d", got, tokensUsed)
	}
}

// TestHandleNaturalLanguage_NilUsageNotPanics verifies that when the provider
// returns a ClassifyResponse without Usage set (e.g. a stub or keyword path)
// the handler does not panic and still records nothing in the budget.
func TestHandleNaturalLanguage_NilUsageNotPanics(t *testing.T) {
	stub := &nlpStub{resp: &nlp.ClassifyResponse{
		Intent:     nlp.IntentConversational,
		Response:   "Fine.",
		Confidence: 0.9,
		// Usage is intentionally nil.
	}}
	budget := nlp.NewTokenBudget(1000)

	h := NewHandlers(HandlersConfig{
		NLPProvider:    stub,
		NLPTokenBudget: budget,
	})
	evt := nlpFakeEvent()

	_, err := h.HandleNaturalLanguage(context.Background(), "hello", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := budget.Used("@alice:example.com"); got != 0 {
		t.Errorf("expected 0 tokens recorded when Usage is nil, got %d", got)
	}
}

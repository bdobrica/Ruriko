package nlp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stubProvider returns a fixed ClassifyResponse (or error) on every Classify
// call, regardless of the request.  It also records the last request for
// inspection.
type stubProvider struct {
	resp     *nlp.ClassifyResponse
	err      error
	captured nlp.ClassifyRequest
}

func (s *stubProvider) Classify(_ context.Context, req nlp.ClassifyRequest) (*nlp.ClassifyResponse, error) {
	s.captured = req
	if s.err != nil {
		return nil, s.err
	}
	// Return a shallow copy so tests can mutate the original without
	// affecting subsequent calls.
	cp := *s.resp
	if cp.Flags != nil {
		flags := make(map[string]string, len(s.resp.Flags))
		for k, v := range s.resp.Flags {
			flags[k] = v
		}
		cp.Flags = flags
	}
	if cp.ReadQueries != nil {
		rq := make([]string, len(s.resp.ReadQueries))
		copy(rq, s.resp.ReadQueries)
		cp.ReadQueries = rq
	}
	return &cp, nil
}

// knownActionKeys is a representative subset of the Router's registered
// handlers used in tests.
var knownActionKeys = []string{
	"help",
	"version",
	"ping",
	"agents.list",
	"agents.show",
	"agents.create",
	"agents.stop",
	"agents.start",
	"secrets.list",
	"secrets.set",
	"audit.tail",
	"gosuto.show",
}

func newClassifier(p nlp.Provider) *nlp.Classifier {
	return nlp.NewClassifier(p, knownActionKeys)
}

// ---------------------------------------------------------------------------
// Intent type coverage
// ---------------------------------------------------------------------------

// TestClassifier_IntentCommand verifies that a high-confidence command intent
// is passed through unchanged when the action key is valid.
func TestClassifier_IntentCommand(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Args:        []string{},
			Flags:       map[string]string{"name": "saito", "template": "saito-agent"},
			Explanation: "Create a new Saito agent.",
			Confidence:  0.95,
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "set up Saito"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Intent != nlp.IntentCommand {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentCommand)
	}
	if got.Action != "agents.create" {
		t.Errorf("action: got %q, want %q", got.Action, "agents.create")
	}
	if got.Flags["name"] != "saito" {
		t.Errorf("flag name: got %q, want %q", got.Flags["name"], "saito")
	}
}

// TestClassifier_IntentConversational verifies that a conversational intent
// (e.g. "how many agents are running?") is passed through with read_queries
// populated.
func TestClassifier_IntentConversational(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:      nlp.IntentConversational,
			ReadQueries: []string{"agents.list"},
			Explanation: "User wants to know the list of running agents.",
			Confidence:  0.9,
			Response:    "Let me look that up for you.",
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "how many agents are running?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Intent != nlp.IntentConversational {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentConversational)
	}
	if len(got.ReadQueries) == 0 || got.ReadQueries[0] != "agents.list" {
		t.Errorf("read_queries: got %v, want [agents.list]", got.ReadQueries)
	}
}

// TestClassifier_IntentUnknown verifies that an explicit unknown intent from
// the LLM is passed through.
func TestClassifier_IntentUnknown(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:      nlp.IntentUnknown,
			Confidence:  0.3,
			Explanation: "Could not determine intent.",
			Response:    "I'm not sure what you mean.",
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "banana"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Intent != nlp.IntentUnknown {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentUnknown)
	}
	if got.Response == "" {
		t.Error("Response should be non-empty for unknown intent")
	}
}

// ---------------------------------------------------------------------------
// Confidence threshold tests
// ---------------------------------------------------------------------------

// TestClassifier_HighConfidence_PassThrough verifies that a response with
// confidence ≥ 0.8 is returned unchanged (beyond flag sanitisation).
func TestClassifier_HighConfidence_PassThrough(t *testing.T) {
	explanation := "List all registered agents."
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:      nlp.IntentCommand,
			Action:      "agents.list",
			Confidence:  0.9,
			Explanation: explanation,
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "show all agents"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Explanation != explanation {
		t.Errorf("explanation changed unexpectedly: got %q, want %q", got.Explanation, explanation)
	}
	// Response should remain empty for high confidence commands.
	if got.Response != "" {
		t.Errorf("unexpected Response for high-confidence command: %q", got.Response)
	}
}

// TestClassifier_MidConfidence_AskForConfirmation verifies that mid-confidence
// responses (0.5 ≤ confidence < 0.8) preserve the structured fields but
// replace the Response field with a clarification question.
func TestClassifier_MidConfidence_AskForConfirmation(t *testing.T) {
	cases := []struct {
		name       string
		confidence float64
	}{
		{"at_lower_bound", 0.5},
		{"mid_range", 0.65},
		{"just_below_high", 0.79},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubProvider{
				resp: &nlp.ClassifyResponse{
					Intent:      nlp.IntentCommand,
					Action:      "agents.stop",
					Args:        []string{"saito"},
					Confidence:  tc.confidence,
					Explanation: "Stop the Saito agent.",
				},
			}
			c := newClassifier(stub)

			got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "kill saito"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Structured fields must be preserved so the caller can dispatch
			// after user confirmation.
			if got.Intent != nlp.IntentCommand {
				t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentCommand)
			}
			if got.Action != "agents.stop" {
				t.Errorf("action: got %q, want %q", got.Action, "agents.stop")
			}
			if len(got.Args) == 0 || got.Args[0] != "saito" {
				t.Errorf("args: got %v, want [saito]", got.Args)
			}

			// Response must contain a clarification prompt.
			if got.Response == "" {
				t.Error("Response is empty for mid-confidence classification")
			}
			if !strings.Contains(got.Response, "Is that right?") &&
				!strings.Contains(got.Response, "is that right?") {
				t.Errorf("Response does not contain clarification question: %q", got.Response)
			}
		})
	}
}

// TestClassifier_LowConfidence_SurfacesClarificationPrompt verifies that
// responses with confidence < 0.5 are downgraded to IntentUnknown and a
// friendly clarification message is returned.
func TestClassifier_LowConfidence_SurfacesClarificationPrompt(t *testing.T) {
	cases := []struct {
		name       string
		confidence float64
	}{
		{"at_zero", 0.0},
		{"well_below", 0.2},
		{"just_below_mid", 0.49},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubProvider{
				resp: &nlp.ClassifyResponse{
					Intent:      nlp.IntentCommand,
					Action:      "agents.create",
					Confidence:  tc.confidence,
					Explanation: "Maybe create something?",
				},
			}
			c := newClassifier(stub)

			got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "do the thing"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.Intent != nlp.IntentUnknown {
				t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentUnknown)
			}
			if got.Response == "" {
				t.Error("Response should be non-empty for low-confidence classification")
			}
			// Must include help hint.
			if !strings.Contains(got.Response, "/ruriko help") {
				t.Errorf("low-confidence Response does not mention /ruriko help: %q", got.Response)
			}
		})
	}
}

// TestClassifier_AtExactConfidenceBoundaries exercises the exact boundary
// values to ensure the thresholds are half-open [lower, upper).
func TestClassifier_AtExactConfidenceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		confidence float64
		wantIntent nlp.Intent
		wantAskQ   bool // want a clarification question in Response
	}{
		// Below MidConfidenceThreshold → downgraded to unknown
		{0.499, nlp.IntentUnknown, false},
		// At MidConfidenceThreshold → mid range (ask for confirmation)
		{nlp.MidConfidenceThreshold, nlp.IntentCommand, true},
		// Just below HighConfidenceThreshold → still mid range
		{0.799, nlp.IntentCommand, true},
		// At HighConfidenceThreshold → pass through unchanged
		{nlp.HighConfidenceThreshold, nlp.IntentCommand, false},
	} {
		stub := &stubProvider{
			resp: &nlp.ClassifyResponse{
				Intent:      nlp.IntentCommand,
				Action:      "ping",
				Confidence:  tc.confidence,
				Explanation: "Ping Ruriko.",
			},
		}
		c := newClassifier(stub)

		got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "are you there?"})
		if err != nil {
			t.Fatalf("confidence=%.3f: unexpected error: %v", tc.confidence, err)
		}
		if got.Intent != tc.wantIntent {
			t.Errorf("confidence=%.3f: intent: got %q, want %q",
				tc.confidence, got.Intent, tc.wantIntent)
		}
		hasQuestion := strings.Contains(strings.ToLower(got.Response), "is that right")
		if tc.wantAskQ && !hasQuestion {
			t.Errorf("confidence=%.3f: expected clarification question in Response, got: %q",
				tc.confidence, got.Response)
		}
		if !tc.wantAskQ && hasQuestion {
			t.Errorf("confidence=%.3f: unexpected clarification question in Response: %q",
				tc.confidence, got.Response)
		}
	}
}

// ---------------------------------------------------------------------------
// Flag sanitisation tests
// ---------------------------------------------------------------------------

// TestClassifier_SanitiseInternalFlags verifies that flag keys starting with
// "_" are stripped from the response before it is returned to the caller.
func TestClassifier_SanitiseInternalFlags(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent: nlp.IntentCommand,
			Action: "agents.create",
			Flags: map[string]string{
				"name":         "saito",
				"template":     "saito-agent",
				"_approved":    "true",   // internal — must be stripped
				"_approval_id": "abc123", // internal — must be stripped
				"_trace_id":    "xyz",    // internal — must be stripped
			},
			Confidence: 0.9,
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "create saito"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for k := range got.Flags {
		if strings.HasPrefix(k, "_") {
			t.Errorf("internal flag %q was not stripped from response", k)
		}
	}
	if got.Flags["name"] != "saito" {
		t.Errorf("legitimate flag 'name' was incorrectly removed")
	}
	if got.Flags["template"] != "saito-agent" {
		t.Errorf("legitimate flag 'template' was incorrectly removed")
	}
}

// TestClassifier_NilFlags verifies that a nil flags map does not cause a
// panic and is returned as nil.
func TestClassifier_NilFlags(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:     nlp.IntentCommand,
			Action:     "agents.list",
			Flags:      nil,
			Confidence: 0.9,
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "list agents"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Flags != nil {
		t.Errorf("expected nil flags, got %v", got.Flags)
	}
}

// ---------------------------------------------------------------------------
// Action-key validation tests
// ---------------------------------------------------------------------------

// TestClassifier_InvalidActionKey verifies that an unknown action key
// produced by the LLM is rejected and converted to IntentUnknown.
func TestClassifier_InvalidActionKey(t *testing.T) {
	cases := []struct {
		name   string
		action string
	}{
		{"phantom_action", "agents.nuke"},
		{"internal_prefix", "_internal.exec"},
		{"empty_namespace", ".list"},
		{"arbitrary_string", "drop_table_users"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubProvider{
				resp: &nlp.ClassifyResponse{
					Intent:     nlp.IntentCommand,
					Action:     tc.action,
					Confidence: 0.95, // high confidence so the rejection is clearly from key validation
				},
			}
			c := newClassifier(stub)

			got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "do something bad"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Intent != nlp.IntentUnknown {
				t.Errorf("expected IntentUnknown for invalid action %q, got %q", tc.action, got.Intent)
			}
			if got.Response == "" {
				t.Error("expected non-empty Response for rejected action key")
			}
		})
	}
}

// TestClassifier_ValidActionKeys verifies that every key in the known set is
// accepted without being downgraded.
func TestClassifier_ValidActionKeys(t *testing.T) {
	for _, key := range knownActionKeys {
		t.Run(key, func(t *testing.T) {
			stub := &stubProvider{
				resp: &nlp.ClassifyResponse{
					Intent:     nlp.IntentCommand,
					Action:     key,
					Confidence: 0.9,
				},
			}
			c := newClassifier(stub)

			got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "test"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Intent != nlp.IntentCommand {
				t.Errorf("valid key %q was rejected (intent=%q)", key, got.Intent)
			}
			if got.Action != key {
				t.Errorf("action changed from %q to %q", key, got.Action)
			}
		})
	}
}

// TestClassifier_ConversationalIntentDoesNotRequireActionKey verifies that a
// conversational intent (read_queries path) does not trigger action-key
// validation even if Action is empty.
func TestClassifier_ConversationalIntentDoesNotRequireActionKey(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:      nlp.IntentConversational,
			ReadQueries: []string{"agents.list", "audit.tail"},
			Confidence:  0.88,
			Response:    "Here is a summary of your agents.",
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "what's going on?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Intent != nlp.IntentConversational {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentConversational)
	}
}

// ---------------------------------------------------------------------------
// Provider error propagation
// ---------------------------------------------------------------------------

// TestClassifier_PropagatesProviderError verifies that errors returned by the
// underlying Provider are returned to the caller unchanged.
func TestClassifier_PropagatesProviderError(t *testing.T) {
	want := errors.New("nlp: API error (rate_limit_exceeded): too many requests")
	stub := &stubProvider{err: want}
	c := newClassifier(stub)

	_, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "hello"})
	if !errors.Is(err, want) {
		t.Errorf("expected error %v, got %v", want, err)
	}
}

// TestClassifier_PropagatesContextCancellation verifies that a cancelled
// context causes the provider error to be returned.
func TestClassifier_PropagatesContextCancellation(t *testing.T) {
	stub := &stubProvider{err: context.Canceled}
	c := newClassifier(stub)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Classify(ctx, nlp.ClassifyRequest{Message: "stop everything"})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Combined / integration-style tests
// ---------------------------------------------------------------------------

// TestClassifier_MaliciousOutputAllFlagsStripped verifies that a response
// that injects internal flags alongside a valid command is sanitised
// correctly: internal flags are stripped while legitimate flags survive.
func TestClassifier_MaliciousOutputAllFlagsStripped(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent: nlp.IntentCommand,
			Action: "agents.create", // valid key present in knownActionKeys
			Flags: map[string]string{
				"_approved": "true",           // internal — must be stripped
				"_trace_id": "injected-trace", // internal — must be stripped
				"name":      "victim-agent",   // legitimate — must survive
				"template":  "kumo-agent",     // legitimate — must survive
			},
			Confidence: 0.97,
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "create victim-agent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// agents.create is a valid key, so the command must NOT be rejected.
	// Internal flags must be gone; legitimate flags must survive.
	for k := range got.Flags {
		if strings.HasPrefix(k, "_") {
			t.Errorf("internal flag %q survived sanitisation", k)
		}
	}
	if got.Flags["name"] != "victim-agent" {
		t.Errorf("legitimate flag 'name' was removed")
	}
	if got.Flags["template"] != "kumo-agent" {
		t.Errorf("legitimate flag 'template' was removed")
	}
}

// TestClassifier_ImplementsProviderInterface ensures at compile time that
// *Classifier satisfies the Provider interface.
var _ nlp.Provider = (*nlp.Classifier)(nil)

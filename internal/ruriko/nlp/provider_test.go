package nlp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

// ---------------------------------------------------------------------------
// Mock provider — verifies the interface is easily mockable for unit tests
// ---------------------------------------------------------------------------

// mockProvider is a test double for nlp.Provider.
type mockProvider struct {
	resp *nlp.ClassifyResponse
	err  error
	// captured records the last request for assertion
	captured nlp.ClassifyRequest
}

func (m *mockProvider) Classify(_ context.Context, req nlp.ClassifyRequest) (*nlp.ClassifyResponse, error) {
	m.captured = req
	return m.resp, m.err
}

// Ensure mockProvider satisfies the interface at compile time.
var _ nlp.Provider = (*mockProvider)(nil)

func TestMockProvider_Command(t *testing.T) {
	want := &nlp.ClassifyResponse{
		Intent:      nlp.IntentCommand,
		Action:      "agents.list",
		Confidence:  0.95,
		Explanation: "User wants a list of all agents.",
	}
	p := &mockProvider{resp: want}

	req := nlp.ClassifyRequest{Message: "show me all agents"}
	got, err := p.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Action != want.Action {
		t.Errorf("action: got %q, want %q", got.Action, want.Action)
	}
	if got.Intent != want.Intent {
		t.Errorf("intent: got %q, want %q", got.Intent, want.Intent)
	}
	if p.captured.Message != req.Message {
		t.Errorf("captured message: got %q, want %q", p.captured.Message, req.Message)
	}
}

func TestMockProvider_Error(t *testing.T) {
	p := &mockProvider{err: context.DeadlineExceeded}

	_, err := p.Classify(context.Background(), nlp.ClassifyRequest{Message: "hello"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// OpenAI provider — HTTP-level tests using httptest
// ---------------------------------------------------------------------------

// buildOAIResponse builds a minimal OpenAI-style response body whose single
// choice message has the given content string.
func buildOAIResponse(content string) []byte {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type choice struct {
		Message      msg    `json:"message"`
		FinishReason string `json:"finish_reason"`
	}
	type resp struct {
		Choices []choice `json:"choices"`
	}
	data, _ := json.Marshal(resp{Choices: []choice{{
		Message:      msg{Role: "assistant", Content: content},
		FinishReason: "stop",
	}}})
	return data
}

func TestOpenAIProvider_SuccessfulClassify(t *testing.T) {
	classified := nlp.ClassifyResponse{
		Intent:      nlp.IntentCommand,
		Action:      "agents.create",
		Args:        []string{},
		Flags:       map[string]string{"template": "kumo-agent", "name": "kumo"},
		Explanation: "Create a new Kumo agent using the kumo-agent template.",
		Confidence:  0.92,
	}
	classifiedJSON, _ := json.Marshal(classified)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(buildOAIResponse(string(classifiedJSON)))
	}))
	defer srv.Close()

	p := nlp.New(nlp.Config{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "gpt-4o-mini",
	})

	req := nlp.ClassifyRequest{
		Message:          "create a kumo agent",
		CommandCatalogue: "/ruriko agents.create ...",
		KnownAgents:      []string{"saito"},
		KnownTemplates:   []string{"kumo-agent"},
	}
	got, err := p.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Intent != nlp.IntentCommand {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentCommand)
	}
	if got.Action != "agents.create" {
		t.Errorf("action: got %q, want %q", got.Action, "agents.create")
	}
	if got.Flags["template"] != "kumo-agent" {
		t.Errorf("flag template: got %q, want %q", got.Flags["template"], "kumo-agent")
	}
}

func TestOpenAIProvider_APIErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided.","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	p := nlp.New(nlp.Config{APIKey: "bad-key", BaseURL: srv.URL})
	_, err := p.Classify(context.Background(), nlp.ClassifyRequest{Message: "hello"})
	if err == nil {
		t.Fatal("expected error for API error response, got nil")
	}
	if !strings.Contains(err.Error(), "API error") {
		t.Errorf("expected 'API error' in error message, got: %v", err)
	}
}

func TestOpenAIProvider_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	p := nlp.New(nlp.Config{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.Classify(context.Background(), nlp.ClassifyRequest{Message: "hello"})
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected 'no choices' in error, got: %v", err)
	}
}

func TestOpenAIProvider_MalformedClassificationJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// The model returns a plain string rather than JSON — robustness test.
		w.Write(buildOAIResponse("I cannot understand the request."))
	}))
	defer srv.Close()

	p := nlp.New(nlp.Config{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.Classify(context.Background(), nlp.ClassifyRequest{Message: "something"})
	if err == nil {
		t.Fatal("expected JSON decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode classification JSON") {
		t.Errorf("expected 'decode classification JSON' in error, got: %v", err)
	}
}

func TestOpenAIProvider_NetworkError(t *testing.T) {
	// Point at a server that is immediately closed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close before any request

	p := nlp.New(nlp.Config{APIKey: "key", BaseURL: srv.URL})
	_, err := p.Classify(context.Background(), nlp.ClassifyRequest{Message: "hello"})
	if err == nil {
		t.Fatal("expected network error, got nil")
	}
}

func TestOpenAIProvider_ContextCancellation(t *testing.T) {
	// Use a request context that is already cancelled so the http.Client
	// never even initiates the connection.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before calling Classify

	// Any reachable or unreachable server URL will do — the request must
	// fail before reaching the network.
	p := nlp.New(nlp.Config{APIKey: "key", BaseURL: "http://127.0.0.1:1"})

	_, err := p.Classify(ctx, nlp.ClassifyRequest{Message: "hello"})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// Intent constant values
// ---------------------------------------------------------------------------

func TestIntentConstants(t *testing.T) {
	if nlp.IntentCommand != "command" {
		t.Errorf("IntentCommand = %q, want %q", nlp.IntentCommand, "command")
	}
	if nlp.IntentConversational != "conversational" {
		t.Errorf("IntentConversational = %q, want %q", nlp.IntentConversational, "conversational")
	}
	if nlp.IntentUnknown != "unknown" {
		t.Errorf("IntentUnknown = %q, want %q", nlp.IntentUnknown, "unknown")
	}
}

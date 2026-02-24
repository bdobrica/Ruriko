package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLLMSummariser_SatisfiesInterface(t *testing.T) {
	s := NewLLMSummariser(LLMSummariserConfig{APIKey: "test-key"})
	var _ Summariser = s
}

func TestLLMSummariser_EmptyMessages(t *testing.T) {
	s := NewLLMSummariser(LLMSummariserConfig{APIKey: "test-key"})
	summary, err := s.Summarise(context.Background(), nil)
	if err != nil {
		t.Fatalf("Summarise(nil) error: %v", err)
	}
	if summary != "" {
		t.Errorf("expected empty summary for nil messages, got %q", summary)
	}
}

func TestLLMSummariser_SuccessfulSummary(t *testing.T) {
	wantSummary := "The user set up Saito as a cron agent and configured it to trigger Kairo every 15 minutes."

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key-abc" {
			t.Errorf("unexpected Authorization: %s", r.Header.Get("Authorization"))
		}

		var req sumRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}

		// Verify system prompt is present.
		if len(req.Messages) < 2 {
			t.Fatalf("expected at least 2 messages, got %d", len(req.Messages))
		}
		if req.Messages[0].Role != "system" {
			t.Errorf("first message should be system, got %q", req.Messages[0].Role)
		}
		if !strings.Contains(req.Messages[0].Content, "Summarise") {
			t.Errorf("system prompt should contain 'Summarise', got %q", req.Messages[0].Content)
		}

		// Verify transcript is in user message.
		if req.Messages[1].Role != "user" {
			t.Errorf("second message should be user, got %q", req.Messages[1].Role)
		}
		if !strings.Contains(req.Messages[1].Content, "set up saito") {
			t.Errorf("expected transcript to contain 'set up saito', got %q", req.Messages[1].Content)
		}

		resp := sumResponse{
			Choices: []sumChoice{
				{Message: sumMessage{Role: "assistant", Content: wantSummary}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	s := NewLLMSummariser(LLMSummariserConfig{
		APIKey:  "test-key-abc",
		BaseURL: srv.URL,
	})

	msgs := []Message{
		{Role: "user", Content: "set up saito", Timestamp: time.Now()},
		{Role: "assistant", Content: "done, saito is running on a 15-minute cron", Timestamp: time.Now()},
		{Role: "user", Content: "configure it to trigger kairo", Timestamp: time.Now()},
		{Role: "assistant", Content: "configured, kairo will receive triggers from saito", Timestamp: time.Now()},
	}

	summary, err := s.Summarise(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarise() error: %v", err)
	}
	if summary != wantSummary {
		t.Errorf("summary = %q, want %q", summary, wantSummary)
	}
}

func TestLLMSummariser_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		resp := sumResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}{
				Message: "server error",
				Type:    "server_error",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	s := NewLLMSummariser(LLMSummariserConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	msgs := []Message{{Role: "user", Content: "test"}}
	_, err := s.Summarise(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error for API error response")
	}
}

func TestLLMSummariser_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		resp := sumResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}{
				Message: "rate limit",
				Type:    "rate_limit_error",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	s := NewLLMSummariser(LLMSummariserConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	msgs := []Message{{Role: "user", Content: "test"}}
	_, err := s.Summarise(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error for rate limit")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error should mention rate limit, got %q", err.Error())
	}
}

func TestLLMSummariser_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := sumResponse{Choices: []sumChoice{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	s := NewLLMSummariser(LLMSummariserConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	msgs := []Message{{Role: "user", Content: "test"}}
	_, err := s.Summarise(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error for no choices")
	}
}

func TestLLMSummariser_TrimsWhitespace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := sumResponse{
			Choices: []sumChoice{
				{Message: sumMessage{Role: "assistant", Content: "  summary with whitespace  \n"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	s := NewLLMSummariser(LLMSummariserConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	msgs := []Message{{Role: "user", Content: "test"}}
	summary, err := s.Summarise(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarise() error: %v", err)
	}
	if summary != "summary with whitespace" {
		t.Errorf("expected trimmed summary, got %q", summary)
	}
}

func TestLLMSummariser_CustomModel(t *testing.T) {
	var receivedModel string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sumRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedModel = req.Model

		resp := sumResponse{
			Choices: []sumChoice{
				{Message: sumMessage{Role: "assistant", Content: "summary"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	s := NewLLMSummariser(LLMSummariserConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
		Model:   "gpt-4o",
	})

	msgs := []Message{{Role: "user", Content: "test"}}
	_, err := s.Summarise(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarise() error: %v", err)
	}
	if receivedModel != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %q", receivedModel)
	}
}

func TestLLMSummariser_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{broken json`))
	}))
	defer srv.Close()

	s := NewLLMSummariser(LLMSummariserConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	msgs := []Message{{Role: "user", Content: "test"}}
	_, err := s.Summarise(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// --- formatTranscript tests ---

func TestFormatTranscript_MultipleMessages(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you"},
	}
	got := formatTranscript(msgs)
	want := "user: hello\nassistant: hi there\nuser: how are you"
	if got != want {
		t.Errorf("formatTranscript() = %q, want %q", got, want)
	}
}

func TestFormatTranscript_SingleMessage(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
	}
	got := formatTranscript(msgs)
	if got != "user: hello" {
		t.Errorf("formatTranscript() = %q, want 'user: hello'", got)
	}
}

func TestFormatTranscript_Empty(t *testing.T) {
	got := formatTranscript(nil)
	if got != "" {
		t.Errorf("formatTranscript(nil) = %q, want empty", got)
	}
}

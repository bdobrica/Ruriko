package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateChatCompletion_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method: got %s want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path: got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	res, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model: "gpt-test",
		Messages: []Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletion: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", res.StatusCode)
	}
	if len(res.Response.Choices) != 1 {
		t.Fatalf("choices: got %d want 1", len(res.Response.Choices))
	}
}

func TestCreateChatCompletion_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	_, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model: "gpt-test",
		Messages: []Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

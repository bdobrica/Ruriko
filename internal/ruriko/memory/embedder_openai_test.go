package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbedder_SatisfiesInterface(t *testing.T) {
	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{APIKey: "test-key"})
	var _ Embedder = e
}

func TestOpenAIEmbedder_EmptyText(t *testing.T) {
	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{APIKey: "test-key"})
	vec, err := e.Embed(context.Background(), "")
	if err != nil {
		t.Fatalf("Embed('') error: %v", err)
	}
	if vec != nil {
		t.Errorf("expected nil for empty text, got %v", vec)
	}
}

func TestOpenAIEmbedder_SuccessfulEmbedding(t *testing.T) {
	wantEmbedding := []float32{0.1, 0.2, 0.3, 0.4, 0.5}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("expected /embeddings, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key-123" {
			t.Errorf("unexpected Authorization header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected Content-Type: %s", r.Header.Get("Content-Type"))
		}

		// Decode request body to verify model.
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "text-embedding-3-small" {
			t.Errorf("expected model text-embedding-3-small, got %q", req.Model)
		}
		if req.Input != "hello world" {
			t.Errorf("expected input 'hello world', got %q", req.Input)
		}

		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: wantEmbedding, Index: 0},
			},
			Usage: embeddingUsage{PromptTokens: 2, TotalTokens: 2},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "test-key-123",
		BaseURL: srv.URL,
	})

	vec, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(vec) != len(wantEmbedding) {
		t.Fatalf("expected %d-dim embedding, got %d", len(wantEmbedding), len(vec))
	}
	for i, v := range vec {
		if v != wantEmbedding[i] {
			t.Errorf("embedding[%d] = %f, want %f", i, v, wantEmbedding[i])
		}
	}
}

func TestOpenAIEmbedder_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		resp := embeddingResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}{
				Message: "invalid api key",
				Type:    "invalid_request_error",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "bad-key",
		BaseURL: srv.URL,
	})

	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for API error response")
	}
}

func TestOpenAIEmbedder_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		resp := embeddingResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}{
				Message: "rate limit exceeded",
				Type:    "rate_limit_error",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for rate limit")
	}
}

func TestOpenAIEmbedder_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Data:  []embeddingData{},
			Usage: embeddingUsage{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty embedding data")
	}
}

func TestOpenAIEmbedder_CustomModel(t *testing.T) {
	var receivedModel string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedModel = req.Model

		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: []float32{0.1}, Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
		Model:   "text-embedding-3-large",
	})

	_, err := e.Embed(context.Background(), "test")
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if receivedModel != "text-embedding-3-large" {
		t.Errorf("expected model text-embedding-3-large, got %q", receivedModel)
	}
}

func TestOpenAIEmbedder_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "key",
		BaseURL: srv.URL,
	})

	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
}

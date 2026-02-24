package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultEmbeddingBase    = "https://api.openai.com/v1"
	defaultEmbeddingModel   = "text-embedding-3-small"
	defaultEmbeddingTimeout = 30 * time.Second
)

// OpenAIEmbedderConfig configures the OpenAI embedding provider.
type OpenAIEmbedderConfig struct {
	// APIKey is the bearer token for authentication.
	APIKey string

	// BaseURL overrides the API endpoint. Defaults to https://api.openai.com/v1
	// when empty. Useful for Azure OpenAI, local proxies, or compatible endpoints.
	BaseURL string

	// Model is the embedding model to use.
	// Defaults to text-embedding-3-small (1536-dim, ~$0.02/1M tokens).
	Model string

	// Timeout is the HTTP request timeout. Defaults to 30 s.
	Timeout time.Duration
}

// OpenAIEmbedder implements Embedder using the OpenAI Embeddings API.
// It produces 1536-dimensional float32 vectors using text-embedding-3-small
// (configurable). The same API key used by Ruriko's NLP provider (R9) works
// here, keeping operational overhead minimal.
type OpenAIEmbedder struct {
	cfg    OpenAIEmbedderConfig
	client *http.Client
}

// NewOpenAIEmbedder creates an Embedder backed by the OpenAI (or compatible)
// embeddings API. The returned embedder is safe for concurrent use.
func NewOpenAIEmbedder(cfg OpenAIEmbedderConfig) *OpenAIEmbedder {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultEmbeddingBase
	}
	if cfg.Model == "" {
		cfg.Model = defaultEmbeddingModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultEmbeddingTimeout
	}
	return &OpenAIEmbedder{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// --- minimal OpenAI embeddings wire types ---

type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Usage embeddingUsage  `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Embed produces a vector embedding for the given text by calling the OpenAI
// embeddings API. Returns the embedding vector or an error if the API call fails.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, nil
	}

	body := embeddingRequest{
		Input: text,
		Model: e.cfg.Model,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("embedder openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.cfg.BaseURL+"/embeddings",
		bytes.NewReader(data),
	)
	if err != nil {
		return nil, fmt.Errorf("embedder openai: create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embedder openai: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embedder openai: read response body: %w", err)
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("embedder openai: decode response: %w", err)
	}

	if embResp.Error != nil {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("embedder openai: rate limit (HTTP 429): %s", embResp.Error.Message)
		}
		return nil, fmt.Errorf("embedder openai: API error (%s): %s", embResp.Error.Type, embResp.Error.Message)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("embedder openai: unexpected HTTP status %d", resp.StatusCode)
	}

	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("embedder openai: no embedding data returned")
	}

	return embResp.Data[0].Embedding, nil
}

// Compile-time interface satisfaction check.
var _ Embedder = (*OpenAIEmbedder)(nil)

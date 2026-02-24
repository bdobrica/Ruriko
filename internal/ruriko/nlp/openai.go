package nlp

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
	defaultNLPBase  = "https://api.openai.com/v1"
	defaultNLPModel = "gpt-4o-mini"
	defaultTimeout  = 30 * time.Second
)

// Config configures the OpenAI-compatible NLP provider.
type Config struct {
	// APIKey is the bearer token used to authenticate against the API.
	APIKey string

	// BaseURL overrides the API endpoint.  Useful for local models (Ollama),
	// Azure OpenAI, or any other OpenAI-compatible endpoint.
	// Defaults to https://api.openai.com/v1 when empty.
	BaseURL string

	// Model is the chat model to use.
	// Defaults to gpt-4o-mini when empty (cost-efficient, sufficient for
	// command translation).
	Model string

	// Timeout is the HTTP request timeout.  Defaults to 30 s.
	Timeout time.Duration
}

// openAIProvider implements Provider using the OpenAI chat completions API
// with JSON-mode output to guarantee a parseable ClassifyResponse.
type openAIProvider struct {
	cfg    Config
	client *http.Client
}

// New returns a Provider backed by the OpenAI (or compatible) chat API.
// The returned provider is safe for concurrent use.
func New(cfg Config) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultNLPBase
	}
	if cfg.Model == "" {
		cfg.Model = defaultNLPModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	return &openAIProvider{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// --- minimal OpenAI wire types ---

type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaiRequest struct {
	Model          string       `json:"model"`
	Messages       []oaiMessage `json:"messages"`
	MaxTokens      int          `json:"max_tokens,omitempty"`
	ResponseFormat *oaiFormat   `json:"response_format,omitempty"`
}

type oaiFormat struct {
	Type string `json:"type"` // "json_object"
}

// oaiUsage holds the token-count fields returned by the OpenAI /chat/completions
// endpoint in the top-level "usage" object.
type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type oaiResponse struct {
	Choices []oaiChoice `json:"choices"`
	Usage   oaiUsage    `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type oaiChoice struct {
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

// Classify sends the user message to the LLM and returns a ClassifyResponse.
//
// The system prompt is built via BuildSystemPrompt (see prompt.go) using
// DefaultCatalogue() so the LLM always receives a complete, up-to-date
// command catalogue.  KnownAgents and KnownTemplates are substituted fresh on
// every call so that the LLM has current context without caching.
func (p *openAIProvider) Classify(ctx context.Context, req ClassifyRequest) (*ClassifyResponse, error) {
	system := BuildSystemPrompt(DefaultCatalogue(), req.KnownAgents, req.KnownTemplates)

	// Build the messages array: system prompt, then conversation history
	// (LTM context + STM turns), then the current user message.
	msgs := make([]oaiMessage, 0, 2+len(req.ConversationHistory))
	msgs = append(msgs, oaiMessage{Role: "system", Content: system})
	for _, hm := range req.ConversationHistory {
		msgs = append(msgs, oaiMessage{Role: hm.Role, Content: hm.Content})
	}
	msgs = append(msgs, oaiMessage{Role: "user", Content: req.Message})

	body := oaiRequest{
		Model:          p.cfg.Model,
		Messages:       msgs,
		MaxTokens:      512,
		ResponseFormat: &oaiFormat{Type: "json_object"},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("nlp: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.cfg.BaseURL+"/chat/completions",
		bytes.NewReader(data),
	)
	if err != nil {
		return nil, fmt.Errorf("nlp: create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	latencyMS := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("nlp: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("nlp: read response body: %w", err)
	}

	var oaiResp oaiResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("nlp: decode API response: %w", err)
	}

	if oaiResp.Error != nil {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("nlp: API rate limit (HTTP 429): %s: %w",
				oaiResp.Error.Message, ErrRateLimit)
		}
		return nil, fmt.Errorf("nlp: API error (%s): %s", oaiResp.Error.Type, oaiResp.Error.Message)
	}

	// A non-429 HTTP error without a structured error body (e.g. 503 upstream).
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("nlp: API rate limit (HTTP 429): %w", ErrRateLimit)
		}
		return nil, fmt.Errorf("nlp: unexpected HTTP status %d", resp.StatusCode)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("nlp: no choices returned (HTTP %d)", resp.StatusCode)
	}

	content := oaiResp.Choices[0].Message.Content
	var classified ClassifyResponse
	if err := json.Unmarshal([]byte(content), &classified); err != nil {
		return nil, fmt.Errorf("nlp: decode classification JSON (%.200s): %w", content, ErrMalformedOutput)
	}

	// Attach token-usage metadata so callers can enforce budgets and write
	// cost entries to the audit trail.
	classified.Usage = &TokenUsage{
		PromptTokens:     oaiResp.Usage.PromptTokens,
		CompletionTokens: oaiResp.Usage.CompletionTokens,
		TotalTokens:      oaiResp.Usage.TotalTokens,
		Model:            p.cfg.Model,
		LatencyMS:        latencyMS,
	}

	return &classified, nil
}

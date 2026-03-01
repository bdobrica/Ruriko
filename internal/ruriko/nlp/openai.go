package nlp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	openaicore "github.com/bdobrica/Ruriko/common/llm/openai"
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
	client *openaicore.Client
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
		cfg: cfg,
		client: openaicore.New(openaicore.Config{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Timeout: cfg.Timeout,
		}),
	}
}

// Classify sends the user message to the LLM and returns a ClassifyResponse.
//
// The system prompt is built via BuildSystemPrompt (see prompt.go) using
// DefaultCatalogue() so the LLM always receives a complete, up-to-date
// command catalogue.  KnownAgents and KnownTemplates are substituted fresh on
// every call so that the LLM has current context without caching.
func (p *openAIProvider) Classify(ctx context.Context, req ClassifyRequest) (*ClassifyResponse, error) {
	system := BuildSystemPrompt(DefaultCatalogue(), req.KnownAgents, req.KnownTemplates, req.CanonicalAgents)

	// Build the messages array: system prompt, then conversation history
	// (LTM context + STM turns), then the current user message.
	msgs := make([]openaicore.Message, 0, 2+len(req.ConversationHistory))
	msgs = append(msgs, openaicore.Message{Role: "system", Content: system})
	for _, hm := range req.ConversationHistory {
		msgs = append(msgs, openaicore.Message{Role: hm.Role, Content: hm.Content})
	}
	msgs = append(msgs, openaicore.Message{Role: "user", Content: req.Message})

	body := openaicore.ChatCompletionRequest{
		Model:          p.cfg.Model,
		Messages:       msgs,
		MaxTokens:      768,
		ResponseFormat: &openaicore.ResponseFormat{Type: "json_object"},
	}

	result, err := p.client.CreateChatCompletion(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("nlp: %w", err)
	}
	oaiResp := result.Response
	latencyMS := result.LatencyMS

	if oaiResp.Error != nil {
		if result.StatusCode == 429 {
			return nil, fmt.Errorf("nlp: API rate limit (HTTP 429): %s: %w",
				oaiResp.Error.Message, ErrRateLimit)
		}
		return nil, fmt.Errorf("nlp: API error (%s): %s", oaiResp.Error.Type, oaiResp.Error.Message)
	}

	// A non-429 HTTP error without a structured error body (e.g. 503 upstream).
	if result.StatusCode >= 400 {
		if result.StatusCode == 429 {
			return nil, fmt.Errorf("nlp: API rate limit (HTTP 429): %w", ErrRateLimit)
		}
		return nil, fmt.Errorf("nlp: unexpected HTTP status %d", result.StatusCode)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("nlp: no choices returned (HTTP %d)", result.StatusCode)
	}

	content, _ := oaiResp.Choices[0].Message.Content.(string)
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

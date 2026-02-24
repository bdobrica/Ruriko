package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultSummariserBase    = "https://api.openai.com/v1"
	defaultSummariserModel   = "gpt-4o-mini"
	defaultSummariserTimeout = 30 * time.Second

	// summariserSystemPrompt instructs the LLM to produce a concise summary
	// focused on decisions and actions — the information most useful for
	// fuzzy long-term recall.
	summariserSystemPrompt = "Summarise this conversation in 2–3 sentences, focusing on decisions made and actions taken."
)

// LLMSummariserConfig configures the LLM-based summariser.
type LLMSummariserConfig struct {
	// APIKey is the bearer token for authentication.
	APIKey string

	// BaseURL overrides the API endpoint. Defaults to https://api.openai.com/v1.
	BaseURL string

	// Model is the chat model to use. Defaults to gpt-4o-mini (cheap, fast).
	Model string

	// Timeout is the HTTP request timeout. Defaults to 30 s.
	Timeout time.Duration
}

// LLMSummariser implements Summariser using an OpenAI-compatible chat
// completions API. It summarises sealed conversations into 2–3 sentence
// summaries suitable for long-term memory storage and embedding.
//
// Uses the same API key and endpoint style as Ruriko's R9 NLP provider,
// keeping operational overhead minimal.
type LLMSummariser struct {
	cfg    LLMSummariserConfig
	client *http.Client
}

// NewLLMSummariser creates a Summariser backed by an OpenAI-compatible chat API.
// The returned summariser is safe for concurrent use.
func NewLLMSummariser(cfg LLMSummariserConfig) *LLMSummariser {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultSummariserBase
	}
	if cfg.Model == "" {
		cfg.Model = defaultSummariserModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultSummariserTimeout
	}
	return &LLMSummariser{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// --- reuse the same wire types as the NLP provider (minimal subset) ---

type sumMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type sumRequest struct {
	Model     string       `json:"model"`
	Messages  []sumMessage `json:"messages"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}

type sumResponse struct {
	Choices []sumChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type sumChoice struct {
	Message sumMessage `json:"message"`
}

// Summarise produces a concise summary of a conversation transcript by
// sending it to the LLM with a summarisation system prompt. Returns the
// summary text or an error if the API call fails.
func (s *LLMSummariser) Summarise(ctx context.Context, messages []Message) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	// Format the conversation as a readable transcript for the LLM.
	transcript := formatTranscript(messages)

	msgs := []sumMessage{
		{Role: "system", Content: summariserSystemPrompt},
		{Role: "user", Content: transcript},
	}

	body := sumRequest{
		Model:     s.cfg.Model,
		Messages:  msgs,
		MaxTokens: 256,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("summariser llm: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.cfg.BaseURL+"/chat/completions",
		bytes.NewReader(data),
	)
	if err != nil {
		return "", fmt.Errorf("summariser llm: create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("summariser llm: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("summariser llm: read response body: %w", err)
	}

	var sumResp sumResponse
	if err := json.Unmarshal(respBody, &sumResp); err != nil {
		return "", fmt.Errorf("summariser llm: decode response: %w", err)
	}

	if sumResp.Error != nil {
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", fmt.Errorf("summariser llm: rate limit (HTTP 429): %s", sumResp.Error.Message)
		}
		return "", fmt.Errorf("summariser llm: API error (%s): %s", sumResp.Error.Type, sumResp.Error.Message)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("summariser llm: unexpected HTTP status %d", resp.StatusCode)
	}

	if len(sumResp.Choices) == 0 {
		return "", fmt.Errorf("summariser llm: no choices returned")
	}

	return strings.TrimSpace(sumResp.Choices[0].Message.Content), nil
}

// formatTranscript converts a slice of Message into a readable transcript
// that the LLM can summarise.
func formatTranscript(messages []Message) string {
	var b strings.Builder
	for i, m := range messages {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s: %s", m.Role, m.Content)
	}
	return b.String()
}

// Compile-time interface satisfaction check.
var _ Summariser = (*LLMSummariser)(nil)

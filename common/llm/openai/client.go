// Package openai provides a shared OpenAI-compatible chat completions
// transport used by both Ruriko and Gitai.
package openai

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
	// DefaultBaseURL is the default OpenAI-compatible API base URL.
	DefaultBaseURL = "https://api.openai.com/v1"
	// DefaultTimeout is the default HTTP timeout used when Config.Timeout is zero.
	DefaultTimeout = 120 * time.Second
)

// Config controls the shared OpenAI transport.
type Config struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

// Client is a thin OpenAI-compatible chat completions transport.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a new transport client.
func New(cfg Config) *Client {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Client{
		baseURL: base,
		apiKey:  cfg.APIKey,
		http:    &http.Client{Timeout: timeout},
	}
}

// ChatCompletionRequest is the shared OpenAI /chat/completions request body.
type ChatCompletionRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Tools          []Tool          `json:"tools,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// ResponseFormat configures OpenAI response formatting options.
type ResponseFormat struct {
	Type string `json:"type"`
}

// Message is the shared OpenAI message type.
type Message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Name       string      `json:"name,omitempty"`
}

// ToolCall is a tool invocation emitted by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is an OpenAI function call payload.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a shared OpenAI tool definition.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef defines a callable function schema.
type FunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// APIError mirrors OpenAI's error envelope.
type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Usage carries token accounting information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice is one completion candidate from OpenAI.
type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatCompletionResponse is the shared OpenAI response body.
type ChatCompletionResponse struct {
	Choices []Choice  `json:"choices"`
	Usage   Usage     `json:"usage"`
	Error   *APIError `json:"error,omitempty"`
}

// ChatCompletionResult contains transport metadata plus decoded response.
type ChatCompletionResult struct {
	StatusCode int
	LatencyMS  int64
	Response   ChatCompletionResponse
}

// CreateChatCompletion calls POST /chat/completions and decodes the response.
func (c *Client) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	start := time.Now()
	httpResp, err := c.http.Do(httpReq)
	latencyMS := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var parsed ChatCompletionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &ChatCompletionResult{
		StatusCode: httpResp.StatusCode,
		LatencyMS:  latencyMS,
		Response:   parsed,
	}, nil
}

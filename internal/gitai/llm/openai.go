package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultOpenAIBase = "https://api.openai.com/v1"

// OpenAIConfig configures the OpenAI-compatible adapter.
type OpenAIConfig struct {
	// APIKey is the bearer token for the API.
	APIKey string
	// BaseURL overrides the API endpoint (useful for local models like Ollama).
	// Defaults to https://api.openai.com/v1.
	BaseURL string
	// Model is the default model to use when CompletionRequest.Model is empty.
	Model string
	// Timeout for each HTTP request. Defaults to 120s.
	Timeout time.Duration
}

// openAIProvider implements Provider using the OpenAI chat completions API.
type openAIProvider struct {
	cfg    OpenAIConfig
	client *http.Client
}

// NewOpenAI returns a Provider backed by the OpenAI (or compatible) API.
func NewOpenAI(cfg OpenAIConfig) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOpenAIBase
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &openAIProvider{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// --- wire types (subset of the OpenAI API) ---

type oaiRequest struct {
	Model     string       `json:"model"`
	Messages  []oaiMessage `json:"messages"`
	Tools     []oaiTool    `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    interface{}   `json:"content"` // string or null
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiFunctionCall `json:"function"`
}

type oaiFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string         `json:"type"`
	Function oaiFunctionDef `json:"function"`
}

type oaiFunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

type oaiResponse struct {
	Choices []oaiChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type oaiChoice struct {
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

// Complete sends a chat completion request.
func (p *openAIProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}

	oaiMessages := make([]oaiMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := oaiMessage{
			Role:       string(m.Role),
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if m.Content != "" {
			om.Content = m.Content
		} else {
			om.Content = nil
		}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, oaiToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: oaiFunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		oaiMessages = append(oaiMessages, om)
	}

	oaiTools := make([]oaiTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		oaiTools = append(oaiTools, oaiTool{
			Type: t.Type,
			Function: oaiFunctionDef{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		})
	}

	body := oaiRequest{
		Model:     model,
		Messages:  oaiMessages,
		Tools:     oaiTools,
		MaxTokens: req.MaxTokens,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.cfg.BaseURL+"/chat/completions",
		bytes.NewReader(data),
	)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var oaiResp oaiResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if oaiResp.Error != nil {
		return nil, fmt.Errorf("openai error %s: %s", oaiResp.Error.Type, oaiResp.Error.Message)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response (status %d)", resp.StatusCode)
	}

	choice := oaiResp.Choices[0]
	msg := Message{
		Role: Role(choice.Message.Role),
	}
	if s, ok := choice.Message.Content.(string); ok {
		msg.Content = s
	}
	for _, tc := range choice.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}

	return &CompletionResponse{
		Message:      msg,
		FinishReason: choice.FinishReason,
		Usage: TokenUsage{
			PromptTokens:     oaiResp.Usage.PromptTokens,
			CompletionTokens: oaiResp.Usage.CompletionTokens,
			TotalTokens:      oaiResp.Usage.TotalTokens,
		},
	}, nil
}

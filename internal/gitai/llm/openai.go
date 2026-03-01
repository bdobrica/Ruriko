package llm

import (
	"context"
	"fmt"
	"time"

	openaicore "github.com/bdobrica/Ruriko/common/llm/openai"
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
	client *openaicore.Client
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
		cfg: cfg,
		client: openaicore.New(openaicore.Config{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Timeout: cfg.Timeout,
		}),
	}
}

// Complete sends a chat completion request.
func (p *openAIProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}

	oaiMessages := make([]openaicore.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := openaicore.Message{
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
			om.ToolCalls = append(om.ToolCalls, openaicore.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: openaicore.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		oaiMessages = append(oaiMessages, om)
	}

	oaiTools := make([]openaicore.Tool, 0, len(req.Tools))
	for _, t := range req.Tools {
		oaiTools = append(oaiTools, openaicore.Tool{
			Type: t.Type,
			Function: openaicore.FunctionDef{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		})
	}

	body := openaicore.ChatCompletionRequest{
		Model:     model,
		Messages:  oaiMessages,
		Tools:     oaiTools,
		MaxTokens: req.MaxTokens,
	}

	result, err := p.client.CreateChatCompletion(ctx, body)
	if err != nil {
		return nil, err
	}
	oaiResp := result.Response

	if oaiResp.Error != nil {
		return nil, fmt.Errorf("openai error %s: %s", oaiResp.Error.Type, oaiResp.Error.Message)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response (status %d)", result.StatusCode)
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

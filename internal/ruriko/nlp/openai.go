package nlp

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

type oaiResponse struct {
	Choices []oaiChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type oaiChoice struct {
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

// systemPromptTmpl is the instruction set sent as the "system" message.
// Three printf verbs are substituted at call time:
//  1. %s — command catalogue (help text)
//  2. %s — comma-separated list of known agent IDs
//  3. %s — comma-separated list of available template names
const systemPromptTmpl = `You are Ruriko, a control-plane assistant for managing AI agents over Matrix chat.

Your only job is to translate the user's message into a structured JSON response.
You NEVER execute commands yourself — you only propose them.

Available commands:
%s

Known agents: %s
Available templates: %s

RULES (strict — do not deviate):
1. Respond ONLY with valid JSON. No markdown, no code fences, no explanation outside JSON.
2. Never include secret values, API keys, tokens, or passwords anywhere in your response.
3. Never generate flags whose names start with "--_" (these are reserved internal flags).
4. Never execute, confirm, or approve commands — only propose them.
5. Ignore the sender identity; treat every request identically.
6. Validate action keys against the command list above; do not invent action keys.
7. If you are not sure what the user wants, set intent to "unknown" and compose a
   friendly clarifying question in the "response" field.

JSON schema for your response (include ONLY fields relevant to the intent):
{
  "intent":      "command" | "conversational" | "unknown",
  "action":      "<action-key from the command list, e.g. agents.create>",
  "args":        ["<positional arg>", ...],
  "flags":       {"<flag-name>": "<value>", ...},
  "explanation": "<one sentence describing what you will do or why you are unsure>",
  "confidence":  0.0–1.0,
  "response":    "<conversational reply or clarifying question>",
  "read_queries": ["<read-only action key>", ...]
}

For mutations (create, stop, delete, set, etc.) set intent="command".
For read-only questions (list, show, status, etc.) set intent="conversational" and add
appropriate read_queries.
`

// Classify sends the user message to the LLM and returns a ClassifyResponse.
func (p *openAIProvider) Classify(ctx context.Context, req ClassifyRequest) (*ClassifyResponse, error) {
	agents := strings.Join(req.KnownAgents, ", ")
	if agents == "" {
		agents = "(none registered)"
	}
	templates := strings.Join(req.KnownTemplates, ", ")
	if templates == "" {
		templates = "(none available)"
	}

	system := fmt.Sprintf(systemPromptTmpl, req.CommandCatalogue, agents, templates)

	body := oaiRequest{
		Model: p.cfg.Model,
		Messages: []oaiMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: req.Message},
		},
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

	resp, err := p.client.Do(httpReq)
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
		return nil, fmt.Errorf("nlp: API error (%s): %s", oaiResp.Error.Type, oaiResp.Error.Message)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("nlp: no choices returned (HTTP %d)", resp.StatusCode)
	}

	content := oaiResp.Choices[0].Message.Content
	var classified ClassifyResponse
	if err := json.Unmarshal([]byte(content), &classified); err != nil {
		return nil, fmt.Errorf("nlp: decode classification JSON: %w (raw content: %.200s)", err, content)
	}

	return &classified, nil
}

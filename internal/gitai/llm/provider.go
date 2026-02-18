// Package llm defines the LLM provider interface and common message types used
// by the Gitai turn loop.
//
// The turn loop calls Complete in a loop until the model returns a plain text
// response (no pending tool calls). Each iteration supplies the accumulated
// message history including any tool call results.
package llm

import "context"

// Role is the role of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message represents a single message in a conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // when Role == RoleTool
	Name       string     `json:"name,omitempty"`         // tool name when Role == RoleTool
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the tool name and raw JSON-encoded arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON
}

// ToolDefinition describes a tool the model may call.
type ToolDefinition struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef is the schema of a callable function.
type FunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"` // JSON Schema object
}

// CompletionRequest is the input to a single LLM inference call.
type CompletionRequest struct {
	Model     string
	Messages  []Message
	Tools     []ToolDefinition
	MaxTokens int
}

// CompletionResponse is the output from the LLM.
type CompletionResponse struct {
	// Message is the assistant message produced.
	Message Message
	// FinishReason explains why the model stopped.
	// "stop" = natural end; "tool_calls" = tool call(s) requested.
	FinishReason string
	// Usage holds token count information.
	Usage TokenUsage
}

// TokenUsage reports token consumption for billing/rate-limit tracking.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Provider is the interface that all LLM backends must implement.
type Provider interface {
	// Complete sends messages to the LLM and returns the next assistant message
	// (which may contain tool call requests).
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

// Package builtin provides the built-in tool registry for Gitai agents.
//
// Built-in tools are capabilities exposed directly by the Gitai runtime
// rather than via external MCP server processes. They are injected alongside
// MCP tools into every LLM CompletionRequest's tools parameter and dispatched
// to local handlers at execution time.
//
// Tool call routing in app.executeToolCall checks the built-in registry first.
// If the tool name is registered here, it bypasses the MCP client path and
// calls the Tool.Execute method directly. Policy evaluation still applies:
// built-in tools are evaluated with the pseudo-MCP server name "builtin" so
// that existing Gosuto capability rules (mcp: builtin, tool: <name>) control
// access via the standard first-match-wins engine.
package builtin

import (
	"context"

	"github.com/bdobrica/Ruriko/internal/gitai/llm"
)

// BuiltinMCPNamespace is the pseudo-MCP server name used during policy
// evaluation for built-in tools. Gosuto capability rules that control built-in
// tools should set mcp: builtin.
const BuiltinMCPNamespace = "builtin"

// Tool is the interface all built-in tools must implement.
type Tool interface {
	// Definition returns the LLM-facing tool definition containing the name,
	// description, and JSON Schema parameter specification. This definition is
	// included in every CompletionRequest's Tools slice.
	Definition() llm.ToolDefinition

	// Execute runs the tool with the given (JSON-decoded) arguments and
	// returns a result string for the LLM, or an error. The context carries
	// the request trace ID, deadline, and cancellation signal.
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}

// Registry holds all registered built-in tools and provides name-based lookup.
// It is not safe to call Register concurrently with IsBuiltin, Get, or
// Definitions — populate the registry at startup before serving requests.
type Registry struct {
	tools map[string]Tool
}

// New returns an empty Registry ready for tool registration.
func New() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds t to the registry. It panics if a tool with the same
// Definition().Function.Name is already registered, which indicates a
// programming error in the registration sequence.
func (r *Registry) Register(t Tool) {
	name := t.Definition().Function.Name
	if _, dup := r.tools[name]; dup {
		panic("builtin: duplicate tool registration: " + name)
	}
	r.tools[name] = t
}

// IsBuiltin reports whether name is handled by this registry.
func (r *Registry) IsBuiltin(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Get returns the Tool registered under name, or nil when not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Definitions returns LLM tool definitions for all registered built-in tools.
// The slice order is non-deterministic (map iteration); LLM providers treat
// tools as an unordered set so this is safe.
func (r *Registry) Definitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

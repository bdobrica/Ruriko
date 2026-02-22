// Package gosuto defines types for the Gosuto agent configuration schema (v1).
//
// Gosuto is the versioned YAML file that configures a Gitai agent. It separates
// policy (deterministic, enforced) from persona (cosmetic, advisory).
package gosuto

// SpecVersion is the API version string required in every Gosuto config.
const SpecVersion = "gosuto/v1"

// Config is the root type for a Gosuto agent configuration.
type Config struct {
	// APIVersion must be "gosuto/v1".
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`

	// Metadata holds descriptive metadata.
	Metadata Metadata `yaml:"metadata" json:"metadata"`

	// Trust defines who and what the agent is allowed to interact with.
	Trust Trust `yaml:"trust" json:"trust"`

	// Limits defines rate and cost constraints.
	Limits Limits `yaml:"limits,omitempty" json:"limits,omitempty"`

	// Capabilities defines capability rules (ordered; first-match-wins).
	Capabilities []Capability `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`

	// Approvals defines approval requirements for sensitive operations.
	Approvals Approvals `yaml:"approvals,omitempty" json:"approvals,omitempty"`

	// MCPs defines the MCP server processes to wire to this agent.
	MCPs []MCPServer `yaml:"mcps,omitempty" json:"mcps,omitempty"`

	// Gateways defines the inbound event gateway processes for this agent.
	Gateways []Gateway `yaml:"gateways,omitempty" json:"gateways,omitempty"`

	// Secrets lists the secret references the agent expects from Ruriko.
	Secrets []SecretRef `yaml:"secrets,omitempty" json:"secrets,omitempty"`

	// Persona defines the agent's LLM persona. Non-authoritative.
	Persona Persona `yaml:"persona,omitempty" json:"persona,omitempty"`
}

// Metadata holds descriptive information about a Gosuto config.
type Metadata struct {
	// Name is the agent name (usually matches the agent ID in Ruriko).
	Name string `yaml:"name" json:"name"`

	// Template is the template this config was derived from (informational).
	Template string `yaml:"template,omitempty" json:"template,omitempty"`

	// Description is a human-readable description of the agent's purpose.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Trust defines who the agent communicates with and under what conditions.
type Trust struct {
	// AllowedRooms is a list of Matrix room IDs the agent monitors.
	// Use "*" to allow all rooms, or list specific room IDs starting with "!".
	AllowedRooms []string `yaml:"allowedRooms" json:"allowedRooms"`

	// AllowedSenders is a list of Matrix user IDs the agent responds to.
	// Use "*" to allow all senders, or list specific MXIDs starting with "@".
	AllowedSenders []string `yaml:"allowedSenders" json:"allowedSenders"`

	// RequireE2EE specifies whether the agent will only operate in
	// end-to-end encrypted rooms.
	RequireE2EE bool `yaml:"requireE2EE,omitempty" json:"requireE2EE,omitempty"`

	// AdminRoom is the Matrix room ID used for operator control messages.
	AdminRoom string `yaml:"adminRoom,omitempty" json:"adminRoom,omitempty"`
}

// Limits defines resource constraints on agent operations.
type Limits struct {
	// MaxRequestsPerMinute is the maximum number of LLM calls per minute.
	// 0 means unlimited.
	MaxRequestsPerMinute int `yaml:"maxRequestsPerMinute,omitempty" json:"maxRequestsPerMinute,omitempty"`

	// MaxTokensPerRequest is the maximum number of tokens per LLM call.
	// 0 means unlimited.
	MaxTokensPerRequest int `yaml:"maxTokensPerRequest,omitempty" json:"maxTokensPerRequest,omitempty"`

	// MaxConcurrentRequests is the maximum number of simultaneous in-flight requests.
	// 0 means unlimited.
	MaxConcurrentRequests int `yaml:"maxConcurrentRequests,omitempty" json:"maxConcurrentRequests,omitempty"`

	// MaxMonthlyCostUSD caps monthly LLM spend in USD. 0 means unlimited.
	MaxMonthlyCostUSD float64 `yaml:"maxMonthlyCostUSD,omitempty" json:"maxMonthlyCostUSD,omitempty"`

	// MaxEventsPerMinute is the maximum number of inbound gateway events
	// processed per minute across all gateways. 0 means unlimited.
	MaxEventsPerMinute int `yaml:"maxEventsPerMinute,omitempty" json:"maxEventsPerMinute,omitempty"`
}

// Capability defines a single allow/deny rule for tool invocation.
// Rules are evaluated in order; the first match wins. If no rule matches,
// the default policy is DENY.
type Capability struct {
	// Name is a human-readable label for this rule.
	Name string `yaml:"name" json:"name"`

	// MCP is the name of the MCP server this rule applies to.
	// Use "*" to match all MCP servers.
	MCP string `yaml:"mcp,omitempty" json:"mcp,omitempty"`

	// Tool is the tool name within the MCP server.
	// Use "*" to match all tools in the given MCP server.
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`

	// Allow specifies whether the matched invocation is permitted (true) or
	// denied (false).
	Allow bool `yaml:"allow" json:"allow"`

	// RequireApproval, when true, gates the invocation behind a human approval
	// even if Allow is true.
	RequireApproval bool `yaml:"requireApproval,omitempty" json:"requireApproval,omitempty"`

	// Constraints is an optional set of key-value restrictions on the tool
	// arguments (e.g. {"url_prefix": "https://example.com"}).
	Constraints map[string]string `yaml:"constraints,omitempty" json:"constraints,omitempty"`
}

// Approvals configures the approval workflow for this agent.
type Approvals struct {
	// Enabled specifies whether the approval workflow is active for this agent.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Room is the Matrix room ID where approval requests are posted.
	Room string `yaml:"room,omitempty" json:"room,omitempty"`

	// Approvers is a list of Matrix user IDs authorised to approve requests.
	Approvers []string `yaml:"approvers,omitempty" json:"approvers,omitempty"`

	// TTLSeconds is how long an approval request waits before expiring.
	// 0 defaults to 3600 (1 hour).
	TTLSeconds int `yaml:"ttlSeconds,omitempty" json:"ttlSeconds,omitempty"`
}

// MCPServer describes a Model Context Protocol server process to be supervised
// by the Gitai runtime.
type MCPServer struct {
	// Name is a unique identifier for this MCP server within the agent.
	Name string `yaml:"name" json:"name"`

	// Command is the path or binary name to execute.
	Command string `yaml:"command" json:"command"`

	// Args are the command-line arguments for the MCP binary.
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`

	// Env holds additional environment variables passed to the MCP process.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// AutoRestart specifies whether Gitai should restart this MCP if it exits
	// unexpectedly.
	AutoRestart bool `yaml:"autoRestart,omitempty" json:"autoRestart,omitempty"`
}

// Gateway describes an inbound event gateway process to be supervised by the
// Gitai runtime. Gateways POST normalised event envelopes to the agent's local
// ACP endpoint (POST /events/{source}), allowing external triggers (cron ticks,
// webhooks, etc.) to initiate agent turns.
type Gateway struct {
	// Name is a unique identifier for this gateway within the agent.
	// Must not collide with MCP server names (they share the supervisor namespace).
	Name string `yaml:"name" json:"name"`

	// Type is the built-in gateway type. Currently "cron" and "webhook" are
	// supported. Mutually exclusive with Command.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`

	// Command is the path or binary name of an external gateway process.
	// Mutually exclusive with Type.
	Command string `yaml:"command,omitempty" json:"command,omitempty"`

	// Args are the command-line arguments for an external gateway binary.
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`

	// Env holds additional environment variables passed to the gateway process.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// Config holds gateway-specific configuration key-value pairs.
	// For cron gateways: "expression" (cron schedule) and "payload" (trigger message).
	// For webhook gateways: "authType" ("bearer" or "hmac-sha256"),
	// "hmacSecretRef" (Ruriko secret ref for HMAC key), "path" (custom route).
	Config map[string]string `yaml:"config,omitempty" json:"config,omitempty"`

	// AutoRestart specifies whether Gitai should restart this gateway process
	// if it exits unexpectedly. Applies to external gateway processes only.
	AutoRestart bool `yaml:"autoRestart,omitempty" json:"autoRestart,omitempty"`
}

// SecretRef is a reference to a Ruriko secret that should be injected into the
// agent at runtime. Ruriko pushes matching secret bindings via the ACP.
type SecretRef struct {
	// Name is the secret name in the Ruriko secret store.
	Name string `yaml:"name" json:"name"`

	// EnvVar is the environment variable the decrypted value is exposed as.
	// If empty, the secret is available via the ACP secrets endpoint but is
	// not injected as an env var.
	EnvVar string `yaml:"envVar,omitempty" json:"envVar,omitempty"`

	// Required indicates whether the agent should refuse to start if this
	// secret is unavailable.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`
}

// Persona defines the agent's LLM persona. This is purely cosmetic —
// all access control is enforced via Capability rules, not the persona.
type Persona struct {
	// SystemPrompt is the LLM system prompt injected at the start of every
	// conversation context.
	SystemPrompt string `yaml:"systemPrompt,omitempty" json:"systemPrompt,omitempty"`

	// LLMProvider is the LLM backend identifier (e.g. "openai", "anthropic").
	LLMProvider string `yaml:"llmProvider,omitempty" json:"llmProvider,omitempty"`

	// Model is the specific model name (e.g. "gpt-4o", "claude-3-5-sonnet-20241022").
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// Temperature controls LLM output randomness. Valid range: 0.0–2.0.
	// A nil pointer means "not specified" (provider default); a non-nil pointer
	// to 0.0 means "explicitly deterministic".
	Temperature *float64 `yaml:"temperature,omitempty" json:"temperature,omitempty"`

	// APIKeySecretRef is the name of the Ruriko secret that holds the LLM
	// provider API key. When set, the agent retrieves the API key from the
	// secret manager (via GetSecret) at runtime rather than from the static
	// environment config. The provider is rebuilt each time secrets are
	// refreshed via ACP /secrets/token.
	//
	// Example: if the Ruriko secret is named "openai_api_key", set:
	//   apiKeySecretRef: openai_api_key
	//
	// Leave empty to use the API key supplied via the environment config
	// (LLM.APIKey / OPENAI_API_KEY env var), which is the legacy path.
	APIKeySecretRef string `yaml:"apiKeySecretRef,omitempty" json:"apiKeySecretRef,omitempty"`
}

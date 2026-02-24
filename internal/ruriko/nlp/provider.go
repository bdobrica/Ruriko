// Package nlp provides the natural-language classification layer for Ruriko.
//
// The NLP layer sits between the raw Matrix message and the command router.
// Its sole responsibility is translation: convert a free-form sentence into
// a structured ClassifyResponse (action key + args + flags) that the existing
// Router.Dispatch pipeline can process.
//
// Security invariants (unchanged by this layer):
//   - The LLM only proposes commands; it never executes them.
//   - Every mutation still flows through validation → approval gate → audit.
//   - The LLM is shown the command catalogue and agent names only; it never
//     sees secret values, approval tokens, or internal state.
//   - Internal flags (--_*) are stripped as usual by commands.Parse.
//   - Rate limiting prevents runaway token spend per sender.
package nlp

import (
	"context"
	"errors"
)

// ErrRateLimit is returned by a Provider when the upstream LLM API reports a
// rate-limiting condition (e.g. HTTP 429 Too Many Requests).  Callers should
// surface a user-visible message instead of silently falling back to keyword
// matching, because the user's request was understood but cannot be fulfilled
// right now.
var ErrRateLimit = errors.New("nlp: upstream rate limit exceeded")

// ErrMalformedOutput is returned by a Provider when the LLM returns a
// structurally valid HTTP response whose body cannot be interpreted as a
// ClassifyResponse (e.g. JSON parse failure, unexpected schema).  Callers
// should surface a clarification prompt so the user knows to rephrase.
var ErrMalformedOutput = errors.New("nlp: malformed response from LLM")

// Intent describes what the LLM inferred from the user's message.
type Intent string

const (
	// IntentCommand means the user wants to run a Ruriko command.
	IntentCommand Intent = "command"
	// IntentConversational means the user is asking a question or chatting.
	IntentConversational Intent = "conversational"
	// IntentUnknown means the model could not determine intent with confidence.
	IntentUnknown Intent = "unknown"
)

// HistoryMessage is a single prior turn in the conversation, injected into
// the LLM context window so the model has continuity across messages.
type HistoryMessage struct {
	// Role is "user", "assistant", or "system" (for LTM context entries).
	Role string
	// Content is the message text.
	Content string
}

// ClassifyRequest is the input to a single NLP classification call.
//
// The caller is responsible for populating the context fields (command
// catalogue, agent list, template list) on each request.  These are cheap
// string slices and are intentionally not cached inside the provider so that
// stale data is not returned.
type ClassifyRequest struct {
	// Message is the raw text sent by the user.
	Message string

	// CommandCatalogue is the help text describing all available Ruriko
	// commands.  The LLM uses this to understand what actions are possible.
	CommandCatalogue string

	// KnownAgents is the list of agent IDs currently registered in Ruriko.
	KnownAgents []string

	// KnownTemplates is the list of Gosuto template names available on disk.
	KnownTemplates []string

	// SenderMXID is the Matrix user ID of the sender.  Present for
	// traceability; the system prompt instructs the model to ignore it.
	SenderMXID string

	// ConversationHistory contains prior messages from the current conversation
	// session (short-term memory) and optionally relevant past conversations
	// (long-term memory).  These are injected between the system prompt and
	// the current user message in the LLM call so the model has continuity.
	// May be nil when memory is disabled or the conversation is fresh.
	ConversationHistory []HistoryMessage
}

// ClassifyResponse is the structured output produced by the NLP provider.
//
// Only the fields relevant to the detected intent are populated:
//   - Intent == IntentCommand      → Action, Args, Flags, Explanation, Confidence
//   - Intent == IntentConversational → Response, ReadQueries, Explanation
//   - Intent == IntentUnknown      → Response (clarification prompt), Explanation
type ClassifyResponse struct {
	// Intent is the high-level category of the user's request.
	Intent Intent `json:"intent"`

	// Action is the Ruriko command action key, e.g. "agents.create".
	// Populated only when Intent == IntentCommand.
	Action string `json:"action,omitempty"`

	// Args are positional arguments for the command.
	// Populated only when Intent == IntentCommand.
	Args []string `json:"args,omitempty"`

	// Flags are key=value flag pairs for the command.
	// Populated only when Intent == IntentCommand.
	Flags map[string]string `json:"flags,omitempty"`

	// Explanation is a short human-readable summary of what the LLM decided.
	// Shown to the user before any confirmation prompt.
	Explanation string `json:"explanation,omitempty"`

	// Confidence is a 0–1 score indicating the model's certainty.
	//   ≥ 0.8  → proceed (with confirmation for mutations)
	//   0.5–0.8 → ask the user to confirm the interpretation first
	//   < 0.5  → return a clarifying question
	Confidence float64 `json:"confidence,omitempty"`

	// Response is a conversational or clarification reply.
	// Populated when Intent == IntentConversational or IntentUnknown.
	Response string `json:"response,omitempty"`

	// ReadQueries are read-only Ruriko action keys the LLM needs to call
	// to compose a conversational answer (e.g. ["agents.list"]).
	ReadQueries []string `json:"read_queries,omitempty"`

	// Steps contains an ordered list of command steps for multi-step mutation
	// requests (e.g. "set up Saito and Kumo"). When non-empty, the NL handler
	// presents each step for individual confirmation in sequence rather than
	// using the top-level Action/Args/Flags fields.
	// Only populated when Intent == IntentCommand.
	Steps []CommandStep `json:"steps,omitempty"`

	// Usage holds the token counts reported by the underlying LLM provider for
	// this call.  Nil when the provider does not report usage data (e.g. stub
	// implementations in tests).  Callers use this to enforce per-sender token
	// budgets and to write cost entries to the audit trail.
	Usage *TokenUsage `json:"-"`
}

// CommandStep is one ordered step within a multi-step mutation response.
// The LLM returns a slice of these when the user's request requires
// more than one Ruriko command to fulfil (e.g. "set up Saito and Kumo").
type CommandStep struct {
	// Action is the Ruriko command action key, e.g. "agents.create".
	Action string `json:"action"`
	// Args are positional arguments for the command.
	Args []string `json:"args,omitempty"`
	// Flags are key=value flag pairs for the command.
	Flags map[string]string `json:"flags,omitempty"`
	// Explanation is a short human-readable summary of this specific step.
	Explanation string `json:"explanation,omitempty"`
}

// TokenUsage carries the token counts reported by the upstream LLM API for a
// single classification call.  Fields are zero-valued when the provider does
// not report usage data.
type TokenUsage struct {
	// PromptTokens is the number of tokens in the input (system prompt + user message).
	PromptTokens int
	// CompletionTokens is the number of tokens in the LLM's response.
	CompletionTokens int
	// TotalTokens is PromptTokens + CompletionTokens.
	TotalTokens int
	// Model is the model name as reported by the provider (may be empty for
	// providers that do not echo it back).
	Model string
	// LatencyMS is the observed HTTP round-trip time in milliseconds.
	LatencyMS int64
}

// RateLimitMessage is the response sent to senders who exceed the per-minute
// NLP call limit.  It is defined here so callers do not need to hard-code it.
const RateLimitMessage = "⏳ I'm processing too many requests from you right now. Please try again in a moment, or use `/ruriko` commands directly."

// APIRateLimitMessage is the response sent when the upstream LLM API reports
// a rate-limit condition (HTTP 429).  Unlike RateLimitMessage (which is a
// per-sender client-side limit), this means the provider is globally throttled.
const APIRateLimitMessage = "⏳ The AI assistant is temporarily rate-limited by the upstream provider. You can still use `/ruriko help` to see all available commands."

// MalformedOutputMessage is the response sent when the LLM returns output
// that cannot be parsed as a valid ClassifyResponse.
const MalformedOutputMessage = "I didn't quite understand that. You can also use `/ruriko help` for available commands."

// TokenBudgetExceededMessage is the reply surfaced to a sender who has
// exhausted their daily token allowance.
const TokenBudgetExceededMessage = "I've reached my daily conversation limit. You can still use `/ruriko` commands directly."

// Provider classifies free-form user messages into structured Ruriko commands.
//
// Implementations must be safe for concurrent use from multiple goroutines.
// When an implementation is unavailable (e.g. network error), it should
// return a descriptive error; callers are expected to degrade gracefully to
// keyword-based intent matching (R5.4).
type Provider interface {
	// Classify sends the user message to the underlying LLM and returns a
	// structured ClassifyResponse.
	Classify(ctx context.Context, req ClassifyRequest) (*ClassifyResponse, error)
}

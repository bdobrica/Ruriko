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

import "context"

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
}

// RateLimitMessage is the response sent to senders who exceed the per-minute
// NLP call limit.  It is defined here so callers do not need to hard-code it.
const RateLimitMessage = "⏳ I'm processing too many requests from you right now. Please try again in a moment, or use `/ruriko` commands directly."

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

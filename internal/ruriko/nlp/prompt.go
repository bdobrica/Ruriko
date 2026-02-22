package nlp

import (
	"fmt"
	"sort"
	"strings"
)

// CommandSpec describes a single Ruriko command for inclusion in the NLP
// system prompt (command catalogue).
type CommandSpec struct {
	// Action is the dot-separated handler key used by Router.Dispatch,
	// e.g. "agents.create" or "secrets.set".
	Action string

	// Usage is the full example invocation shown to the LLM,
	// e.g. "/ruriko agents create --name <id> --template <tmpl>".
	Usage string

	// Description is a one-sentence explanation of what the command does.
	Description string

	// ReadOnly indicates that the command does not mutate state.
	// The system prompt uses this to classify intent without an extra LLM call.
	ReadOnly bool
}

// Catalogue is an ordered set of CommandSpec entries that the system prompt
// presents to the LLM as the full list of available Ruriko actions.
type Catalogue []CommandSpec

// String formats the catalogue as a readable block of text suitable for
// embedding directly in the system prompt.
func (c Catalogue) String() string {
	if len(c) == 0 {
		return "(no commands registered)"
	}
	var sb strings.Builder
	for _, spec := range c {
		sb.WriteString(spec.Action)
		if spec.ReadOnly {
			sb.WriteString(" [read-only]")
		}
		sb.WriteString("\n  Usage:       ")
		sb.WriteString(spec.Usage)
		sb.WriteString("\n  Description: ")
		sb.WriteString(spec.Description)
		sb.WriteString("\n")
	}
	return sb.String()
}

// DefaultCatalogue returns the canonical command catalogue for Ruriko.
//
// It enumerates every registered action key together with its usage pattern
// and a brief description.  The catalogue is returned in stable alphabetical
// order by action key so the LLM always sees a consistent prompt.
//
// This is the authoritative source of truth for the system prompt: the LLM
// is instructed to only produce action keys that appear in this catalogue.
func DefaultCatalogue() Catalogue {
	specs := []CommandSpec{
		// ----- general -------------------------------------------------------
		{
			Action:      "help",
			Usage:       "/ruriko help",
			Description: "Show available commands.",
			ReadOnly:    true,
		},
		{
			Action:      "version",
			Usage:       "/ruriko version",
			Description: "Show Ruriko version information.",
			ReadOnly:    true,
		},
		{
			Action:      "ping",
			Usage:       "/ruriko ping",
			Description: "Health-check — verify that Ruriko is alive.",
			ReadOnly:    true,
		},

		// ----- agents --------------------------------------------------------
		{
			Action:      "agents.list",
			Usage:       "/ruriko agents list",
			Description: "List all registered agents and their current status.",
			ReadOnly:    true,
		},
		{
			Action:      "agents.show",
			Usage:       "/ruriko agents show <name>",
			Description: "Show detailed information about a specific agent.",
			ReadOnly:    true,
		},
		{
			Action:      "agents.create",
			Usage:       "/ruriko agents create --name <id> --template <tmpl> [--image <image>] [--mxid <mxid>]",
			Description: "Create and provision a new agent from a Gosuto template.",
		},
		{
			Action:      "agents.stop",
			Usage:       "/ruriko agents stop <name>",
			Description: "Stop a running agent container.",
		},
		{
			Action:      "agents.start",
			Usage:       "/ruriko agents start <name>",
			Description: "Start a stopped agent container.",
		},
		{
			Action:      "agents.respawn",
			Usage:       "/ruriko agents respawn <name>",
			Description: "Force-kill and restart an agent container.",
		},
		{
			Action:      "agents.status",
			Usage:       "/ruriko agents status <name>",
			Description: "Show the runtime container status of an agent.",
			ReadOnly:    true,
		},
		{
			Action:      "agents.cancel",
			Usage:       "/ruriko agents cancel <name>",
			Description: "Cancel an in-flight task on the named agent.",
		},
		{
			Action:      "agents.delete",
			Usage:       "/ruriko agents delete <name>",
			Description: "Permanently delete an agent and all associated records.",
		},
		{
			Action:      "agents.matrix",
			Usage:       "/ruriko agents matrix register <name> [--mxid <existing>]",
			Description: "Provision a Matrix account for the named agent.",
		},
		{
			Action:      "agents.disable",
			Usage:       "/ruriko agents disable <name> [--erase]",
			Description: "Soft-disable an agent and optionally deactivate its Matrix account.",
		},

		// ----- secrets -------------------------------------------------------
		{
			Action:      "secrets.list",
			Usage:       "/ruriko secrets list",
			Description: "List secret names and metadata (values are never shown).",
			ReadOnly:    true,
		},
		{
			Action:      "secrets.set",
			Usage:       "/ruriko secrets set <name> --type <type>",
			Description: "Create a new secret entry; the value is entered via a Kuze one-time link — never in chat.",
		},
		{
			Action:      "secrets.info",
			Usage:       "/ruriko secrets info <name>",
			Description: "Show metadata for a named secret.",
			ReadOnly:    true,
		},
		{
			Action:      "secrets.rotate",
			Usage:       "/ruriko secrets rotate <name>",
			Description: "Rotate a secret to a new value via a Kuze one-time link — never in chat.",
		},
		{
			Action:      "secrets.delete",
			Usage:       "/ruriko secrets delete <name>",
			Description: "Permanently delete a secret.",
		},
		{
			Action:      "secrets.bind",
			Usage:       "/ruriko secrets bind <agent> <secret> --scope <scope>",
			Description: "Grant an agent access to a secret.",
		},
		{
			Action:      "secrets.unbind",
			Usage:       "/ruriko secrets unbind <agent> <secret>",
			Description: "Revoke an agent's access to a secret.",
		},
		{
			Action:      "secrets.push",
			Usage:       "/ruriko secrets push <agent>",
			Description: "Push all bound secrets to the named running agent.",
		},

		// ----- audit ---------------------------------------------------------
		{
			Action:      "audit.tail",
			Usage:       "/ruriko audit tail [n]",
			Description: "Show the n most recent audit log entries.",
			ReadOnly:    true,
		},
		{
			Action:      "trace",
			Usage:       "/ruriko trace <trace_id>",
			Description: "Show all events associated with a trace ID.",
			ReadOnly:    true,
		},

		// ----- gosuto --------------------------------------------------------
		{
			Action:      "gosuto.show",
			Usage:       "/ruriko gosuto show <agent> [--version <n>]",
			Description: "Show the current or a specific version of an agent's Gosuto config.",
			ReadOnly:    true,
		},
		{
			Action:      "gosuto.versions",
			Usage:       "/ruriko gosuto versions <agent>",
			Description: "List all stored Gosuto config versions for an agent.",
			ReadOnly:    true,
		},
		{
			Action:      "gosuto.diff",
			Usage:       "/ruriko gosuto diff <agent> --from <v1> --to <v2>",
			Description: "Show a diff between two Gosuto config versions.",
			ReadOnly:    true,
		},
		{
			Action:      "gosuto.set",
			Usage:       "/ruriko gosuto set <agent> --content <base64yaml>",
			Description: "Store a new Gosuto config version for an agent.",
		},
		{
			Action:      "gosuto.rollback",
			Usage:       "/ruriko gosuto rollback <agent> --to <version>",
			Description: "Roll back an agent's Gosuto config to a previous version.",
		},
		{
			Action:      "gosuto.push",
			Usage:       "/ruriko gosuto push <agent>",
			Description: "Push the current Gosuto config to the running agent.",
		},

		// ----- approvals -----------------------------------------------------
		{
			Action:      "approvals.list",
			Usage:       "/ruriko approvals list [--status pending|approved|denied|expired|cancelled]",
			Description: "List approval requests, optionally filtered by status.",
			ReadOnly:    true,
		},
		{
			Action:      "approvals.show",
			Usage:       "/ruriko approvals show <id>",
			Description: "Show details of a specific approval request.",
			ReadOnly:    true,
		},
		{
			Action:      "approve",
			Usage:       "approve <id> [reason]",
			Description: "Approve a pending operation.",
		},
		{
			Action:      "deny",
			Usage:       `deny <id> reason="<text>"`,
			Description: "Deny a pending operation with a reason.",
		},
	}

	// Sort alphabetically by action key for stable, deterministic output.
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].Action < specs[j].Action
	})

	return Catalogue(specs)
}

// systemPromptTemplate is the complete LLM "system" message template.
//
// Substitution variables (in order via fmt.Sprintf):
//  1. %s — formatted command catalogue (Catalogue.String())
//  2. %s — agent summary lines ("name — status", one per line)
//  3. %s — available template names (one per line)
//
// This constant is defined here (not in openai.go) so it can be tested and
// extended independently of the HTTP transport layer.
const systemPromptTemplate = `You are Ruriko, a control-plane assistant for managing AI agents over Matrix chat.

Your only job is to translate the user's message into a structured JSON response.
You translate user requests into Ruriko commands. You NEVER execute anything yourself.

SECURITY RULES (never violate these):
1. For mutations (create, delete, stop, config changes): always set intent="command" so the
   user can review and confirm the proposed command before anything executes.
2. For read-only queries (list, show, status, audit): set intent="conversational" and
   populate read_queries with the relevant read-only action keys.
3. Never generate flags whose names start with "--_" — these are reserved internal flags
   and must never appear in your output.
4. Never include secret values, API keys, tokens, passwords, or any credentials anywhere
   in your response.
5. If you are unsure what the user wants, set intent="unknown" and ask a short clarifying
   question in the "response" field. Do not guess.
6. Respond ONLY with valid JSON. No markdown, no code fences, no explanation outside JSON.
7. Only use action keys listed in the command catalogue below. Do not invent action keys.
8. Ignore the senderMXID field; treat every request identically regardless of sender.

COMMAND CATALOGUE:
%s
KNOWN AGENTS (name — status):
%s

AVAILABLE TEMPLATES:
%s

JSON RESPONSE SCHEMA (include only fields relevant to the intent):
{
  "intent":       "command" | "conversational" | "unknown",
  "action":       "<action key from catalogue, e.g. agents.create>",
  "args":         ["<positional argument>", ...],
  "flags":        {"<flag-name without -- prefix>": "<value>", ...},
  "explanation":  "<one sentence: what will happen or why you are unsure>",
  "confidence":   <0.0–1.0>,
  "response":     "<conversational reply or clarifying question>",
  "read_queries": ["<read-only action key>", ...]
}`

// agentSummary formats agent descriptors for the system prompt context block.
//
// Each entry should be formatted as "name — status" by the caller so the LLM
// can answer questions like "is saito running?" without an extra lookup.
// Returns a sentinel string when the slice is empty so the LLM understands no
// agents are registered yet.
func agentSummary(agents []string) string {
	if len(agents) == 0 {
		return "(none registered)"
	}
	return strings.Join(agents, "\n")
}

// templateSummary formats available template names for the system prompt.
// Returns a sentinel string when no templates are available.
func templateSummary(templates []string) string {
	if len(templates) == 0 {
		return "(none available)"
	}
	return strings.Join(templates, "\n")
}

// BuildSystemPrompt constructs the complete LLM system prompt.
//
// catalogue is the full command catalogue to present to the LLM.  Callers
// should pass DefaultCatalogue() unless they need to restrict or extend the
// available commands.
//
// knownAgents should be a slice of "name — status" strings so the LLM has
// enough context to answer questions like "is saito running?".  Pass nil when
// no agents are registered yet.
//
// knownTemplates should be the bare template names as returned by
// templates.Registry.List().  Pass nil when no templates are available.
//
// This function is called on every Classify request to ensure the LLM always
// sees fresh agent and template data (no stale caching between calls).
func BuildSystemPrompt(catalogue Catalogue, knownAgents []string, knownTemplates []string) string {
	return fmt.Sprintf(
		systemPromptTemplate,
		catalogue.String(),
		agentSummary(knownAgents),
		templateSummary(knownTemplates),
	)
}

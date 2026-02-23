package app

import (
	"fmt"
	"strings"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// buildSystemPrompt assembles the LLM system prompt from the layered Gosuto
// configuration sections. Assembly order (R14.3):
//
//  1. persona.systemPrompt  — cosmetic identity (or "You are {Name}. {Description}" fallback)
//  2. instructions.role     — operational role description
//  3. instructions.workflow — structured trigger → action workflow steps
//  4. instructions.context.user  — human user awareness (sole approver, etc.)
//  5. instructions.context.peers — known peer agents and their roles
//  6. messagingTargets      — allowed messaging targets (from R15; pass nil until that phase)
//  7. memoryContext         — optional injected memory snippets (from R10/R18; pass "" until then)
//
// Available tools are NOT listed here — they are passed as structured objects
// in the OpenAI API request's `tools` parameter by the LLM provider.
//
// messagingTargets should be a slice of "alias (roomID)" strings derived from
// messaging.allowedTargets in the Gosuto. Pass nil when the messaging section
// is not yet configured.
//
// memoryContext is an optional pre-formatted block of memory snippets (STM +
// LTM summaries) to prepend to the context. Pass an empty string when no
// memory is available.
func buildSystemPrompt(cfg *gosutospec.Config, messagingTargets []string, memoryContext string) string {
	if cfg == nil {
		return ""
	}

	var sb strings.Builder

	// ─── 1. Persona ──────────────────────────────────────────────────────────
	personaPrompt := cfg.Persona.SystemPrompt
	if personaPrompt == "" {
		personaPrompt = fmt.Sprintf("You are %s. %s", cfg.Metadata.Name, cfg.Metadata.Description)
	}
	sb.WriteString(strings.TrimSpace(personaPrompt))

	// ─── 2. Operational Role ─────────────────────────────────────────────────
	if role := strings.TrimSpace(cfg.Instructions.Role); role != "" {
		sb.WriteString("\n\n## Operational Role\n")
		sb.WriteString(role)
	}

	// ─── 3. Workflow ─────────────────────────────────────────────────────────
	if len(cfg.Instructions.Workflow) > 0 {
		sb.WriteString("\n\n## Workflow\n")
		for _, step := range cfg.Instructions.Workflow {
			trigger := strings.TrimSpace(step.Trigger)
			action := strings.TrimSpace(step.Action)
			if trigger != "" && action != "" {
				fmt.Fprintf(&sb, "- When %s:\n  → %s\n", trigger, action)
			}
		}
	}

	// ─── 4 + 5. Context (user + peers) ───────────────────────────────────────
	hasUser := strings.TrimSpace(cfg.Instructions.Context.User) != ""
	hasPeers := len(cfg.Instructions.Context.Peers) > 0

	if hasUser || hasPeers {
		sb.WriteString("\n\n## Context")

		if hasUser {
			sb.WriteString("\n\n### User\n")
			sb.WriteString(strings.TrimSpace(cfg.Instructions.Context.User))
		}

		if hasPeers {
			sb.WriteString("\n\n### Peer Agents\n")
			for _, peer := range cfg.Instructions.Context.Peers {
				name := strings.TrimSpace(peer.Name)
				role := strings.TrimSpace(peer.Role)
				if name != "" {
					if role != "" {
						fmt.Fprintf(&sb, "- **%s**: %s\n", name, role)
					} else {
						fmt.Fprintf(&sb, "- **%s**\n", name)
					}
				}
			}
		}
	}

	// ─── 6. Messaging targets ─────────────────────────────────────────────────
	if len(messagingTargets) > 0 {
		sb.WriteString("\n\n## Messaging Targets\n")
		sb.WriteString("You can send messages to the following agents and users:\n")
		for _, t := range messagingTargets {
			fmt.Fprintf(&sb, "- %s\n", t)
		}
	}

	// ─── 7. Memory context ────────────────────────────────────────────────────
	if mc := strings.TrimSpace(memoryContext); mc != "" {
		sb.WriteString("\n\n## Memory Context\n")
		sb.WriteString(mc)
	}

	return sb.String()
}

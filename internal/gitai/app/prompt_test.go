package app

// Tests for R14.3: System Prompt Assembly — Persona + Instructions.
//
// The buildSystemPrompt function assembles the Gitai LLM system prompt from
// the layered Gosuto config: persona → instructions.role → instructions.workflow
// → instructions.context.user → instructions.context.peers → messaging targets
// → memory context.
//
// These tests verify every layer is included and the fallback / empty-section
// paths degrade gracefully.

import (
	"strings"
	"testing"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// helpers ────────────────────────────────────────────────────────────────────

func ptr[T any](v T) *T { return &v }

// minimalConfig returns a Config with only the required metadata fields set
// and no persona/instructions content, so tests can build on top of a clean
// base without repeating the whole struct.
func minimalConfig(name, desc string) *gosutospec.Config {
	return &gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata: gosutospec.Metadata{
			Name:        name,
			Description: desc,
		},
	}
}

// withPersona clones cfg and adds a persona systemPrompt.
func withPersona(cfg *gosutospec.Config, prompt string) *gosutospec.Config {
	c := *cfg
	c.Persona.SystemPrompt = prompt
	return &c
}

// withInstructions clones cfg and sets the full instructions block.
func withInstructions(cfg *gosutospec.Config, inst gosutospec.Instructions) *gosutospec.Config {
	c := *cfg
	c.Instructions = inst
	return &c
}

// ── nil / empty config ───────────────────────────────────────────────────────

func TestBuildSystemPrompt_NilConfig(t *testing.T) {
	got := buildSystemPrompt(nil, nil, "")
	if got != "" {
		t.Errorf("expected empty string for nil config, got %q", got)
	}
}

// ── persona layer ────────────────────────────────────────────────────────────

func TestBuildSystemPrompt_PersonaSystemPromptUsed(t *testing.T) {
	cfg := withPersona(minimalConfig("kairo", "finance agent"), "You are Kairo, a meticulous analyst.")
	got := buildSystemPrompt(cfg, nil, "")

	if !strings.Contains(got, "You are Kairo, a meticulous analyst.") {
		t.Errorf("system prompt did not include persona.systemPrompt\ngot:\n%s", got)
	}
}

func TestBuildSystemPrompt_PersonaFallbackWhenEmpty(t *testing.T) {
	cfg := minimalConfig("kumo", "news search agent")
	got := buildSystemPrompt(cfg, nil, "")

	if !strings.Contains(got, "kumo") {
		t.Errorf("system prompt fallback must include agent name 'kumo'\ngot:\n%s", got)
	}
	if !strings.Contains(got, "news search agent") {
		t.Errorf("system prompt fallback must include agent description\ngot:\n%s", got)
	}
}

func TestBuildSystemPrompt_PersonaNotDuplicated(t *testing.T) {
	const personaText = "You are Saito, a scheduling coordinator."
	cfg := withPersona(minimalConfig("saito", "cron agent"), personaText)
	got := buildSystemPrompt(cfg, nil, "")

	if count := strings.Count(got, personaText); count != 1 {
		t.Errorf("persona text should appear exactly once, got %d occurrences", count)
	}
}

// ── instructions.role layer ──────────────────────────────────────────────────

func TestBuildSystemPrompt_InstructionsRoleIncluded(t *testing.T) {
	cfg := withInstructions(minimalConfig("kairo", ""), gosutospec.Instructions{
		Role: "You are responsible for portfolio analysis.",
	})
	got := buildSystemPrompt(cfg, nil, "")

	if !strings.Contains(got, "You are responsible for portfolio analysis.") {
		t.Errorf("system prompt must include instructions.role\ngot:\n%s", got)
	}
}

func TestBuildSystemPrompt_InstructionsRoleAbsent_NoPanic(t *testing.T) {
	cfg := minimalConfig("saito", "cron agent")
	got := buildSystemPrompt(cfg, nil, "")

	// Must not panic and must produce a non-empty string (persona fallback).
	if got == "" {
		t.Error("expected non-empty system prompt even with empty instructions")
	}
}

// ── instructions.workflow layer ──────────────────────────────────────────────

func TestBuildSystemPrompt_WorkflowStepsTriggerAndAction(t *testing.T) {
	cfg := withInstructions(minimalConfig("kairo", ""), gosutospec.Instructions{
		Workflow: []gosutospec.WorkflowStep{
			{Trigger: "cron.tick event received", Action: "Retrieve portfolio data."},
			{Trigger: "after analysis", Action: "Send tickers to Kumo for news lookup."},
		},
	})
	got := buildSystemPrompt(cfg, nil, "")

	for _, fragment := range []string{
		"cron.tick event received",
		"Retrieve portfolio data.",
		"after analysis",
		"Send tickers to Kumo for news lookup.",
	} {
		if !strings.Contains(got, fragment) {
			t.Errorf("system prompt missing workflow fragment %q\ngot:\n%s", fragment, got)
		}
	}
}

func TestBuildSystemPrompt_WorkflowStepFormatted(t *testing.T) {
	cfg := withInstructions(minimalConfig("kairo", ""), gosutospec.Instructions{
		Workflow: []gosutospec.WorkflowStep{
			{Trigger: "on trigger", Action: "do something"},
		},
	})
	got := buildSystemPrompt(cfg, nil, "")

	// The action should follow the trigger with the arrow notation.
	if !strings.Contains(got, "→") {
		t.Errorf("workflow step should use '→' arrow notation\ngot:\n%s", got)
	}
	if !strings.Contains(got, "on trigger") {
		t.Errorf("trigger text missing from workflow step\ngot:\n%s", got)
	}
	if !strings.Contains(got, "do something") {
		t.Errorf("action text missing from workflow step\ngot:\n%s", got)
	}
}

func TestBuildSystemPrompt_EmptyWorkflow_NoPanic(t *testing.T) {
	cfg := minimalConfig("saito", "cron agent")
	got := buildSystemPrompt(cfg, nil, "")

	if got == "" {
		t.Error("expected non-empty prompt even with empty workflow")
	}
	if strings.Contains(got, "## Workflow") {
		t.Errorf("expected no Workflow section when workflow is empty\ngot:\n%s", got)
	}
}

// ── instructions.context.user layer ─────────────────────────────────────────

func TestBuildSystemPrompt_UserContextIncluded(t *testing.T) {
	cfg := withInstructions(minimalConfig("kairo", ""), gosutospec.Instructions{
		Context: gosutospec.InstructionsContext{
			User: "The user (Bogdan) is the sole approver and the intended recipient of final reports.",
		},
	})
	got := buildSystemPrompt(cfg, nil, "")

	if !strings.Contains(got, "sole approver") {
		t.Errorf("system prompt must contain user context\ngot:\n%s", got)
	}
}

func TestBuildSystemPrompt_UserContextAbsent_NoSection(t *testing.T) {
	cfg := minimalConfig("saito", "cron agent")
	got := buildSystemPrompt(cfg, nil, "")

	if strings.Contains(got, "### User") {
		t.Errorf("expected no User section when context.user is empty\ngot:\n%s", got)
	}
}

// ── instructions.context.peers layer ────────────────────────────────────────

func TestBuildSystemPrompt_PeerAgentsIncluded(t *testing.T) {
	cfg := withInstructions(minimalConfig("kairo", ""), gosutospec.Instructions{
		Context: gosutospec.InstructionsContext{
			Peers: []gosutospec.PeerRef{
				{Name: "saito", Role: "Cron/trigger agent — sends you scheduled wake-up messages."},
				{Name: "kumo", Role: "News/search agent — ask it for news on specific tickers."},
			},
		},
	})
	got := buildSystemPrompt(cfg, nil, "")

	for _, fragment := range []string{"saito", "kumo", "Cron/trigger agent", "News/search agent"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("system prompt missing peer fragment %q\ngot:\n%s", fragment, got)
		}
	}
}

func TestBuildSystemPrompt_PeerAgentsAbsent_NoPeerSection(t *testing.T) {
	cfg := minimalConfig("saito", "cron agent")
	got := buildSystemPrompt(cfg, nil, "")

	if strings.Contains(got, "### Peer Agents") {
		t.Errorf("expected no Peer Agents section when no peers configured\ngot:\n%s", got)
	}
}

// ── R14.3 composite: persona + instructions (both present) ──────────────────

func TestBuildSystemPrompt_BothPersonaAndInstructions(t *testing.T) {
	temp := ptr(0.3)
	cfg := &gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kairo", Description: "finance agent"},
		Persona: gosutospec.Persona{
			SystemPrompt: "You are Kairo, a meticulous financial analyst.",
			LLMProvider:  "openai",
			Model:        "gpt-4o",
			Temperature:  temp,
		},
		Instructions: gosutospec.Instructions{
			Role: "You are responsible for portfolio analysis and market data interpretation.",
			Workflow: []gosutospec.WorkflowStep{
				{Trigger: "on message from Saito or cron event", Action: "Retrieve portfolio data via finnhub MCP, analyse market state."},
				{Trigger: "after analysis", Action: "Send relevant tickers to Kumo via matrix.send_message for news lookup."},
			},
			Context: gosutospec.InstructionsContext{
				User: "The user (Bogdan) is the sole approver and the intended recipient of final reports.",
				Peers: []gosutospec.PeerRef{
					{Name: "saito", Role: "Cron/trigger agent — sends you scheduled wake-up messages."},
					{Name: "kumo", Role: "News/search agent — you can ask it for news on specific tickers or topics."},
				},
			},
		},
	}

	got := buildSystemPrompt(cfg, nil, "")

	mustContain := []string{
		// persona
		"You are Kairo, a meticulous financial analyst.",
		// instructions.role
		"portfolio analysis and market data interpretation",
		// workflow triggers
		"on message from Saito or cron event",
		"after analysis",
		// workflow actions
		"Retrieve portfolio data via finnhub MCP",
		"Send relevant tickers to Kumo",
		// user context
		"sole approver",
		// peer agents
		"saito",
		"kumo",
		"Cron/trigger agent",
		"News/search agent",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt missing expected fragment %q\ngot:\n%s", want, got)
		}
	}
}

// ── messaging targets layer (R15 stub path) ──────────────────────────────────

func TestBuildSystemPrompt_MessagingTargetsIncluded(t *testing.T) {
	cfg := minimalConfig("saito", "cron agent")
	targets := []string{"kairo (!kairo-admin:localhost)", "user (!user-dm:localhost)"}
	got := buildSystemPrompt(cfg, targets, "")

	for _, t2 := range targets {
		if !strings.Contains(got, t2) {
			t.Errorf("system prompt missing messaging target %q\ngot:\n%s", t2, got)
		}
	}
	if !strings.Contains(got, "Messaging Targets") {
		t.Errorf("expected 'Messaging Targets' header in prompt\ngot:\n%s", got)
	}
}

func TestBuildSystemPrompt_NoMessagingTargets_NoSection(t *testing.T) {
	cfg := minimalConfig("saito", "cron agent")
	got := buildSystemPrompt(cfg, nil, "")

	if strings.Contains(got, "Messaging Targets") {
		t.Errorf("expected no Messaging Targets section when targets are nil\ngot:\n%s", got)
	}
}

// ── memory context layer (R10/R18 stub path) ─────────────────────────────────

func TestBuildSystemPrompt_MemoryContextIncluded(t *testing.T) {
	cfg := minimalConfig("kairo", "finance agent")
	memCtx := "Previous relevant conversation (2025-02-20): Bogdan asked about AAPL portfolio."
	got := buildSystemPrompt(cfg, nil, memCtx)

	if !strings.Contains(got, memCtx) {
		t.Errorf("system prompt must include injected memory context\ngot:\n%s", got)
	}
	if !strings.Contains(got, "Memory Context") {
		t.Errorf("expected 'Memory Context' header when memory is non-empty\ngot:\n%s", got)
	}
}

func TestBuildSystemPrompt_EmptyMemoryContext_NoSection(t *testing.T) {
	cfg := minimalConfig("kairo", "finance agent")
	got := buildSystemPrompt(cfg, nil, "")

	if strings.Contains(got, "Memory Context") {
		t.Errorf("expected no Memory Context section when memoryContext is empty\ngot:\n%s", got)
	}
}

// ── determinism ──────────────────────────────────────────────────────────────

func TestBuildSystemPrompt_IsDeterministic(t *testing.T) {
	cfg := &gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kairo"},
		Persona:    gosutospec.Persona{SystemPrompt: "You are Kairo."},
		Instructions: gosutospec.Instructions{
			Role: "Portfolio analysis.",
			Workflow: []gosutospec.WorkflowStep{
				{Trigger: "cron tick", Action: "Analyse portfolio."},
			},
			Context: gosutospec.InstructionsContext{
				User:  "Bogdan is the approver.",
				Peers: []gosutospec.PeerRef{{Name: "kumo", Role: "news agent"}},
			},
		},
	}
	targets := []string{"kumo (!kumo:localhost)"}
	memCtx := "Past conversation summary."

	p1 := buildSystemPrompt(cfg, targets, memCtx)
	p2 := buildSystemPrompt(cfg, targets, memCtx)

	if p1 != p2 {
		t.Error("buildSystemPrompt must return identical output for identical inputs")
	}
}

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

// ── R14.5: canonical template instructions render into the system prompt ────
//
// Each sub-test builds a Config that mirrors a canonical template's
// instructions block (the fragments that matter for the LLM prompt) and
// asserts that buildSystemPrompt surfaces them correctly.

func TestBuildSystemPrompt_CanonicalTemplateInstructions(t *testing.T) {
	cases := []struct {
		name           string
		persona        string
		instructions   gosutospec.Instructions
		mustContain    []string
		mustNotContain []string
	}{
		{
			name:    "saito-agent",
			persona: "You are Saito, a scheduling and trigger agent.",
			instructions: gosutospec.Instructions{
				Role: "You are a scheduling coordinator. Your only job is to send trigger messages\nto the appropriate peer agents when the cron schedule fires.",
				Workflow: []gosutospec.WorkflowStep{
					{
						Trigger: "cron.tick event received",
						Action:  "Send a trigger message to each configured peer agent via matrix.send_message to start their workflows.",
					},
				},
				Context: gosutospec.InstructionsContext{
					User:  "The user is the sole approver and oversees all agent workflows.",
					Peers: nil,
				},
			},
			mustContain: []string{
				"scheduling coordinator",
				"cron.tick event received",
				"matrix.send_message",
				"sole approver",
				"## Operational Role",
				"## Workflow",
			},
			mustNotContain: []string{"### Peer Agents"},
		},
		{
			name:    "kairo-agent",
			persona: "You are Kairo, a meticulous financial analyst.",
			instructions: gosutospec.Instructions{
				Role: "You are responsible for portfolio analysis and market data interpretation.\nYou work with Kumo (news agent) to enrich analysis with news context.",
				Workflow: []gosutospec.WorkflowStep{
					{Trigger: "on message from Saito or a cron event", Action: "Retrieve portfolio data via finnhub MCP tools. Analyse market state."},
					{Trigger: "after initial analysis", Action: "Send the relevant tickers to Kumo via matrix.send_message requesting a news summary."},
					{Trigger: "after receiving Kumo's news response", Action: "Revise the analysis incorporating the news context."},
					{Trigger: "if findings are material", Action: "Write structured analysis to the database, then send a concise final report to the user."},
					{Trigger: "if findings are not material", Action: "Write analysis to the database. Do not notify the user."},
				},
				Context: gosutospec.InstructionsContext{
					User: "The user is the sole approver and the intended recipient of final reports.",
					Peers: []gosutospec.PeerRef{
						{Name: "saito", Role: "Cron/trigger agent — sends you scheduled wake-up messages."},
						{Name: "kumo", Role: "News/search agent — send it a list of tickers or company names."},
					},
				},
			},
			mustContain: []string{
				"portfolio analysis",
				"on message from Saito or a cron event",
				"after initial analysis",
				"matrix.send_message",
				"sole approver",
				"saito",
				"kumo",
				"### Peer Agents",
			},
		},
		{
			name:    "kumo-agent",
			persona: "You are Kumo, a news and web search agent.",
			instructions: gosutospec.Instructions{
				Role: "You are a news and web search agent. When given a list of tickers, company\nnames, or topics, search for recent and relevant news using your tools.",
				Workflow: []gosutospec.WorkflowStep{
					{Trigger: "message received with tickers or topics to research", Action: "Use brave-search MCP to find recent news."},
					{Trigger: "after gathering results", Action: "Summarise findings and send summary back to the requester via matrix.send_message."},
				},
				Context: gosutospec.InstructionsContext{
					User:  "The user is the sole approver. Only contact them directly if explicitly instructed.",
					Peers: []gosutospec.PeerRef{{Name: "kairo", Role: "Finance/analysis agent — primary requester."}},
				},
			},
			mustContain: []string{
				"news and web search",
				"message received with tickers",
				"brave-search",
				"matrix.send_message",
				"kairo",
				"Finance/analysis agent",
			},
		},
		{
			name:    "browser-agent",
			persona: "You are a controlled browser automation agent.",
			instructions: gosutospec.Instructions{
				Role: "You are a controlled browser automation agent. You only take browser actions\nat explicit operator request.",
				Workflow: []gosutospec.WorkflowStep{
					{Trigger: "operator message requesting a screenshot or page observation", Action: "Use playwright screenshot tool to capture the current page state."},
					{Trigger: "operator message requesting navigation to a URL", Action: "Request approval via the approval workflow, then navigate to the URL."},
					{Trigger: "operator message requesting a click action", Action: "Request approval, then perform the approved click action."},
				},
				Context: gosutospec.InstructionsContext{
					User: "The user is the sole operator and approver. All browser actions require their explicit authorisation.",
				},
			},
			mustContain: []string{
				"browser automation",
				"operator message requesting a screenshot",
				"playwright screenshot",
				"operator message requesting navigation",
				"approval workflow",
				"sole operator and approver",
			},
			mustNotContain: []string{"### Peer Agents"},
		},
		{
			name:    "email-agent",
			persona: "You are an email-reactive agent.",
			instructions: gosutospec.Instructions{
				Role: "You are an email-reactive agent. When a new email arrives via the IMAP\ngateway, summarise it clearly and concisely.",
				Workflow: []gosutospec.WorkflowStep{
					{Trigger: "imap.email event received", Action: "Read the email subject and body from the event. Optionally fetch any referenced URLs. Post a structured summary to the admin room."},
					{Trigger: "URL fetch completes", Action: "Incorporate fetched content into the summary if relevant, then post the final structured summary."},
				},
				Context: gosutospec.InstructionsContext{
					User: "The user is the sole approver and recipient of email summaries.",
				},
			},
			mustContain: []string{
				"email-reactive",
				"imap.email event received",
				"Post a structured summary",
				"URL fetch completes",
				"sole approver",
			},
			mustNotContain: []string{"### Peer Agents"},
		},
		{
			name:    "cron-agent",
			persona: "You are a scheduled task agent.",
			instructions: gosutospec.Instructions{
				Role: "You are a scheduled task agent. On each cron trigger, perform the\nconfigured periodic check using your search and fetch tools.",
				Workflow: []gosutospec.WorkflowStep{
					{Trigger: "cron.tick event received", Action: "Perform the scheduled check: search for relevant information using brave-search, optionally fetch URLs for detail, then summarise findings."},
					{Trigger: "after gathering results", Action: "Post a concise, structured summary to the admin room."},
				},
				Context: gosutospec.InstructionsContext{
					User: "The user is the sole approver and recipient of scheduled reports.",
				},
			},
			mustContain: []string{
				"scheduled task",
				"cron.tick event received",
				"brave-search",
				"after gathering results",
				"admin room",
				"sole approver",
			},
			mustNotContain: []string{"### Peer Agents"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := withInstructions(withPersona(minimalConfig(tc.name, ""), tc.persona), tc.instructions)
			got := buildSystemPrompt(cfg, nil, "")

			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("[%s] system prompt missing %q\ngot:\n%s", tc.name, want, got)
				}
			}
			for _, notWant := range tc.mustNotContain {
				if strings.Contains(got, notWant) {
					t.Errorf("[%s] system prompt should NOT contain %q\ngot:\n%s", tc.name, notWant, got)
				}
			}
		})
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

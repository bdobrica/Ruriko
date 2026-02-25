package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

type sequenceProvider struct {
	responses []*nlp.ClassifyResponse
	requests  []nlp.ClassifyRequest
}

func (p *sequenceProvider) Classify(_ context.Context, req nlp.ClassifyRequest) (*nlp.ClassifyResponse, error) {
	p.requests = append(p.requests, req)
	if len(p.responses) == 0 {
		return nil, fmt.Errorf("no classify response configured")
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	if resp == nil {
		return nil, fmt.Errorf("nil classify response")
	}
	cp := *resp
	if resp.ReadQueries != nil {
		cp.ReadQueries = append([]string(nil), resp.ReadQueries...)
	}
	if resp.Steps != nil {
		cp.Steps = append([]nlp.CommandStep(nil), resp.Steps...)
	}
	return &cp, nil
}

func newR16IntegrationHarness(provider nlp.Provider) (*Handlers, *Router) {
	h := NewHandlers(HandlersConfig{NLPProvider: provider})
	router := NewRouter("/ruriko")
	h.SetDispatch(func(ctx context.Context, action string, cmd *Command, evt *event.Event) (string, error) {
		return router.Dispatch(ctx, action, cmd, evt)
	})
	return h, router
}

// R16.4 integration-style: uppercase agent IDs from the LLM are normalised in
// the NL dispatch path before reaching the command handler.
func TestR16Integration_AgentIDSanitisedBeforeDispatch(t *testing.T) {
	provider := &sequenceProvider{responses: []*nlp.ClassifyResponse{{
		Intent:      nlp.IntentCommand,
		Action:      "agents.create",
		Flags:       map[string]string{"name": "Saito", "template": "saito-agent", "image": "gitai:latest"},
		Explanation: "Create Saito.",
		Confidence:  0.98,
	}}}

	h, router := newR16IntegrationHarness(provider)
	router.Register("agents.create", func(_ context.Context, cmd *Command, _ *event.Event) (string, error) {
		name := cmd.GetFlag("name", "")
		if err := validateAgentID(name); err != nil {
			return "", err
		}
		if name != "saito" {
			return "", fmt.Errorf("name not normalised: got %q, want %q", name, "saito")
		}
		return "created", nil
	})

	evt := nlpFakeEvent()
	if _, err := h.HandleNaturalLanguage(context.Background(), "set up Saito", evt); err != nil {
		t.Fatalf("first call: %v", err)
	}
	result, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("confirm call: %v", err)
	}
	if result != "created" {
		t.Fatalf("result: got %q, want %q", result, "created")
	}
}

// R16.5 integration-style: clarification follow-up includes original context
// via conversation history in the next classify call.
func TestR16Integration_ClarificationFollowUpHasConversationHistory(t *testing.T) {
	provider := &sequenceProvider{responses: []*nlp.ClassifyResponse{
		{
			Intent:   nlp.IntentUnknown,
			Response: "Could you clarify the schedule time?",
		},
		{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Flags:       map[string]string{"name": "saito", "template": "saito-agent", "image": "gitai:latest"},
			Explanation: "Create saito for 8am schedule.",
			Confidence:  0.91,
		},
	}}

	h, _ := newR16IntegrationHarness(provider)
	evt := nlpFakeEvent()

	_, _ = h.HandleNaturalLanguage(context.Background(), "set up saito every morning", evt)
	_, _ = h.HandleNaturalLanguage(context.Background(), "8am", evt)

	if len(provider.requests) != 2 {
		t.Fatalf("expected 2 classify requests, got %d", len(provider.requests))
	}

	history := provider.requests[1].ConversationHistory
	if len(history) == 0 {
		t.Fatalf("second classify request missing conversation history")
	}

	var sawOriginalUser, sawClarification bool
	for _, msg := range history {
		if msg.Role == "user" && strings.Contains(strings.ToLower(msg.Content), "set up saito every morning") {
			sawOriginalUser = true
		}
		if msg.Role == "assistant" && strings.Contains(strings.ToLower(msg.Content), "clarify") {
			sawClarification = true
		}
	}
	if !sawOriginalUser {
		t.Fatalf("history missing original user request: %#v", history)
	}
	if !sawClarification {
		t.Fatalf("history missing assistant clarification: %#v", history)
	}
}

// R16.6 integration-style: validation failure triggers re-query correction and
// avoids repeating the same broken command indefinitely.
func TestR16Integration_RequeryFixesValidationFailure(t *testing.T) {
	provider := &sequenceProvider{responses: []*nlp.ClassifyResponse{
		{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Flags:       map[string]string{"name": "Saito", "template": "saito-agent", "image": "gitai:latest"},
			Explanation: "Create Saito.",
			Confidence:  0.96,
		},
		{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Flags:       map[string]string{"name": "saito", "template": "saito-agent", "image": "gitai:latest"},
			Explanation: "Corrected command.",
			Confidence:  0.96,
		},
	}}

	h, router := newR16IntegrationHarness(provider)

	attempts := 0
	router.Register("agents.create", func(_ context.Context, cmd *Command, _ *event.Event) (string, error) {
		attempts++
		if attempts == 1 {
			return "", fmt.Errorf("agent ID %q is invalid: must match ^[a-z0-9][a-z0-9-]{0,62}$", cmd.GetFlag("name", ""))
		}
		if cmd.GetFlag("name", "") != "saito" {
			return "", fmt.Errorf("expected corrected lowercase name, got %q", cmd.GetFlag("name", ""))
		}
		return "created-after-requery", nil
	})

	evt := nlpFakeEvent()

	_, _ = h.HandleNaturalLanguage(context.Background(), "set up Saito", evt)
	repairPrompt, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("confirm #1: %v", err)
	}
	if !strings.Contains(strings.ToLower(repairPrompt), "correct") {
		t.Fatalf("expected correction prompt, got: %q", repairPrompt)
	}

	result, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("confirm #2: %v", err)
	}
	if result != "created-after-requery" {
		t.Fatalf("result: got %q, want %q", result, "created-after-requery")
	}

	if len(provider.requests) != 2 {
		t.Fatalf("expected initial classify + correction re-query, got %d classify calls", len(provider.requests))
	}
	if !strings.Contains(strings.ToLower(provider.requests[1].Message), "failed because") {
		t.Fatalf("correction request missing failure context: %q", provider.requests[1].Message)
	}
}

// R16.6 integration-style: correction retries are capped at 2 attempts and
// then fail closed with a user-facing fallback message.
func TestR16Integration_RequeryMaxRetriesExhausted(t *testing.T) {
	provider := &sequenceProvider{responses: []*nlp.ClassifyResponse{
		{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Flags:       map[string]string{"name": "saito", "template": "saito-agent", "image": "gitai:latest"},
			Explanation: "Create Saito.",
			Confidence:  0.96,
		},
		{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Flags:       map[string]string{"name": "saito", "template": "saito-agent", "image": "gitai:latest"},
			Explanation: "Correction attempt 1.",
			Confidence:  0.96,
		},
		{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Flags:       map[string]string{"name": "saito", "template": "saito-agent", "image": "gitai:latest"},
			Explanation: "Correction attempt 2.",
			Confidence:  0.96,
		},
	}}

	h, router := newR16IntegrationHarness(provider)
	router.Register("agents.create", func(_ context.Context, _ *Command, _ *event.Event) (string, error) {
		return "", fmt.Errorf("--template is required")
	})

	evt := nlpFakeEvent()

	_, _ = h.HandleNaturalLanguage(context.Background(), "set up Saito", evt)

	reply1, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("confirm #1: %v", err)
	}
	if !strings.Contains(reply1, "attempt 1/2") {
		t.Fatalf("expected correction attempt 1/2, got: %q", reply1)
	}

	reply2, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("confirm #2: %v", err)
	}
	if !strings.Contains(reply2, "attempt 2/2") {
		t.Fatalf("expected correction attempt 2/2, got: %q", reply2)
	}

	reply3, err := h.HandleNaturalLanguage(context.Background(), "yes", evt)
	if err != nil {
		t.Fatalf("confirm #3: %v", err)
	}
	if !strings.Contains(strings.ToLower(reply3), "after 2 attempts") {
		t.Fatalf("expected max-retry fallback message, got: %q", reply3)
	}

	if len(provider.requests) != 3 {
		t.Fatalf("expected initial classify + 2 correction re-queries, got %d classify calls", len(provider.requests))
	}
}

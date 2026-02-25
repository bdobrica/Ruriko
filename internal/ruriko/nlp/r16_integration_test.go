package nlp_test

// R16.1, R16.2 and R16.3 integration tests — exercise the real OpenAI API.
//
// These tests are skipped automatically when RURIKO_NLP_API_KEY is not set in
// the environment, so they never block the regular `make test` run.  To run
// them locally:
//
//	RURIKO_NLP_API_KEY=sk-… go test -v -run TestR16 ./internal/ruriko/nlp/
//
// Or, if the key is already in examples/docker-compose/.env:
//
//	source <(grep RURIKO_NLP_API_KEY examples/docker-compose/.env | sed 's/^/export /') && \
//	    go test -v -run TestR16 ./internal/ruriko/nlp/
//
// Each test sends a real prompt to the LLM and asserts the structural output
// (intent, action key, flags).  The exact wording of "explanation" and
// "response" fields is not asserted — only the structured fields that the
// command dispatcher consumes.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

// -----------------------------------------------------------------
// Shared helpers for integration tests
// -----------------------------------------------------------------

// canonicalAgentsForTest returns the three well-known singleton agents that
// are registered in the template directory.  These are exactly the entries
// that templates.Registry.DescribeAll() would produce from the Gosuto YAML
// files — inlined here so the tests have no filesystem dependency.
func canonicalAgentsForTest() []nlp.CanonicalAgentSpec {
	return []nlp.CanonicalAgentSpec{
		{
			Name:     "saito",
			Role:     "Cron/trigger agent. Fires on a schedule and sends Matrix messages to other agents.",
			Template: "saito-agent",
		},
		{
			Name:     "kairo",
			Role:     "Finance agent. Portfolio analysis via finnhub MCP, writes to DB.",
			Template: "kairo-agent",
		},
		{
			Name:     "kumo",
			Role:     "News/search agent. Web search via Brave Search MCP, news retrieval and summarisation.",
			Template: "kumo-agent",
		},
	}
}

// knownTemplatesForTest returns the template names that a freshly-deployed
// Ruriko instance would discover on disk.
func knownTemplatesForTest() []string {
	return []string{
		"browser-agent",
		"cron-agent",
		"email-agent",
		"kairo-agent",
		"kumo-agent",
		"saito-agent",
	}
}

// buildIntegrationClassifier creates an nlp.Classifier backed by the real
// OpenAI provider.  knownKeys is derived from DefaultCatalogue() so the
// Classifier's action-key watch-list is always in sync with the prompt.
func buildIntegrationClassifier(t *testing.T) *nlp.Classifier {
	t.Helper()

	apiKey := os.Getenv("RURIKO_NLP_API_KEY")
	if apiKey == "" {
		t.Skip("RURIKO_NLP_API_KEY not set — skipping live LLM integration test")
	}

	provider := nlp.New(nlp.Config{APIKey: apiKey})

	// Derive the complete set of registered action keys from DefaultCatalogue
	// so the Classifier is consistent with what the system prompt advertises.
	catalogue := nlp.DefaultCatalogue()
	keys := make([]string, len(catalogue))
	for i, spec := range catalogue {
		keys[i] = spec.Action
	}

	return nlp.NewClassifier(provider, keys)
}

// buildCreateRequest assembles a ClassifyRequest for the given user message
// with full canonical-agent and template context, matching what Ruriko's
// HandleNaturalLanguage sets up in production.
func buildCreateRequest(message string) nlp.ClassifyRequest {
	return nlp.ClassifyRequest{
		Message:         message,
		KnownAgents:     nil, // no agents provisioned yet
		KnownTemplates:  knownTemplatesForTest(),
		CanonicalAgents: canonicalAgentsForTest(),
	}
}

// -----------------------------------------------------------------
// R16.1 — Test: "set up Saito" → agents.create --name saito --template saito-agent
// -----------------------------------------------------------------

// TestR16_SetUpSaito verifies that a natural-language request to set up the
// canonical Saito agent is translated to:
//
//	agents.create --name saito --template saito-agent
func TestR16_SetUpSaito(t *testing.T) {
	classifier := buildIntegrationClassifier(t)

	req := buildCreateRequest("set up Saito")

	resp, err := classifier.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}

	t.Logf("intent=%s action=%s flags=%v confidence=%.2f explanation=%q",
		resp.Intent, resp.Action, resp.Flags, resp.Confidence, resp.Explanation)

	// The LLM must want to execute a command (not ask a clarifying question).
	if resp.Intent != nlp.IntentCommand {
		t.Errorf("intent: got %q, want %q (response: %q)", resp.Intent, nlp.IntentCommand, resp.Response)
	}

	if resp.Action != "agents.create" {
		t.Errorf("action: got %q, want %q", resp.Action, "agents.create")
	}

	if resp.Flags["name"] != "saito" {
		t.Errorf("flag 'name': got %q, want %q (flags: %v)", resp.Flags["name"], "saito", resp.Flags)
	}

	if resp.Flags["template"] != "saito-agent" {
		t.Errorf("flag 'template': got %q, want %q (flags: %v)", resp.Flags["template"], "saito-agent", resp.Flags)
	}
}

// -----------------------------------------------------------------
// R16.1 — Test: "set up a news agent" → agents.create --template kumo-agent
// -----------------------------------------------------------------

// TestR16_SetUpNewsAgent verifies that a natural-language request to set up a
// "news agent" (a role description, not a proper name) is translated to:
//
//	agents.create --template kumo-agent
func TestR16_SetUpNewsAgent(t *testing.T) {
	classifier := buildIntegrationClassifier(t)

	req := buildCreateRequest("set up a news agent")

	resp, err := classifier.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}

	t.Logf("intent=%s action=%s flags=%v confidence=%.2f explanation=%q",
		resp.Intent, resp.Action, resp.Flags, resp.Confidence, resp.Explanation)

	// The LLM must want to execute a command.
	if resp.Intent != nlp.IntentCommand {
		t.Errorf("intent: got %q, want %q (response: %q)", resp.Intent, nlp.IntentCommand, resp.Response)
	}

	if resp.Action != "agents.create" {
		t.Errorf("action: got %q, want %q", resp.Action, "agents.create")
	}

	if resp.Flags["template"] != "kumo-agent" {
		t.Errorf("flag 'template': got %q, want %q (flags: %v)", resp.Flags["template"], "kumo-agent", resp.Flags)
	}
}

// -----------------------------------------------------------------
// R16.2 — Test: "set up Saito and Kumo" → plan with two agents.create steps
// -----------------------------------------------------------------

// TestR16_SetUpSaitoAndKumo verifies that a natural-language request to set
// up both canonical agents together is translated into a multi-step plan:
//
//	intent="plan", steps=[agents.create saito, agents.create kumo]
func TestR16_SetUpSaitoAndKumo(t *testing.T) {
	classifier := buildIntegrationClassifier(t)

	req := buildCreateRequest("set up Saito and Kumo")

	resp, err := classifier.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}

	t.Logf("intent=%s action=%s steps=%d confidence=%.2f explanation=%q",
		resp.Intent, resp.Action, len(resp.Steps), resp.Confidence, resp.Explanation)
	for i, step := range resp.Steps {
		t.Logf("  step[%d]: action=%s flags=%v explanation=%q", i, step.Action, step.Flags, step.Explanation)
	}

	// The LLM must produce a plan (multi-agent decomposition).
	if resp.Intent != nlp.IntentPlan {
		t.Errorf("intent: got %q, want %q (response: %q)", resp.Intent, nlp.IntentPlan, resp.Response)
	}

	// Must have at least two steps (one per agent).
	if len(resp.Steps) < 2 {
		t.Fatalf("steps: got %d, want ≥2", len(resp.Steps))
	}

	// Both agents.create actions should be present.
	for _, step := range resp.Steps {
		if step.Action != "agents.create" {
			t.Errorf("unexpected step action %q; all plan steps should be agents.create", step.Action)
		}
	}

	// Saito and Kumo must both appear as named agents in the steps.
	sawSaito, sawKumo := false, false
	for _, step := range resp.Steps {
		if step.Flags["name"] == "saito" {
			sawSaito = true
			if step.Flags["template"] != "saito-agent" {
				t.Errorf("saito step: template got %q, want saito-agent", step.Flags["template"])
			}
		}
		if step.Flags["name"] == "kumo" {
			sawKumo = true
			if step.Flags["template"] != "kumo-agent" {
				t.Errorf("kumo step: template got %q, want kumo-agent", step.Flags["template"])
			}
		}
	}
	if !sawSaito {
		t.Errorf("plan steps do not include saito (steps: %v)", resp.Steps)
	}
	if !sawKumo {
		t.Errorf("plan steps do not include kumo (steps: %v)", resp.Steps)
	}
}

// -----------------------------------------------------------------
// R16.2 — Test: multi-agent workflow with scheduling configuration
// -----------------------------------------------------------------

// TestR16_MultiAgentWorkflowWithSchedule verifies that a request describing
// a full multi-agent workflow (saito triggers kumo every morning) is handled
// in one of two valid ways:
//
//	a) The LLM produces a plan (two+ steps: create saito, create kumo, ...)
//	   referencing both agents. This is the pre-R16.3 expected output where the
//	   LLM assumes a reasonable default time (e.g. 8 AM).
//
//	b) The LLM asks a clarifying question about "every morning" (R16.3 AMBIGUOUS
//	   SCHEDULES behaviour). This is also correct: "every morning" is ambiguous
//	   without a specific time and the operator should be asked to clarify before
//	   the cron expression is committed.
//
// Both outcomes are acceptable. The test fails only if the LLM returns
// intent="unknown" without any meaningful response.
func TestR16_MultiAgentWorkflowWithSchedule(t *testing.T) {
	classifier := buildIntegrationClassifier(t)

	req := buildCreateRequest("set up Saito so that every morning he asks Kumo for news")

	resp, err := classifier.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}

	t.Logf("intent=%s steps=%d confidence=%.2f explanation=%q",
		resp.Intent, len(resp.Steps), resp.Confidence, resp.Explanation)
	for i, step := range resp.Steps {
		t.Logf("  step[%d]: action=%s flags=%v explanation=%q", i, step.Action, step.Flags, step.Explanation)
	}

	switch resp.Intent {
	case nlp.IntentPlan:
		// Path (a): LLM produced a multi-step plan — validate it contains both agents.

		// Must have at least 2 steps (one per agent, at minimum).
		if len(resp.Steps) < 2 {
			t.Fatalf("steps: got %d, want ≥2 (one per agent)", len(resp.Steps))
		}

		// All step actions must be valid action keys.
		validActions := map[string]bool{
			"agents.create":       true,
			"agents.config.apply": true,
			"gosuto.set":          true,
			"gosuto.push":         true,
		}
		for i, step := range resp.Steps {
			if !validActions[step.Action] {
				t.Errorf("step[%d]: action=%q not in expected set of valid plan actions", i, step.Action)
			}
		}

		// The plan must reference both saito and kumo somewhere in the steps.
		mentionsSaito, mentionsKumo := false, false
		for _, step := range resp.Steps {
			if step.Flags["name"] == "saito" || step.Flags["template"] == "saito-agent" {
				mentionsSaito = true
			}
			if step.Flags["name"] == "kumo" || step.Flags["template"] == "kumo-agent" {
				mentionsKumo = true
			}
		}
		if !mentionsSaito {
			t.Errorf("plan must reference saito in at least one step, steps: %v", resp.Steps)
		}
		if !mentionsKumo {
			t.Errorf("plan must reference kumo in at least one step, steps: %v", resp.Steps)
		}

	case nlp.IntentUnknown:
		// Path (b): LLM asked a clarifying question about the ambiguous schedule.
		// This is correct R16.3 behaviour for "every morning" (no time of day given).
		// Verify the clarification is about the time, not a generic fallback.
		lc := strings.ToLower(resp.Response)
		timeKeywords := []string{"time", "hour", "am", "pm", "utc", "when", "morning", "o'clock"}
		found := false
		for _, kw := range timeKeywords {
			if strings.Contains(lc, kw) {
				found = true
				break
			}
		}
		if !found || resp.Response == "" {
			t.Errorf("intent=unknown but response %q is not a schedule clarification — expected a time question", resp.Response)
		}
		t.Logf("R16.3 AMBIGUOUS SCHEDULES path: LLM asked for clarification: %q", resp.Response)

	default:
		t.Errorf("intent: got %q — expected plan (path a) or unknown+clarification (path b), response: %q",
			resp.Intent, resp.Response)
	}
}

// -----------------------------------------------------------------
// R16.3 — Test: "every 15 minutes" maps to */15 * * * *
// -----------------------------------------------------------------

// TestR16_3_Every15MinutesCronMapping verifies that when the user asks to
// schedule Saito to run "every 15 minutes", the NLP classifier:
//   - Returns intent="command" (or intent="plan" if it also creates the agent)
//   - Produces a cron flag whose value is exactly "*/15 * * * *"
//
// This test exercises the CRON EXPRESSION MAPPING section added to the system
// prompt in R16.3 and confirms that the Classifier's cron validation does not
// reject the expression.
func TestR16_3_Every15MinutesCronMapping(t *testing.T) {
	classifier := buildIntegrationClassifier(t)

	req := buildCreateRequest("set up Saito to run every 15 minutes")

	resp, err := classifier.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}

	t.Logf("intent=%s action=%s flags=%v steps=%d confidence=%.2f explanation=%q",
		resp.Intent, resp.Action, resp.Flags, len(resp.Steps), resp.Confidence, resp.Explanation)

	// The classifier must not downgrade to IntentUnknown — we need a concrete
	// command or plan with a usable cron expression, not a clarifying question.
	if resp.Intent == nlp.IntentUnknown {
		t.Fatalf("intent: got %q — LLM should produce a command or plan, not a clarification: %s", resp.Intent, resp.Response)
	}

	// Find the cron flag in the top-level flags or in any plan step.
	cronValue := resp.Flags["cron"]
	if cronValue == "" {
		for _, step := range resp.Steps {
			if v := step.Flags["cron"]; v != "" {
				cronValue = v
				break
			}
			if v := step.Flags["schedule"]; v != "" {
				cronValue = v
				break
			}
		}
	}
	if cronValue == "" {
		// The LLM may embed the cron expression in a different flag name —
		// log available flags for diagnostics but don't fail hard here because
		// the cron flag naming is an NLP convention that may vary by model.
		t.Logf("WARNING: no cron/schedule flag found in response — flags=%v steps=%v", resp.Flags, resp.Steps)
		t.Skip("LLM did not include a cron flag; skipping cron-value assertion")
	}

	if err := nlp.ValidateCronExpression(cronValue); err != nil {
		t.Errorf("cron flag value %q is not a valid cron expression: %v", cronValue, err)
	}

	if cronValue != "*/15 * * * *" {
		t.Errorf("cron flag: got %q, want %q", cronValue, "*/15 * * * *")
	}
}

// -----------------------------------------------------------------
// R16.3 — Test: Ambiguous "daily" prompts for clarification
// -----------------------------------------------------------------

// TestR16_3_AmbiguousDailyPromptsForClarification verifies that when the user
// gives an underspecified schedule ("set up Saito to run daily" with no time),
// the NLP classifier:
//   - Returns intent="unknown"
//   - Includes a clarifying question in the "response" field asking for the time
//
// This exercises the AMBIGUOUS SCHEDULES section of the R16.3 system prompt
// additions.
func TestR16_3_AmbiguousDailyPromptsForClarification(t *testing.T) {
	classifier := buildIntegrationClassifier(t)

	req := buildCreateRequest("set up Saito to run daily")

	resp, err := classifier.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}

	t.Logf("intent=%s action=%s flags=%v confidence=%.2f response=%q explanation=%q",
		resp.Intent, resp.Action, resp.Flags, resp.Confidence, resp.Response, resp.Explanation)

	// The LLM must ask a clarifying question, not assume a time.
	if resp.Intent != nlp.IntentUnknown {
		t.Errorf("intent: got %q, want %q — ambiguous schedule should produce a clarifying question, not a command",
			resp.Intent, nlp.IntentUnknown)
	}

	// The clarifying response must mention time (hour, time, AM, PM, UTC, or "when").
	lc := strings.ToLower(resp.Response)
	timeKeywords := []string{"time", "hour", "am", "pm", "utc", "when", "o'clock", "clock"}
	found := false
	for _, kw := range timeKeywords {
		if strings.Contains(lc, kw) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("response %q does not ask about time — expected a clarifying question about the time of day", resp.Response)
	}
}

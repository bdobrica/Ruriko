package nlp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

// ---------------------------------------------------------------------------
// ValidateCronExpression unit tests
// ---------------------------------------------------------------------------

func TestValidateCronExpression_Valid(t *testing.T) {
	valid := []string{
		// Canonical mappings from the R16.3 spec.
		"*/15 * * * *", // every 15 minutes
		"0 * * * *",    // every hour
		"0 8 * * *",    // every morning (default 8 AM)
		"0 8 * * 1",    // every Monday at 8 AM
		"0 8,20 * * *", // twice a day
		"0 8 * * 1-5",  // every weekday morning

		// Additional common forms.
		"* * * * *",     // every minute
		"0 0 * * *",     // midnight
		"0 9 * * 1-5",   // weekdays at 9 AM
		"30 6 * * *",    // 6:30 AM daily
		"0 0 1 1 *",     // Jan 1 annually
		"0 0 1 * *",     // first of the month at midnight
		"0 0 * * 0",     // Sunday midnight (dow = 0)
		"0 0 * * 7",     // Sunday midnight (dow = 7 — alias)
		"5-10 * * * *",  // minutes 5 through 10
		"*/5 */2 * * *", // every 5 min, every 2 hours
	}

	for _, expr := range valid {
		if err := nlp.ValidateCronExpression(expr); err != nil {
			t.Errorf("expected valid, got error for %q: %v", expr, err)
		}
	}
}

func TestValidateCronExpression_Invalid(t *testing.T) {
	cases := []struct {
		expr    string
		wantErr string
	}{
		// Wrong field count.
		{"* * * *", "5 fields"},
		{"* * * * * *", "5 fields"},
		{"", "5 fields"},

		// Out-of-bounds values.
		{"60 * * * *", "minute"}, // minute 60
		{"* 24 * * *", "hour"},   // hour 24
		{"* * 32 * *", "day-of-month"},
		{"* * 0 * *", "day-of-month"}, // dom min is 1
		{"* * * 13 *", "month"},       // month 13
		{"* * * 0 *", "month"},        // month 0
		{"* * * * 8", "day-of-week"},  // dow > 7

		// Invalid ranges.
		{"10-5 * * * *", "inverted"}, // lo > hi
		{"* 5-24 * * *", "out of bounds"},

		// Invalid steps.
		{"*/0 * * * *", "step"}, // step of 0
		{"*/-1 * * * *", "step"},

		// Non-numeric garbage.
		{"every * * * *", "unrecognised"},
		{"* * * * monday", "unrecognised"},
	}

	for _, tc := range cases {
		err := nlp.ValidateCronExpression(tc.expr)
		if err == nil {
			t.Errorf("expected error for %q, got nil", tc.expr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("for %q: error %q does not contain %q", tc.expr, err.Error(), tc.wantErr)
		}
	}
}

// ---------------------------------------------------------------------------
// Classifier cron-flag validation tests (unit — no LLM call)
// ---------------------------------------------------------------------------

// TestClassifier_InvalidCronFlagReturnsUnknown tests that when the LLM stub
// returns a response whose "cron" flag contains an invalid expression, the
// Classifier rewrites the response to IntentUnknown with a clarifying message.
func TestClassifier_InvalidCronFlagReturnsUnknown(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:      nlp.IntentCommand,
			Action:      "agents.create",
			Flags:       map[string]string{"name": "saito", "cron": "every morning"},
			Confidence:  0.90,
			Explanation: "Create Saito with a daily schedule.",
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "set up Saito to run every morning"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Intent != nlp.IntentUnknown {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentUnknown)
	}
	if !strings.Contains(strings.ToLower(got.Response), "schedule") {
		t.Errorf("response %q should mention 'schedule'", got.Response)
	}
}

// TestClassifier_PreservesIntentionalClarifyingQuestion verifies that when
// the LLM deliberately sets intent="unknown" with a non-empty clarifying
// question (e.g. AMBIGUOUS SCHEDULES rule), applyConfidencePolicy preserves
// that response verbatim even when confidence=0.
func TestClassifier_PreservesIntentionalClarifyingQuestion(t *testing.T) {
	clarifyMsg := "By 'every morning', do you mean a specific time — for example 8:00 AM UTC?"
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:     nlp.IntentUnknown,
			Response:   clarifyMsg,
			Confidence: 0.0, // intentionally set to unknown — no confidence score
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "set up Saito to run daily"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Intent != nlp.IntentUnknown {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentUnknown)
	}
	if got.Response != clarifyMsg {
		t.Errorf("response: got %q, want preserved LLM clarification %q", got.Response, clarifyMsg)
	}
}

// TestClassifier_ValidCronFlagPassedThrough verifies that a response whose
// cron flag is a valid expression is not rewritten by the Classifier.
func TestClassifier_ValidCronFlagPassedThrough(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent:     nlp.IntentCommand,
			Action:     "agents.create",
			Flags:      map[string]string{"name": "saito", "cron": "*/15 * * * *"},
			Confidence: 0.95,
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "set up Saito every 15 minutes"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Intent != nlp.IntentCommand {
		t.Errorf("intent: got %q, want %q", got.Intent, nlp.IntentCommand)
	}
	if got.Flags["cron"] != "*/15 * * * *" {
		t.Errorf("cron flag: got %q, want %q", got.Flags["cron"], "*/15 * * * *")
	}
}

// TestClassifier_InvalidCronInPlanStepReturnsUnknown tests that an invalid
// cron expression inside a plan step also triggers the IntentUnknown rewrite.
func TestClassifier_InvalidCronInPlanStepReturnsUnknown(t *testing.T) {
	stub := &stubProvider{
		resp: &nlp.ClassifyResponse{
			Intent: nlp.IntentPlan,
			Steps: []nlp.CommandStep{
				{
					Action:      "agents.create",
					Flags:       map[string]string{"name": "saito", "template": "saito-agent"},
					Explanation: "Create Saito.",
				},
				{
					Action:      "gosuto.set",
					Flags:       map[string]string{"schedule": "daily"},
					Explanation: "Set Saito schedule to daily.",
				},
			},
			Confidence:  0.88,
			Explanation: "Plan: create Saito then set schedule.",
		},
	}
	c := newClassifier(stub)

	got, err := c.Classify(context.Background(), nlp.ClassifyRequest{Message: "set up Saito to run daily"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Intent != nlp.IntentUnknown {
		t.Errorf("intent: got %q, want %q — plan step with invalid cron should be rejected", got.Intent, nlp.IntentUnknown)
	}
}

// TestSystemPrompt_ContainsCronMappingSection verifies that the system prompt
// generated by BuildSystemPrompt includes the cron expression mapping block
// introduced in R16.3.
func TestSystemPrompt_ContainsCronMappingSection(t *testing.T) {
	prompt := nlp.BuildSystemPrompt(
		nlp.DefaultCatalogue(),
		nil,
		nil,
		nil,
	)

	required := []string{
		"CRON EXPRESSION MAPPING",
		"*/15 * * * *",
		"AMBIGUOUS SCHEDULES",
		"intent=\"unknown\"",
	}
	for _, needle := range required {
		if !strings.Contains(prompt, needle) {
			t.Errorf("system prompt missing expected content: %q", needle)
		}
	}
}

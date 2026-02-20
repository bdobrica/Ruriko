package commands

import (
	"testing"
	"time"
)

// TestParseIntent_CreateAgent verifies that common natural-language phrases
// produce the expected IntentCreateAgent result.
func TestParseIntent_CreateAgent(t *testing.T) {
	cases := []struct {
		name         string
		input        string
		wantTemplate string
		wantAgent    string
	}{
		// --- saito-agent ---
		{
			name:         "explicit name saito",
			input:        "set up Saito",
			wantTemplate: "saito-agent",
			wantAgent:    "saito",
		},
		{
			name:         "create cron agent",
			input:        "create a cron agent",
			wantTemplate: "saito-agent",
			wantAgent:    "saito",
		},
		{
			name:         "i need a scheduler",
			input:        "I need a scheduler",
			wantTemplate: "saito-agent",
			wantAgent:    "saito",
		},
		{
			name:         "set up periodic trigger",
			input:        "Can you set up a periodic trigger agent called my-cron?",
			wantTemplate: "saito-agent",
			wantAgent:    "my-cron",
		},
		{
			name:         "spin up saito",
			input:        "spin up Saito please",
			wantTemplate: "saito-agent",
			wantAgent:    "saito",
		},
		{
			name:         "deploy scheduler named ticker",
			input:        "deploy a scheduler named ticker",
			wantTemplate: "saito-agent",
			wantAgent:    "ticker",
		},

		// --- kumo-agent ---
		{
			name:         "explicit name kumo",
			input:        "set up Kumo",
			wantTemplate: "kumo-agent",
			wantAgent:    "kumo",
		},
		{
			name:         "create a news agent",
			input:        "create a news agent",
			wantTemplate: "kumo-agent",
			wantAgent:    "kumo",
		},
		{
			name:         "i want a search agent",
			input:        "I want a search agent called news-bot",
			wantTemplate: "kumo-agent",
			wantAgent:    "news-bot",
		},
		{
			name:         "brave search agent",
			input:        "Can you add a brave search agent?",
			wantTemplate: "kumo-agent",
			wantAgent:    "kumo",
		},

		// --- browser-agent ---
		{
			name:         "browser agent",
			input:        "I need a browser agent",
			wantTemplate: "browser-agent",
			wantAgent:    "browser",
		},
		{
			name:         "playwright agent",
			input:        "set up a playwright agent called web",
			wantTemplate: "browser-agent",
			wantAgent:    "web",
		},

		// --- kairo-agent ---
		{
			name:         "explicit kairo",
			input:        "create Kairo",
			wantTemplate: "kairo-agent",
			wantAgent:    "kairo",
		},
		{
			name:         "finance agent",
			input:        "I'd like a finance agent named kairo",
			wantTemplate: "kairo-agent",
			wantAgent:    "kairo",
		},
		{
			name:         "portfolio agent",
			input:        "set up a portfolio analysis agent",
			wantTemplate: "kairo-agent",
			wantAgent:    "kairo",
		},

		// --- explicit name extraction ---
		{
			name:         "called <name>",
			input:        "create a cron agent called my-saito",
			wantTemplate: "saito-agent",
			wantAgent:    "my-saito",
		},
		{
			name:         "named <name>",
			input:        "I need a news agent named kumo2",
			wantTemplate: "kumo-agent",
			wantAgent:    "kumo2",
		},
		{
			name:         "name it <name>",
			input:        "set up a browser agent, name it web-browser",
			wantTemplate: "browser-agent",
			wantAgent:    "web-browser",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			intent := ParseIntent(tc.input)
			if intent == nil {
				t.Fatalf("ParseIntent(%q) = nil, want non-nil", tc.input)
			}
			if intent.Type != IntentCreateAgent {
				t.Errorf("Type = %q, want %q", intent.Type, IntentCreateAgent)
			}
			if intent.TemplateName != tc.wantTemplate {
				t.Errorf("TemplateName = %q, want %q", intent.TemplateName, tc.wantTemplate)
			}
			if intent.AgentName != tc.wantAgent {
				t.Errorf("AgentName = %q, want %q", intent.AgentName, tc.wantAgent)
			}
		})
	}
}

// TestParseIntent_NoIntent verifies that messages without a recognisable intent
// return nil.
func TestParseIntent_NoIntent(t *testing.T) {
	noIntentMessages := []string{
		"hello there",
		"what time is it?",
		"how is Saito doing?",    // no setup verb
		"show me the agents",     // no template keyword that maps cleanly
		"the market is up today", // "market" keyword but no setup verb
		"",
		"   ",
		"thanks!",
		"ok sounds good",
	}

	for _, msg := range noIntentMessages {
		msg := msg
		t.Run(msg, func(t *testing.T) {
			intent := ParseIntent(msg)
			if intent != nil {
				t.Errorf("ParseIntent(%q) = %+v, want nil", msg, intent)
			}
		})
	}
}

// TestTokenise verifies that the tokeniser splits text correctly.
func TestTokenise(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"hello world", []string{"hello", "world"}},
		// tokenise preserves case; callers lowercase the input first when needed.
		{"set up Saito", []string{"set", "up", "Saito"}},
		{"my-agent123", []string{"my-agent123"}},
		{"news/search agent?", []string{"news", "search", "agent"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got := tokenise(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("tokenise(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("token[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

// TestSanitiseAgentID verifies that agent ID sanitisation produces valid IDs.
func TestSanitiseAgentID(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"saito", "saito"},
		{"Saito", "saito"},
		{"My Agent", "myagent"},
		{"-bad-start-", "bad-start"},
		{"ok!", "ok"},
		{"has spaces and CAPS", "hasspacesandcaps"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got := sanitiseAgentID(tc.input)
			if got != tc.want {
				t.Errorf("sanitiseAgentID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestInferSecretTypeHint verifies that hint strings are sensible.
func TestInferSecretTypeHint(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"saito.openai-api-key", "OpenAI API key"},
		{"kumo.brave-api-key", "Brave Search API key"},
		{"kairo.finnhub-api-key", "Finnhub API key"},
		{"agent.matrix_token", "Matrix access token"},
		{"agent.unknown-secret", "API key / secret"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := inferSecretTypeHint(tc.name)
			if got != tc.want {
				t.Errorf("inferSecretTypeHint(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestConversationStore verifies basic get/set/delete with TTL expiry.
func TestConversationStore(t *testing.T) {
	cs := newConversationStore()

	session := &conversationSession{
		step:      stepAwaitingConfirmation,
		expiresAt: farFuture(),
	}

	// Store and retrieve
	cs.set("room1", "user1", session)
	got, ok := cs.get("room1", "user1")
	if !ok || got == nil {
		t.Fatal("expected session to be present")
	}

	// Different sender — should not be found
	_, ok = cs.get("room1", "user2")
	if ok {
		t.Error("unexpected session for different sender")
	}

	// Delete and verify gone
	cs.delete("room1", "user1")
	_, ok = cs.get("room1", "user1")
	if ok {
		t.Error("expected session to be gone after delete")
	}
}

// TestConversationStore_Expiry verifies that expired sessions are pruned on access.
func TestConversationStore_Expiry(t *testing.T) {
	cs := newConversationStore()

	expired := &conversationSession{
		step:      stepAwaitingConfirmation,
		expiresAt: pastTime(),
	}
	cs.set("room1", "user1", expired)

	_, ok := cs.get("room1", "user1")
	if ok {
		t.Error("expected expired session to be absent")
	}
}

// TestBuildConfirmationPrompt_AllSecrets verifies the prompt when no secrets
// are missing.
func TestBuildConfirmationPrompt_AllSecrets(t *testing.T) {
	intent := ParsedIntent{
		Type:         IntentCreateAgent,
		TemplateName: "saito-agent",
		AgentName:    "saito",
	}
	prompt := buildConfirmationPrompt(intent, nil, "ghcr.io/bdobrica/gitai:latest")

	if !contains(prompt, "saito") {
		t.Errorf("prompt missing agent name: %q", prompt)
	}
	if !contains(prompt, "Saito") {
		t.Errorf("prompt missing template display name: %q", prompt)
	}
	if !contains(prompt, "yes") {
		t.Errorf("prompt missing confirmation instruction: %q", prompt)
	}
	if contains(prompt, "Missing secrets") {
		t.Errorf("prompt should not mention missing secrets when all present: %q", prompt)
	}
}

// TestBuildConfirmationPrompt_MissingSecrets verifies the prompt when secrets are absent.
func TestBuildConfirmationPrompt_MissingSecrets(t *testing.T) {
	intent := ParsedIntent{
		Type:         IntentCreateAgent,
		TemplateName: "kumo-agent",
		AgentName:    "kumo",
	}
	missing := []string{"kumo.openai-api-key", "kumo.brave-api-key"}
	prompt := buildConfirmationPrompt(intent, missing, "ghcr.io/bdobrica/gitai:latest")

	if !contains(prompt, "kumo.openai-api-key") {
		t.Errorf("prompt missing secret name: %q", prompt)
	}
	if !contains(prompt, "kumo.brave-api-key") {
		t.Errorf("prompt missing secret name: %q", prompt)
	}
	if !contains(prompt, "/ruriko secrets set") {
		t.Errorf("prompt missing secrets set command: %q", prompt)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func contains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && (s == sub || len(s) >= len(sub) && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func farFuture() time.Time {
	return time.Now().Add(sessionTTL)
}

func pastTime() time.Time {
	return time.Now().Add(-sessionTTL - time.Second)
}

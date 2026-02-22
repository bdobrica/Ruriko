package nlp_test

import (
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

// ---------------------------------------------------------------------------
// DefaultCatalogue
// ---------------------------------------------------------------------------

func TestDefaultCatalogue_NotEmpty(t *testing.T) {
	cat := nlp.DefaultCatalogue()
	if len(cat) == 0 {
		t.Fatal("DefaultCatalogue returned an empty catalogue")
	}
}

func TestDefaultCatalogue_AllSpecsHaveRequiredFields(t *testing.T) {
	for _, spec := range nlp.DefaultCatalogue() {
		if spec.Action == "" {
			t.Errorf("CommandSpec with empty Action: %+v", spec)
		}
		if spec.Usage == "" {
			t.Errorf("CommandSpec %q has empty Usage", spec.Action)
		}
		if spec.Description == "" {
			t.Errorf("CommandSpec %q has empty Description", spec.Action)
		}
	}
}

func TestDefaultCatalogue_ContainsAllRegisteredActionKeys(t *testing.T) {
	// These are every action key registered in app.go — the catalogue must
	// contain exactly these so the LLM cannot produce an unknown action key.
	required := []string{
		"help",
		"version",
		"ping",
		"agents.list",
		"agents.show",
		"agents.create",
		"agents.stop",
		"agents.start",
		"agents.respawn",
		"agents.status",
		"agents.cancel",
		"agents.delete",
		"agents.matrix",
		"agents.disable",
		"secrets.list",
		"secrets.set",
		"secrets.info",
		"secrets.rotate",
		"secrets.delete",
		"secrets.bind",
		"secrets.unbind",
		"secrets.push",
		"audit.tail",
		"trace",
		"gosuto.show",
		"gosuto.versions",
		"gosuto.diff",
		"gosuto.set",
		"gosuto.rollback",
		"gosuto.push",
		"approvals.list",
		"approvals.show",
		"approve",
		"deny",
	}

	actions := make(map[string]bool, len(nlp.DefaultCatalogue()))
	for _, spec := range nlp.DefaultCatalogue() {
		actions[spec.Action] = true
	}

	for _, key := range required {
		if !actions[key] {
			t.Errorf("DefaultCatalogue missing required action key %q", key)
		}
	}
}

func TestDefaultCatalogue_IsSortedAlphabetically(t *testing.T) {
	cat := nlp.DefaultCatalogue()
	for i := 1; i < len(cat); i++ {
		if cat[i].Action < cat[i-1].Action {
			t.Errorf("catalogue not sorted: %q comes after %q", cat[i-1].Action, cat[i].Action)
		}
	}
}

func TestDefaultCatalogue_ReadOnlyAnnotations(t *testing.T) {
	// Spot-check: list/show/status/info commands must be read-only.
	readOnly := map[string]bool{
		"agents.list":     true,
		"agents.show":     true,
		"agents.status":   true,
		"secrets.list":    true,
		"secrets.info":    true,
		"gosuto.show":     true,
		"gosuto.versions": true,
		"gosuto.diff":     true,
		"approvals.list":  true,
		"approvals.show":  true,
		"audit.tail":      true,
		"trace":           true,
	}

	specs := make(map[string]nlp.CommandSpec, len(nlp.DefaultCatalogue()))
	for _, spec := range nlp.DefaultCatalogue() {
		specs[spec.Action] = spec
	}

	for action, wantRO := range readOnly {
		spec, ok := specs[action]
		if !ok {
			t.Errorf("action key %q not found in catalogue", action)
			continue
		}
		if spec.ReadOnly != wantRO {
			t.Errorf("action %q: ReadOnly = %v, want %v", action, spec.ReadOnly, wantRO)
		}
	}

	// Mutation commands must NOT be read-only.
	mutations := []string{
		"agents.create", "agents.stop", "agents.delete",
		"secrets.set", "secrets.delete",
		"gosuto.set", "gosuto.rollback", "gosuto.push",
		"approve", "deny",
	}
	for _, action := range mutations {
		spec, ok := specs[action]
		if !ok {
			t.Errorf("action key %q not found in catalogue", action)
			continue
		}
		if spec.ReadOnly {
			t.Errorf("mutation action %q must not be marked ReadOnly", action)
		}
	}
}

// ---------------------------------------------------------------------------
// Catalogue.String()
// ---------------------------------------------------------------------------

func TestCatalogueString_ContainsActionKeysAndUsage(t *testing.T) {
	cat := nlp.Catalogue{
		{Action: "test.action", Usage: "/ruriko test action", Description: "A test command.", ReadOnly: false},
		{Action: "test.read", Usage: "/ruriko test read", Description: "A read-only command.", ReadOnly: true},
	}
	s := cat.String()

	for _, want := range []string{"test.action", "/ruriko test action", "A test command."} {
		if !strings.Contains(s, want) {
			t.Errorf("catalogue string missing %q", want)
		}
	}
	if !strings.Contains(s, "[read-only]") {
		t.Error("catalogue string missing [read-only] annotation for ReadOnly=true spec")
	}
}

func TestCatalogueString_EmptyCatalogue(t *testing.T) {
	var cat nlp.Catalogue
	if got := cat.String(); got != "(no commands registered)" {
		t.Errorf("empty Catalogue.String() = %q; want %q", got, "(no commands registered)")
	}
}

func TestCatalogueString_NonReadOnlyHasNoAnnotation(t *testing.T) {
	cat := nlp.Catalogue{
		{Action: "agents.create", Usage: "/ruriko agents create", Description: "Create agent."},
	}
	s := cat.String()
	if strings.Contains(s, "[read-only]") {
		t.Error("non-read-only spec should not contain [read-only] annotation")
	}
}

// ---------------------------------------------------------------------------
// BuildSystemPrompt
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_ContainsAllCatalogueActionKeys(t *testing.T) {
	cat := nlp.DefaultCatalogue()
	prompt := nlp.BuildSystemPrompt(cat, nil, nil)

	for _, spec := range cat {
		if !strings.Contains(prompt, spec.Action) {
			t.Errorf("system prompt does not contain action key %q", spec.Action)
		}
	}
}

func TestBuildSystemPrompt_ForbidsInternalFlags(t *testing.T) {
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), nil, nil)

	// The prompt must explicitly call out the --_ prefix restriction so the
	// LLM knows not to produce internal flags even if a user tries to inject
	// them through a natural-language request.
	if !strings.Contains(prompt, "--_") {
		t.Error("system prompt does not mention the '--_' internal flag prefix restriction")
	}
}

func TestBuildSystemPrompt_ForbidsSecretValues(t *testing.T) {
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), nil, nil)

	// The prompt must contain at least one explicit prohibition covering
	// secret / credential leakage.
	forbidden := []string{"secret", "password", "credentials", "API key", "token"}
	for _, phrase := range forbidden {
		if strings.Contains(strings.ToLower(prompt), strings.ToLower(phrase)) {
			return // at least one phrase found — pass
		}
	}
	t.Errorf("system prompt does not warn against including secret values; checked phrases: %v", forbidden)
}

func TestBuildSystemPrompt_IncludesAgentContext(t *testing.T) {
	agents := []string{"saito — running", "kumo — stopped"}
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), agents, nil)

	for _, a := range agents {
		if !strings.Contains(prompt, a) {
			t.Errorf("system prompt does not include agent descriptor %q", a)
		}
	}
}

func TestBuildSystemPrompt_IncludesTemplateContext(t *testing.T) {
	templates := []string{"saito-agent", "kumo-agent", "browser-agent"}
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), nil, templates)

	for _, tmpl := range templates {
		if !strings.Contains(prompt, tmpl) {
			t.Errorf("system prompt does not include template %q", tmpl)
		}
	}
}

func TestBuildSystemPrompt_EmptyAgentsAndTemplates(t *testing.T) {
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), nil, nil)

	if !strings.Contains(prompt, "(none registered)") {
		t.Error("system prompt should show '(none registered)' when no agents are known")
	}
	if !strings.Contains(prompt, "(none available)") {
		t.Error("system prompt should show '(none available)' when no templates are available")
	}
}

func TestBuildSystemPrompt_InstructsNeverExecute(t *testing.T) {
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), nil, nil)

	// The prompt must explicitly state that the LLM never executes commands.
	if !strings.Contains(strings.ToLower(prompt), "never execute") &&
		!strings.Contains(strings.ToLower(prompt), "you never execute") {
		t.Error("system prompt must instruct the model to never execute commands")
	}
}

func TestBuildSystemPrompt_InstructsMutationConfirmation(t *testing.T) {
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), nil, nil)

	// The prompt must instruct that mutations require confirmation.
	lower := strings.ToLower(prompt)
	if !strings.Contains(lower, "confirm") && !strings.Contains(lower, "review") {
		t.Error("system prompt must require user confirmation for mutations")
	}
}

func TestBuildSystemPrompt_InstructsClarificationOnUnsure(t *testing.T) {
	prompt := nlp.BuildSystemPrompt(nlp.DefaultCatalogue(), nil, nil)

	lower := strings.ToLower(prompt)
	if !strings.Contains(lower, "unsure") && !strings.Contains(lower, "not sure") {
		t.Error("system prompt must instruct the model to ask for clarification when unsure")
	}
}

func TestBuildSystemPrompt_IsDeterministic(t *testing.T) {
	cat := nlp.DefaultCatalogue()
	agents := []string{"saito — running"}
	templates := []string{"saito-agent"}

	p1 := nlp.BuildSystemPrompt(cat, agents, templates)
	p2 := nlp.BuildSystemPrompt(cat, agents, templates)

	if p1 != p2 {
		t.Error("BuildSystemPrompt must return identical output given the same inputs")
	}
}

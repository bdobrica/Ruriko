package commands_test

// gosuto_instructions_test.go — tests for R14.4 Ruriko Instructions Authoring and Auditing
//
// Coverage:
//   - HandleGosutoShow: persona and instructions displayed as separate labelled sections.
//   - HandleGosutoShow: raw YAML fallback when YAML is non-parseable.
//   - HandleGosutoSetInstructions: updates only the instructions section; creates a new
//     versioned Gosuto entry; leaves persona unchanged.
//   - HandleGosutoSetPersona: updates only the persona section; creates a new versioned
//     Gosuto entry; leaves instructions unchanged.
//   - No-op detection: set-instructions with identical content does not create a new version.
//   - Audit trail: instructions update emits a gosuto.set-instructions audit log entry.
//   - Provisioned agent: a template with instructions produces a Gosuto version that
//     contains those instructions.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bdobrica/Ruriko/common/spec/gosuto"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
	"gopkg.in/yaml.v3"
)

// ────────────────────────────────────────────────────────────────────────────
// shared test YAML stubs

// validGosutoWithPersonaAndInstructions is a complete, valid Gosuto v1 config
// that includes both a persona section and an instructions section.
const validGosutoWithPersonaAndInstructions = `apiVersion: gosuto/v1
metadata:
  name: testbot
trust:
  allowedRooms:
    - "!admin:example.com"
  allowedSenders:
    - "*"
persona:
  systemPrompt: "You are Testbot, a reliable assistant."
  llmProvider: openai
  model: gpt-4o
  temperature: 0.5
instructions:
  role: "You handle test scenarios reliably and thoroughly."
  workflow:
    - trigger: "message received"
      action: "Process the message, return a structured result."
    - trigger: "after processing"
      action: "Post the result to the admin room."
  context:
    user: "The user is the sole approver."
    peers:
      - name: "peer-alpha"
        role: "Provides data for analysis."
`

// updatedInstructionsYAML is YAML for just the instructions section.
const updatedInstructionsYAML = `role: "You handle test scenarios with updated logic."
workflow:
  - trigger: "updated trigger"
    action: "Execute the updated action sequence."
context:
  user: "Updated user context — still the sole approver."
  peers: []
`

// updatedPersonaYAML is YAML for just the persona section.
const updatedPersonaYAML = `systemPrompt: "You are Testbot, now with an updated persona."
llmProvider: openai
model: gpt-4o-mini
temperature: 0.1
`

// gosutoTemplateWithInstructions is a template YAML that includes both persona
// and instructions. Used by the provisioning test.
const gosutoTemplateWithInstructions = `apiVersion: gosuto/v1
metadata:
  name: "{{.AgentName}}"
  template: instr-template
trust:
  allowedRooms:
    - "!admin:example.com"
  allowedSenders:
    - "*"
persona:
  systemPrompt: "You are a provisioned test agent."
  llmProvider: openai
  model: gpt-4o-mini
instructions:
  role: "You perform provisioning integration tests."
  workflow:
    - trigger: "cron.tick event"
      action: "Run the scheduled check and report findings."
  context:
    user: "The user is the sole approver."
    peers: []
`

// ────────────────────────────────────────────────────────────────────────────
// helpers

// seedAgentWithGosuto creates an agent and stores a Gosuto version in the store.
// The YAML is canonicalised through unmarshal→marshal before storage so that
// the stored hash is consistent with what patchCurrentGosuto would compute.
// This is required for no-op detection tests to work correctly.
func seedAgentWithGosuto(t *testing.T, s *appstore.Store, agentID, rawYAML string) *appstore.GosutoVersion {
	t.Helper()
	ctx := context.Background()

	if err := s.CreateAgent(ctx, &appstore.Agent{
		ID:          agentID,
		DisplayName: agentID,
		Status:      "running",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateAgent %q: %v", agentID, err)
	}

	// Canonicalise: unmarshal into gosuto.Config then re-marshal. patchCurrentGosuto
	// does the same transformation, so the resulting hash will match on no-op checks.
	// Fall back to raw YAML if it cannot be parsed as a gosuto.Config.
	storeBlob := rawYAML
	var cfg gosuto.Config
	if yaml.Unmarshal([]byte(rawYAML), &cfg) == nil {
		if canonical, err := yaml.Marshal(&cfg); err == nil {
			storeBlob = string(canonical)
		}
	}

	nextVer, err := s.NextGosutoVersion(ctx, agentID)
	if err != nil {
		t.Fatalf("NextGosutoVersion %q: %v", agentID, err)
	}
	if err := s.CreateGosutoVersion(ctx, &appstore.GosutoVersion{
		AgentID:       agentID,
		Version:       nextVer,
		Hash:          hashString(storeBlob),
		YAMLBlob:      storeBlob,
		CreatedByMXID: "@admin:example.com",
	}); err != nil {
		t.Fatalf("CreateGosutoVersion %q: %v", agentID, err)
	}
	// Re-fetch so CreatedAt is populated.
	fetched, err := s.GetLatestGosutoVersion(ctx, agentID)
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion %q: %v", agentID, err)
	}
	return fetched
}

// hashString returns the SHA-256 hex digest of s.
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum)
}

// b64 base64-encodes a string (StdEncoding).
func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// ────────────────────────────────────────────────────────────────────────────
// HandleGosutoShow

// TestGosutoShow_DisplaysPersonaAndInstructionsSeparately verifies that
// HandleGosutoShow renders persona and instructions as clearly labelled
// sections rather than only outputting the raw YAML blob.
func TestGosutoShow_DisplaysPersonaAndInstructionsSeparately(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	seedAgentWithGosuto(t, s, "showbot", validGosutoWithPersonaAndInstructions)

	cmd := parseCmd(t, "/ruriko gosuto show showbot")
	resp, err := h.HandleGosutoShow(ctx, cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleGosutoShow: %v", err)
	}

	for _, want := range []string{
		"**Persona**",
		"**Instructions**",
		"System prompt:",
		"Role:",
		"Workflow:",
		"Peers:",
		"gpt-4o",
		"You handle test scenarios",
		"peer-alpha",
	} {
		if !strings.Contains(resp, want) {
			t.Errorf("HandleGosutoShow response missing %q\nGot:\n%s", want, resp)
		}
	}
}

// TestGosutoShow_FallsBackToRawYAMLForUnparseable verifies that the show handler
// does not crash and still returns useful output when the stored YAML cannot
// be parsed into a gosuto.Config (e.g. a legacy format or malformed blob).
func TestGosutoShow_FallsBackToRawYAML(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	// Store malformed YAML directly (bypassing seedAgentWithGosuto which
	// canonicalises — malformed YAML is the only reliable way to trigger the
	// fallback path in formatGosutoShow).
	if err := s.CreateAgent(ctx, &appstore.Agent{
		ID:          "brokenbot",
		DisplayName: "brokenbot",
		Status:      "running",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	malformed := "key: [\n  - unclosed bracket\n  and more\n"
	sum := sha256.Sum256([]byte(malformed))
	if err := s.CreateGosutoVersion(ctx, &appstore.GosutoVersion{
		AgentID:       "brokenbot",
		Version:       1,
		Hash:          fmt.Sprintf("%x", sum),
		YAMLBlob:      malformed,
		CreatedByMXID: "@admin:example.com",
	}); err != nil {
		t.Fatalf("CreateGosutoVersion: %v", err)
	}

	cmd := parseCmd(t, "/ruriko gosuto show brokenbot")
	resp, err := h.HandleGosutoShow(ctx, cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleGosutoShow (broken YAML): %v", err)
	}
	// Must still show the agent name.
	if !strings.Contains(resp, "brokenbot") {
		t.Errorf("fallback response missing agent name; got:\n%s", resp)
	}
	// Must show the raw YAML code block (fallback path).
	if !strings.Contains(resp, "```yaml") {
		t.Errorf("fallback response missing raw YAML block; got:\n%s", resp)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// HandleGosutoSetInstructions

// TestGosutoSetInstructions_CreatesNewVersion verifies that set-instructions
// stores a new Gosuto version with the updated instructions section and leaves
// the persona unchanged.
func TestGosutoSetInstructions_CreatesNewVersion(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	seedAgentWithGosuto(t, s, "instrbot", validGosutoWithPersonaAndInstructions)

	cmd := parseCmd(t, "/ruriko gosuto set-instructions instrbot --content "+b64(updatedInstructionsYAML))
	resp, err := h.HandleGosutoSetInstructions(ctx, cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleGosutoSetInstructions: %v", err)
	}
	if !strings.Contains(resp, "v2") {
		t.Errorf("expected v2 in response, got: %q", resp)
	}

	// Verify new version stored.
	gv, err := s.GetLatestGosutoVersion(ctx, "instrbot")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if gv.Version != 2 {
		t.Errorf("expected version 2 after update, got %d", gv.Version)
	}

	// Verify instructions changed.
	var cfg gosuto.Config
	if err := yaml.Unmarshal([]byte(gv.YAMLBlob), &cfg); err != nil {
		t.Fatalf("unmarshal patched YAML: %v", err)
	}
	if !strings.Contains(cfg.Instructions.Role, "updated logic") {
		t.Errorf("instructions.role not updated; got: %q", cfg.Instructions.Role)
	}
	if len(cfg.Instructions.Workflow) != 1 || !strings.Contains(cfg.Instructions.Workflow[0].Trigger, "updated trigger") {
		t.Errorf("instructions.workflow not updated; got: %+v", cfg.Instructions.Workflow)
	}

	// Verify persona is unchanged.
	if !strings.Contains(cfg.Persona.SystemPrompt, "Testbot, a reliable assistant") {
		t.Errorf("persona.systemPrompt was unexpectedly changed; got: %q", cfg.Persona.SystemPrompt)
	}
	if cfg.Persona.Model != "gpt-4o" {
		t.Errorf("persona.model was unexpectedly changed; got: %q", cfg.Persona.Model)
	}
}

// TestGosutoSetInstructions_NoOpWhenContentUnchanged verifies that calling
// set-instructions with instructions identical to the current version does
// not create a new Gosuto version.
func TestGosutoSetInstructions_NoOpWhenContentUnchanged(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	// Store a version, then read back what is actually stored (the canonical
	// re-marshaled form) and extract instructions from that.  Using the
	// stored blob rather than the original constant ensures the hash of the
	// re-marshaled output matches current.Hash exactly (no-op check).
	seedAgentWithGosuto(t, s, "noobot", validGosutoWithPersonaAndInstructions)

	gv, err := s.GetLatestGosutoVersion(ctx, "noobot")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion (seed): %v", err)
	}
	var cfg gosuto.Config
	if err := yaml.Unmarshal([]byte(gv.YAMLBlob), &cfg); err != nil {
		t.Fatalf("unmarshal stored blob: %v", err)
	}
	sameInstrYAML, err := yaml.Marshal(cfg.Instructions)
	if err != nil {
		t.Fatalf("marshal instructions: %v", err)
	}

	cmd := parseCmd(t, "/ruriko gosuto set-instructions noobot --content "+b64(string(sameInstrYAML)))
	resp, err := h.HandleGosutoSetInstructions(ctx, cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleGosutoSetInstructions (no-op): %v", err)
	}

	// Response must mention "unchanged".
	if !strings.Contains(strings.ToLower(resp), "unchanged") {
		t.Errorf("expected 'unchanged' in no-op response; got: %q", resp)
	}

	// Still only one version.
	versions, err := s.ListGosutoVersions(ctx, "noobot")
	if err != nil {
		t.Fatalf("ListGosutoVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Errorf("expected 1 version after no-op, got %d", len(versions))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// HandleGosutoSetPersona

// TestGosutoSetPersona_CreatesNewVersion verifies that set-persona stores a
// new Gosuto version with the updated persona section and leaves the
// instructions unchanged.
func TestGosutoSetPersona_CreatesNewVersion(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	seedAgentWithGosuto(t, s, "personabot", validGosutoWithPersonaAndInstructions)

	cmd := parseCmd(t, "/ruriko gosuto set-persona personabot --content "+b64(updatedPersonaYAML))
	resp, err := h.HandleGosutoSetPersona(ctx, cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleGosutoSetPersona: %v", err)
	}
	if !strings.Contains(resp, "v2") {
		t.Errorf("expected v2 in response, got: %q", resp)
	}

	// Verify new version stored.
	gv, err := s.GetLatestGosutoVersion(ctx, "personabot")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if gv.Version != 2 {
		t.Errorf("expected version 2 after persona update, got %d", gv.Version)
	}

	// Verify persona changed.
	var cfg gosuto.Config
	if err := yaml.Unmarshal([]byte(gv.YAMLBlob), &cfg); err != nil {
		t.Fatalf("unmarshal patched YAML: %v", err)
	}
	if !strings.Contains(cfg.Persona.SystemPrompt, "updated persona") {
		t.Errorf("persona.systemPrompt not updated; got: %q", cfg.Persona.SystemPrompt)
	}
	if cfg.Persona.Model != "gpt-4o-mini" {
		t.Errorf("persona.model not updated; got: %q", cfg.Persona.Model)
	}

	// Verify instructions unchanged.
	if !strings.Contains(cfg.Instructions.Role, "You handle test scenarios") {
		t.Errorf("instructions.role was unexpectedly changed; got: %q", cfg.Instructions.Role)
	}
	if len(cfg.Instructions.Context.Peers) != 1 || cfg.Instructions.Context.Peers[0].Name != "peer-alpha" {
		t.Errorf("instructions.context.peers was unexpectedly changed; got: %+v", cfg.Instructions.Context.Peers)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Audit trail

// TestGosutoSetInstructions_IsAudited verifies that a successful set-instructions
// operation writes a gosuto.set-instructions audit entry with result="success".
func TestGosutoSetInstructions_IsAudited(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	seedAgentWithGosuto(t, s, "auditbot", validGosutoWithPersonaAndInstructions)

	cmd := parseCmd(t, "/ruriko gosuto set-instructions auditbot --content "+b64(updatedInstructionsYAML))
	if _, err := h.HandleGosutoSetInstructions(ctx, cmd, fakeEvent("@admin:example.com")); err != nil {
		t.Fatalf("HandleGosutoSetInstructions: %v", err)
	}

	// Fetch audit log and verify the entry.
	entries, err := s.GetAuditLog(ctx, 20)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "gosuto.set-instructions" && e.Result == "success" {
			found = true
			if !e.Target.Valid || e.Target.String != "auditbot" {
				t.Errorf("audit target: got %q, want %q", e.Target.String, "auditbot")
			}
			break
		}
	}
	if !found {
		t.Errorf("no gosuto.set-instructions success audit entry found")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Provisioning — default instructions from template

// TestProvisionedAgentHasDefaultInstructions verifies that when an agent is
// provisioned from a template that includes an instructions section, the
// stored Gosuto version contains those instructions, ensuring that the
// provisioning pipeline preserves instructions from templates as-is.
func TestProvisionedAgentHasDefaultInstructions(t *testing.T) {
	// Use an in-memory FS carrying our instructions-bearing template.
	testFS := fstest.MapFS{
		"instr-template/gosuto.yaml": &fstest.MapFile{Data: []byte(gosutoTemplateWithInstructions)},
	}
	reg := templates.NewRegistry(testFS)

	// Render the template for a test agent — this mirrors what the provisioning
	// pipeline does in runProvisioningPipeline step 3.
	vars := templates.TemplateVars{
		AgentName:    "provbot",
		DisplayName:  "provbot",
		AdminRoom:    "!admin:example.com",
		OperatorMXID: "@admin:example.com",
	}
	rendered, err := reg.Render("instr-template", vars)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// The rendered YAML must be a valid Gosuto config.
	cfg, err := gosuto.Parse(rendered)
	if err != nil {
		t.Fatalf("gosuto.Parse(rendered): %v", err)
	}

	// Verify instructions are present and correct.
	if cfg.Instructions.Role == "" {
		t.Error("provisioned Gosuto config has empty instructions.role")
	}
	if !strings.Contains(cfg.Instructions.Role, "provisioning integration tests") {
		t.Errorf("instructions.role unexpected content: %q", cfg.Instructions.Role)
	}
	if len(cfg.Instructions.Workflow) == 0 {
		t.Error("provisioned Gosuto config has empty instructions.workflow")
	}
	if cfg.Instructions.Context.User == "" {
		t.Error("provisioned Gosuto config has empty instructions.context.user")
	}
}

// TestGosutoDiff_AnnotatesSectionChanges verifies that the diff output includes
// section-change annotations ("Changed sections") when instructions differ
// between two versions.
func TestGosutoDiff_AnnotatesSectionChanges(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	// Seed v1 with the base config.
	seedAgentWithGosuto(t, s, "diffbot", validGosutoWithPersonaAndInstructions)

	// Create v2 by updating instructions.
	cmd := parseCmd(t, "/ruriko gosuto set-instructions diffbot --content "+b64(updatedInstructionsYAML))
	if _, err := h.HandleGosutoSetInstructions(ctx, cmd, fakeEvent("@admin:example.com")); err != nil {
		t.Fatalf("HandleGosutoSetInstructions: %v", err)
	}

	// Diff v1 → v2.
	diffCmd := parseCmd(t, "/ruriko gosuto diff diffbot --from 1 --to 2")
	resp, err := h.HandleGosutoDiff(ctx, diffCmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleGosutoDiff: %v", err)
	}

	// Output should note that instructions changed.
	if !strings.Contains(resp, "instructions") {
		t.Errorf("diff output does not mention 'instructions'; got:\n%s", resp)
	}
	// Persona should be noted as unchanged in the section notes.
	if strings.Contains(resp, "**persona**") {
		t.Errorf("diff unexpectedly reports persona as changed; got:\n%s", resp)
	}
}

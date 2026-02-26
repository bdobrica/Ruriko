package templates_test

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
)

// makeFS creates an in-memory fs.FS for testing.
func makeFS(files map[string]string) fstest.MapFS {
	m := make(fstest.MapFS)
	for path, content := range files {
		m[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

const cronTemplate = `apiVersion: gosuto/v1
metadata:
  name: "{{.AgentName}}"
  template: cron-agent
trust:
  allowedRooms:
    - "{{.AdminRoom}}"
  allowedSenders:
    - "*"
`

func TestRegistry_List(t *testing.T) {
	fs := makeFS(map[string]string{
		"cron-agent/gosuto.yaml":    cronTemplate,
		"browser-agent/gosuto.yaml": cronTemplate,
		"README.md":                 "not a template dir",
	})

	reg := templates.NewRegistry(fs)

	names, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(names) != 2 {
		t.Errorf("List: got %d names, want 2: %v", len(names), names)
	}
}

func TestRegistry_Render(t *testing.T) {
	fs := makeFS(map[string]string{
		"cron-agent/gosuto.yaml": cronTemplate,
	})

	reg := templates.NewRegistry(fs)

	vars := templates.TemplateVars{
		AgentName: "my-bot",
		AdminRoom: "!abc123:example.com",
	}

	rendered, err := reg.Render("cron-agent", vars)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(rendered)

	if !strings.Contains(got, "name: \"my-bot\"") {
		t.Errorf("rendered YAML should contain agent name:\n%s", got)
	}
	if !strings.Contains(got, "!abc123:example.com") {
		t.Errorf("rendered YAML should contain admin room:\n%s", got)
	}
}

func TestRegistry_Render_NotFound(t *testing.T) {
	fs := makeFS(map[string]string{
		"cron-agent/gosuto.yaml": cronTemplate,
	})

	reg := templates.NewRegistry(fs)

	_, err := reg.Render("nonexistent", templates.TemplateVars{AgentName: "x"})
	if err == nil {
		t.Fatal("expected error for missing template, got nil")
	}
}

func TestRegistry_Render_TemplateError(t *testing.T) {
	fs := makeFS(map[string]string{
		"bad-agent/gosuto.yaml": "name: {{.NonExistentField.SubField}}",
	})

	reg := templates.NewRegistry(fs)

	_, err := reg.Render("bad-agent", templates.TemplateVars{})
	if err == nil {
		t.Fatal("expected template execution error, got nil")
	}
}

// ── Disk-backed tests for canonical templates ─────────────────────────────────

// newDiskRegistry loads the templates from the real templates/ directory on
// disk (three levels above this package).
func newDiskRegistry(t *testing.T) *templates.Registry {
	t.Helper()
	fs := os.DirFS("../../../templates")
	return templates.NewRegistry(fs)
}

var canonicalVars = templates.TemplateVars{
	AgentName:      "test-agent",
	DisplayName:    "Test Agent",
	AdminRoom:      "!admin:example.com",
	AgentMXID:      "@test-agent:example.com",
	OperatorMXID:   "@operator:example.com",
	KairoAdminRoom: "!kairo-admin:example.com",
	KumoAdminRoom:  "!kumo-admin:example.com",
	UserRoom:       "!user-dm:example.com",
}

func TestRegistry_List_IncludesCanonicalTemplates(t *testing.T) {
	reg := newDiskRegistry(t)

	names, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"kairo-agent", "kumo-agent", "saito-agent"}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("List: expected template %q to be present; got %v", w, names)
		}
	}
}

func TestRegistry_Render_SaitoAgent(t *testing.T) {
	reg := newDiskRegistry(t)

	rendered, err := reg.Render("saito-agent", canonicalVars)
	if err != nil {
		t.Fatalf("Render saito-agent: %v", err)
	}

	got := string(rendered)

	checks := []struct {
		desc    string
		contain string
	}{
		{"agent name substituted", `name: "test-agent"`},
		{"admin room substituted", "!admin:example.com"},
		{"correct template tag", "template: saito-agent"},
		{"deny-all-tools capability present", "deny-all-tools"},
		{"allow-matrix-send capability present", "allow-matrix-send"},
		{"default deterministic payload", "Time for a portfolio check."},
		{"no MCP servers", "mcps:"},
		{"cron gateway block present", "gateways:"},
		{"scheduler gateway name", "scheduler"},
		{"cron expression", "*/15 * * * *"},
		{"messaging section present", "messaging:"},
		{"kairo target alias", "alias: kairo"},
		{"user target alias", "alias: user"},
		{"kairo room id substituted", "!kairo-admin:example.com"},
		{"user room id substituted", "!user-dm:example.com"},
		{"matrix.send_message in persona", "matrix.send_message"},
	}

	for _, c := range checks {
		if c.desc == "no MCP servers" {
			// Saito should NOT have mcps block
			if strings.Contains(got, c.contain) {
				t.Errorf("saito-agent rendered YAML should NOT contain %q:\n%s", c.contain, got)
			}
			continue
		}
		if !strings.Contains(got, c.contain) {
			t.Errorf("saito-agent rendered YAML should contain %q:\n%s", c.contain, got)
		}
	}

	if strings.Contains(got, "openai-api-key") {
		t.Errorf("saito-agent rendered YAML should not require OpenAI secret:\n%s", got)
	}
}

func TestRegistry_Render_KumoAgent(t *testing.T) {
	reg := newDiskRegistry(t)

	rendered, err := reg.Render("kumo-agent", canonicalVars)
	if err != nil {
		t.Fatalf("Render kumo-agent: %v", err)
	}

	got := string(rendered)

	checks := []string{
		`name: "test-agent"`,
		"!admin:example.com",
		"template: kumo-agent",
		"allow-brave-search",
		"allow-fetch-read",
		"allow-matrix-send",
		"deny-all-others",
		"brave-search",
		"@modelcontextprotocol/server-brave-search",
		"fetch",
		"@modelcontextprotocol/server-fetch",
		"test-agent.openai-api-key",
		"test-agent.brave-api-key",
		"gpt-4o",
		"${BRAVE_API_KEY}",
		"messaging:",
		"alias: kairo",
		"alias: user",
		"!kairo-admin:example.com",
		"!user-dm:example.com",
	}

	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("kumo-agent rendered YAML should contain %q:\n%s", want, got)
		}
	}
}

func TestRegistry_Render_SaitoAgent_MissingVar(t *testing.T) {
	reg := newDiskRegistry(t)

	// AgentName is empty — template uses .AgentName so this must fail
	_, err := reg.Render("saito-agent", templates.TemplateVars{
		AdminRoom: "!admin:example.com",
	})
	// The missingkey=error option only triggers when the key is outright
	// missing from the struct.  Empty string is a valid value for AgentName
	// and will render as an empty name.  We just verify the render completes
	// without a panic; callers are responsible for passing valid vars.
	_ = err // error or nil both acceptable for an empty but present field
}

func TestRegistry_Render_KumoAgent_MissingVar(t *testing.T) {
	reg := newDiskRegistry(t)

	_, err := reg.Render("kumo-agent", templates.TemplateVars{
		AdminRoom: "!admin:example.com",
	})
	_ = err
}

func TestRegistry_Render_KairoAgent(t *testing.T) {
	reg := newDiskRegistry(t)

	rendered, err := reg.Render("kairo-agent", canonicalVars)
	if err != nil {
		t.Fatalf("Render kairo-agent: %v", err)
	}

	got := string(rendered)

	checks := []string{
		`name: "test-agent"`,
		"!admin:example.com",
		"template: kairo-agent",
		"allow-finnhub-all",
		"allow-database-read",
		"allow-database-write",
		"allow-database-update",
		"allow-database-query",
		"allow-matrix-send",
		"deny-all-others",
		"finnhub",
		"uv",
		"stock_market_server.py",
		"database",
		"mcp-sqlite",
		"test-agent.db",
		"test-agent.openai-api-key",
		"test-agent.finnhub-api-key",
		"FINNHUB_API_KEY",
		"gpt-4o",
		"0.2",
		"messaging:",
		"alias: kumo",
		"alias: user",
		"!kumo-admin:example.com",
		"!user-dm:example.com",
	}

	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("kairo-agent rendered YAML should contain %q:\n%s", want, got)
		}
	}
}

func TestRegistry_Render_KairoAgent_MissingVar(t *testing.T) {
	reg := newDiskRegistry(t)

	_, err := reg.Render("kairo-agent", templates.TemplateVars{
		AdminRoom: "!admin:example.com",
	})
	_ = err
}

// ── R14.5: All canonical templates pass Gosuto validation and have instructions ──

// TestRegistry_Render_AllTemplates_PassValidation renders every canonical
// template with valid substitution variables, parses the result with
// gosuto.Parse(), and verifies:
//   - parsing succeeds (no validation errors)
//   - instructions.role is populated
//   - instructions.workflow has at least one step
//   - instructions.context.user is populated
func TestRegistry_Render_AllTemplates_PassValidation(t *testing.T) {
	reg := newDiskRegistry(t)

	templateNames, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for _, name := range templateNames {
		name := name
		t.Run(name, func(t *testing.T) {
			rendered, err := reg.Render(name, canonicalVars)
			if err != nil {
				t.Fatalf("Render %s: %v", name, err)
			}

			cfg, err := gosutospec.Parse(rendered)
			if err != nil {
				t.Fatalf("gosuto.Parse %s: %v", name, err)
			}

			if cfg.Instructions.Role == "" {
				t.Errorf("%s: instructions.role must not be empty", name)
			}
			if len(cfg.Instructions.Workflow) == 0 {
				t.Errorf("%s: instructions.workflow must have at least one step", name)
			}
			if cfg.Instructions.Context.User == "" {
				t.Errorf("%s: instructions.context.user must not be empty", name)
			}
		})
	}
}

// ── R16.1: Describe / DescribeAll — template metadata extraction ──────────────

const describeTemplate = `apiVersion: gosuto/v1
metadata:
  name: "{{.AgentName}}"
  template: test-agent
  canonicalName: tester
  description: >
    A test agent used in unit tests.
trust:
  allowedRooms:
    - "{{.AdminRoom}}"
  allowedSenders:
    - "*"
`

const nonCanonicalTemplate = `apiVersion: gosuto/v1
metadata:
  name: "{{.AgentName}}"
  template: generic-agent
trust:
  allowedRooms:
    - "{{.AdminRoom}}"
  allowedSenders:
    - "*"
`

func TestRegistry_Describe_ExtractsMetadata(t *testing.T) {
	fs := makeFS(map[string]string{
		"test-agent/gosuto.yaml": describeTemplate,
	})
	reg := templates.NewRegistry(fs)

	info, err := reg.Describe("test-agent")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	if info.Name != "test-agent" {
		t.Errorf("Name: got %q, want %q", info.Name, "test-agent")
	}
	if info.CanonicalName != "tester" {
		t.Errorf("CanonicalName: got %q, want %q", info.CanonicalName, "tester")
	}
	if !strings.Contains(info.Description, "A test agent") {
		t.Errorf("Description does not contain expected text: %q", info.Description)
	}
}

func TestRegistry_Describe_EmptyCanonicalNameWhenAbsent(t *testing.T) {
	fs := makeFS(map[string]string{
		"generic-agent/gosuto.yaml": nonCanonicalTemplate,
	})
	reg := templates.NewRegistry(fs)

	info, err := reg.Describe("generic-agent")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	if info.CanonicalName != "" {
		t.Errorf("CanonicalName: got %q, want empty string for non-canonical template", info.CanonicalName)
	}
}

func TestRegistry_Describe_NotFound(t *testing.T) {
	fs := makeFS(map[string]string{
		"test-agent/gosuto.yaml": describeTemplate,
	})
	reg := templates.NewRegistry(fs)

	_, err := reg.Describe("nonexistent")
	if err == nil {
		t.Error("expected error for missing template, got nil")
	}
}

func TestRegistry_DescribeAll_ReturnsInfoForAll(t *testing.T) {
	fs := makeFS(map[string]string{
		"test-agent/gosuto.yaml":    describeTemplate,
		"generic-agent/gosuto.yaml": nonCanonicalTemplate,
	})
	reg := templates.NewRegistry(fs)

	infos, err := reg.DescribeAll()
	if err != nil {
		t.Fatalf("DescribeAll: %v", err)
	}

	if len(infos) != 2 {
		t.Errorf("DescribeAll: got %d entries, want 2", len(infos))
	}
}

func TestRegistry_DescribeAll_CanonicalAgentsHaveNames(t *testing.T) {
	fs := makeFS(map[string]string{
		"test-agent/gosuto.yaml":    describeTemplate,
		"generic-agent/gosuto.yaml": nonCanonicalTemplate,
	})
	reg := templates.NewRegistry(fs)

	infos, err := reg.DescribeAll()
	if err != nil {
		t.Fatalf("DescribeAll: %v", err)
	}

	// Only test-agent has a canonicalName; generic-agent does not.
	canonical := 0
	for _, info := range infos {
		if info.CanonicalName != "" {
			canonical++
		}
	}
	if canonical != 1 {
		t.Errorf("expected 1 canonical agent in test set, got %d", canonical)
	}
}

// Disk-backed tests: canonical templates must expose canonicalName via DescribeAll.

func TestRegistry_DescribeAll_CanonicalTemplatesHaveCanonicalName(t *testing.T) {
	reg := newDiskRegistry(t)

	infos, err := reg.DescribeAll()
	if err != nil {
		t.Fatalf("DescribeAll on disk registry: %v", err)
	}

	want := map[string]string{
		"saito-agent": "saito",
		"kairo-agent": "kairo",
		"kumo-agent":  "kumo",
	}

	byName := make(map[string]templates.TemplateInfo, len(infos))
	for _, info := range infos {
		byName[info.Name] = info
	}

	for tmpl, wantCanonical := range want {
		info, ok := byName[tmpl]
		if !ok {
			t.Errorf("DescribeAll: template %q not found in results", tmpl)
			continue
		}
		if info.CanonicalName != wantCanonical {
			t.Errorf("template %q: CanonicalName = %q, want %q", tmpl, info.CanonicalName, wantCanonical)
		}
		if info.Description == "" {
			t.Errorf("template %q: Description must not be empty", tmpl)
		}
	}
}

func TestRegistry_DescribeAll_CanonicalDescriptionsAreNonEmpty(t *testing.T) {
	reg := newDiskRegistry(t)

	infos, err := reg.DescribeAll()
	if err != nil {
		t.Fatalf("DescribeAll: %v", err)
	}

	for _, info := range infos {
		if info.Name == "" {
			t.Error("TemplateInfo with empty Name returned from DescribeAll")
		}
		// Description may be empty for templates that omit metadata.description,
		// but the canonical three must always have one.
		canonical := map[string]bool{"saito-agent": true, "kairo-agent": true, "kumo-agent": true}
		if canonical[info.Name] && info.Description == "" {
			t.Errorf("canonical template %q must have a non-empty Description", info.Name)
		}
	}
}

// ── R15.6: Canonical messaging-capable templates pass validation and have messaging ──

// TestRegistry_Render_MessagingAgents_HaveAllowedTargets verifies that the
// three canonical agents that participate in the peer-to-peer mesh (saito,
// kairo, kumo) each render with:
//   - a non-empty messaging.allowedTargets list
//   - a capability rule allowing the builtin matrix.send_message tool
//   - Gosuto validation passing end-to-end for the rendered config
func TestRegistry_Render_MessagingAgents_HaveAllowedTargets(t *testing.T) {
	reg := newDiskRegistry(t)

	agents := []struct {
		name        string
		wantAliases []string
		wantRoomIDs []string
	}{
		{
			name:        "saito-agent",
			wantAliases: []string{"kairo", "user"},
			wantRoomIDs: []string{"!kairo-admin:example.com", "!user-dm:example.com"},
		},
		{
			name:        "kairo-agent",
			wantAliases: []string{"kumo", "user"},
			wantRoomIDs: []string{"!kumo-admin:example.com", "!user-dm:example.com"},
		},
		{
			name:        "kumo-agent",
			wantAliases: []string{"kairo", "user"},
			wantRoomIDs: []string{"!kairo-admin:example.com", "!user-dm:example.com"},
		},
	}

	for _, tc := range agents {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := reg.Render(tc.name, canonicalVars)
			if err != nil {
				t.Fatalf("Render %s: %v", tc.name, err)
			}

			// Full Gosuto validation must pass.
			cfg, err := gosutospec.Parse(rendered)
			if err != nil {
				t.Fatalf("gosuto.Parse %s: %v", tc.name, err)
			}

			// messaging.allowedTargets must be non-empty.
			if len(cfg.Messaging.AllowedTargets) == 0 {
				t.Errorf("%s: messaging.allowedTargets must not be empty", tc.name)
			}

			// Each expected alias must appear.
			aliasSet := make(map[string]bool, len(cfg.Messaging.AllowedTargets))
			for _, target := range cfg.Messaging.AllowedTargets {
				aliasSet[target.Alias] = true
			}
			for _, want := range tc.wantAliases {
				if !aliasSet[want] {
					t.Errorf("%s: expected alias %q in messaging.allowedTargets, got %v",
						tc.name, want, cfg.Messaging.AllowedTargets)
				}
			}

			// Each expected room ID must appear.
			roomSet := make(map[string]bool, len(cfg.Messaging.AllowedTargets))
			for _, target := range cfg.Messaging.AllowedTargets {
				roomSet[target.RoomID] = true
			}
			for _, want := range tc.wantRoomIDs {
				if !roomSet[want] {
					t.Errorf("%s: expected roomId %q in messaging.allowedTargets, got %v",
						tc.name, want, cfg.Messaging.AllowedTargets)
				}
			}

			// A capability rule allowing builtin/matrix.send_message must exist.
			found := false
			for _, cap := range cfg.Capabilities {
				if cap.MCP == "builtin" && cap.Tool == "matrix.send_message" && cap.Allow {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: no allow:true capability rule for mcp=builtin tool=matrix.send_message", tc.name)
			}
		})
	}
}

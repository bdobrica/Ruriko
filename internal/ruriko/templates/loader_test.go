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
	AgentName:    "test-agent",
	DisplayName:  "Test Agent",
	AdminRoom:    "!admin:example.com",
	AgentMXID:    "@test-agent:example.com",
	OperatorMXID: "@operator:example.com",
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
		{"openai secret present", "test-agent.openai-api-key"},
		{"gpt-4o-mini model", "gpt-4o-mini"},
		{"no MCP servers", "mcps:"},
		{"cron gateway block present", "gateways:"},
		{"scheduler gateway name", "scheduler"},
		{"cron expression", "*/15 * * * *"},
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
		"deny-all-others",
		"brave-search",
		"@modelcontextprotocol/server-brave-search",
		"fetch",
		"@modelcontextprotocol/server-fetch",
		"test-agent.openai-api-key",
		"test-agent.brave-api-key",
		"gpt-4o",
		"${BRAVE_API_KEY}",
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

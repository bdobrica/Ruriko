package templates_test

import (
	"testing"
	"testing/fstest"

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

	if !contains(got, "name: \"my-bot\"") {
		t.Errorf("rendered YAML should contain agent name:\n%s", got)
	}
	if !contains(got, "!abc123:example.com") {
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

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

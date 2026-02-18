package gosuto_test

import (
	"testing"

	"github.com/bdobrica/Ruriko/common/spec/gosuto"
)

const minimalValid = `
apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms:
    - "!room:example.com"
  allowedSenders:
    - "@alice:example.com"
`

const fullValid = `
apiVersion: gosuto/v1
metadata:
  name: my-agent
  template: cron-agent
  description: Test agent

trust:
  allowedRooms:
    - "!room:example.com"
  allowedSenders:
    - "*"
  requireE2EE: false
  adminRoom: "!room:example.com"

limits:
  maxRequestsPerMinute: 10
  maxTokensPerRequest: 4096
  maxConcurrentRequests: 1
  maxMonthlyCostUSD: 5.0

capabilities:
  - name: allow-search
    mcp: brave-search
    tool: "*"
    allow: true
  - name: deny-rest
    mcp: "*"
    tool: "*"
    allow: false

approvals:
  enabled: false
  ttlSeconds: 300

mcps:
  - name: brave-search
    command: npx
    args:
      - "-y"
      - "@modelcontextprotocol/server-brave-search"
    autoRestart: true

secrets:
  - name: my-agent.api-key
    envVar: API_KEY
    required: true

persona:
  systemPrompt: "You are a helpful agent."
  llmProvider: openai
  model: gpt-4o-mini
  temperature: 0.2
`

func TestParse_MinimalValid(t *testing.T) {
	cfg, err := gosuto.Parse([]byte(minimalValid))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if cfg.Metadata.Name != "test-agent" {
		t.Errorf("name: got %q, want %q", cfg.Metadata.Name, "test-agent")
	}
	if cfg.APIVersion != "gosuto/v1" {
		t.Errorf("apiVersion: got %q, want %q", cfg.APIVersion, "gosuto/v1")
	}
}

func TestParse_FullValid(t *testing.T) {
	cfg, err := gosuto.Parse([]byte(fullValid))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if cfg.Metadata.Template != "cron-agent" {
		t.Errorf("template: got %q, want %q", cfg.Metadata.Template, "cron-agent")
	}
	if len(cfg.Capabilities) != 2 {
		t.Errorf("capabilities count: got %d, want 2", len(cfg.Capabilities))
	}
	if len(cfg.MCPs) != 1 {
		t.Errorf("mcps count: got %d, want 1", len(cfg.MCPs))
	}
	if cfg.MCPs[0].Name != "brave-search" {
		t.Errorf("mcp name: got %q, want %q", cfg.MCPs[0].Name, "brave-search")
	}
	if len(cfg.Secrets) != 1 {
		t.Errorf("secrets count: got %d, want 1", len(cfg.Secrets))
	}
	if cfg.Persona.Temperature == nil || *cfg.Persona.Temperature != 0.2 {
		t.Errorf("temperature: got %v, want 0.2", cfg.Persona.Temperature)
	}
}

func TestValidate_WrongAPIVersion(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v99
metadata:
  name: x
trust:
  allowedRooms: ["!r:e"]
  allowedSenders: ["@a:e"]
`))
	if err == nil {
		t.Fatal("expected error for wrong apiVersion, got nil")
	}
}

func TestValidate_EmptyMetadataName(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v1
metadata:
  name: ""
trust:
  allowedRooms: ["!r:e"]
  allowedSenders: ["@a:e"]
`))
	if err == nil {
		t.Fatal("expected error for empty metadata.name, got nil")
	}
}

func TestValidate_InvalidRoomID(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v1
metadata:
  name: x
trust:
  allowedRooms: ["not-a-room-id"]
  allowedSenders: ["@a:e"]
`))
	if err == nil {
		t.Fatal("expected error for invalid room ID, got nil")
	}
}

func TestValidate_InvalidSenderMXID(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v1
metadata:
  name: x
trust:
  allowedRooms: ["!r:e"]
  allowedSenders: ["not-an-mxid"]
`))
	if err == nil {
		t.Fatal("expected error for invalid sender MXID, got nil")
	}
}

func TestValidate_WildcardRoomAndSenderAllowed(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v1
metadata:
  name: x
trust:
  allowedRooms: ["*"]
  allowedSenders: ["*"]
`))
	if err != nil {
		t.Fatalf("wildcard trust should be valid: %v", err)
	}
}

func TestValidate_DuplicateMCPName(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v1
metadata:
  name: x
trust:
  allowedRooms: ["*"]
  allowedSenders: ["*"]
mcps:
  - name: foo
    command: foo
  - name: foo
    command: bar
`))
	if err == nil {
		t.Fatal("expected error for duplicate MCP name, got nil")
	}
}

func TestValidate_NegativeTemperature(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v1
metadata:
  name: x
trust:
  allowedRooms: ["*"]
  allowedSenders: ["*"]
persona:
  temperature: -0.1
`))
	if err == nil {
		t.Fatal("expected error for negative temperature, got nil")
	}
}

func TestValidate_TemperatureAboveMax(t *testing.T) {
	_, err := gosuto.Parse([]byte(`
apiVersion: gosuto/v1
metadata:
  name: x
trust:
  allowedRooms: ["*"]
  allowedSenders: ["*"]
persona:
  temperature: 2.1
`))
	if err == nil {
		t.Fatal("expected error for temperature > 2.0, got nil")
	}
}

func TestValidate_InvalidYAML(t *testing.T) {
	_, err := gosuto.Parse([]byte(`{not valid: yaml: :`))
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

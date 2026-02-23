package gosuto_test

import (
	"strings"
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

// ── Saito agent template tests ────────────────────────────────────────────────

// saitoRendered is the saito-agent gosuto.yaml template with Go template vars
// replaced by concrete values, as produced by the template loader at
// provisioning time.
const saitoRendered = `
apiVersion: gosuto/v1
metadata:
  name: saito
  template: saito-agent
  description: >
    Scheduling and trigger agent. Emits periodic coordination messages to
    orchestrate other agents at defined intervals. Does not reason — only
    schedules and triggers.

trust:
  allowedRooms:
    - "!admin:example.com"
  allowedSenders:
    - "*"
  requireE2EE: false
  adminRoom: "!admin:example.com"

limits:
  maxRequestsPerMinute: 2
  maxTokensPerRequest: 512
  maxConcurrentRequests: 1
  maxMonthlyCostUSD: 1.00

capabilities:
  - name: deny-all-tools
    mcp: "*"
    tool: "*"
    allow: false

gateways:
  - name: scheduler
    type: cron
    config:
      expression: "*/15 * * * *"
      payload: "Trigger scheduled check for all coordinated agents"

persona:
  systemPrompt: |
    You are Saito, a scheduling and trigger agent. Your sole responsibility is
    to emit periodic trigger messages to coordinate other agents at scheduled
    intervals. Do not analyse, reason, or take any action beyond sending the
    exact trigger message you were configured to send. Be deterministic and
    concise. Never deviate from your schedule.
  llmProvider: openai
  model: gpt-4o-mini
  temperature: 0.0
  apiKeySecretRef: saito.openai-api-key

secrets:
  - name: saito.openai-api-key
    envVar: OPENAI_API_KEY
    required: true
`

func TestParse_SaitoAgentTemplate(t *testing.T) {
	cfg, err := gosuto.Parse([]byte(saitoRendered))
	if err != nil {
		t.Fatalf("Parse saito-agent: unexpected error: %v", err)
	}

	if cfg.Metadata.Name != "saito" {
		t.Errorf("name: got %q, want %q", cfg.Metadata.Name, "saito")
	}
	if cfg.Metadata.Template != "saito-agent" {
		t.Errorf("template: got %q, want %q", cfg.Metadata.Template, "saito-agent")
	}
	if len(cfg.Capabilities) != 1 {
		t.Errorf("capabilities count: got %d, want 1", len(cfg.Capabilities))
	}
	if cfg.Capabilities[0].Name != "deny-all-tools" {
		t.Errorf("capability name: got %q, want %q", cfg.Capabilities[0].Name, "deny-all-tools")
	}
	if cfg.Capabilities[0].Allow {
		t.Error("deny-all-tools capability should have allow=false")
	}
	if len(cfg.MCPs) != 0 {
		t.Errorf("mcps count: got %d, want 0 (Saito has no MCP tools)", len(cfg.MCPs))
	}
	if len(cfg.Gateways) != 1 {
		t.Fatalf("gateways count: got %d, want 1", len(cfg.Gateways))
	}
	if cfg.Gateways[0].Name != "scheduler" {
		t.Errorf("gateway name: got %q, want %q", cfg.Gateways[0].Name, "scheduler")
	}
	if cfg.Gateways[0].Type != "cron" {
		t.Errorf("gateway type: got %q, want %q", cfg.Gateways[0].Type, "cron")
	}
	if cfg.Gateways[0].Config["expression"] != "*/15 * * * *" {
		t.Errorf("gateway expression: got %q, want %q", cfg.Gateways[0].Config["expression"], "*/15 * * * *")
	}
	if cfg.Gateways[0].Config["payload"] != "Trigger scheduled check for all coordinated agents" {
		t.Errorf("gateway payload: got %q, want %q", cfg.Gateways[0].Config["payload"], "Trigger scheduled check for all coordinated agents")
	}
	if len(cfg.Secrets) != 1 {
		t.Errorf("secrets count: got %d, want 1", len(cfg.Secrets))
	}
	if cfg.Secrets[0].Name != "saito.openai-api-key" {
		t.Errorf("secret name: got %q, want %q", cfg.Secrets[0].Name, "saito.openai-api-key")
	}
	if !cfg.Secrets[0].Required {
		t.Error("openai-api-key secret should be required")
	}
	if cfg.Persona.Model != "gpt-4o-mini" {
		t.Errorf("model: got %q, want %q", cfg.Persona.Model, "gpt-4o-mini")
	}
	if cfg.Persona.Temperature == nil || *cfg.Persona.Temperature != 0.0 {
		t.Errorf("temperature: got %v, want 0.0", cfg.Persona.Temperature)
	}
	if cfg.Limits.MaxRequestsPerMinute != 2 {
		t.Errorf("maxRequestsPerMinute: got %d, want 2", cfg.Limits.MaxRequestsPerMinute)
	}
	if cfg.Limits.MaxMonthlyCostUSD != 1.00 {
		t.Errorf("maxMonthlyCostUSD: got %g, want 1.00", cfg.Limits.MaxMonthlyCostUSD)
	}
}

// ── Kumo agent template tests ─────────────────────────────────────────────────

// kumoRendered is the kumo-agent gosuto.yaml template with Go template vars
// replaced by concrete values, as produced by the template loader at
// provisioning time.
const kumoRendered = `
apiVersion: gosuto/v1
metadata:
  name: kumo
  template: kumo-agent
  description: >
    News and web search agent. Searches Brave Search for recent news, articles,
    and public information about the companies, tickers, or topics requested by
    Ruriko or other agents.

trust:
  allowedRooms:
    - "!admin:example.com"
  allowedSenders:
    - "*"
  requireE2EE: false
  adminRoom: "!admin:example.com"

limits:
  maxRequestsPerMinute: 10
  maxTokensPerRequest: 8192
  maxConcurrentRequests: 2
  maxMonthlyCostUSD: 10.00

capabilities:
  - name: allow-brave-search
    mcp: brave-search
    tool: "*"
    allow: true

  - name: allow-fetch-read
    mcp: fetch
    tool: fetch
    allow: true
    constraints:
      method: GET

  - name: deny-all-others
    mcp: "*"
    tool: "*"
    allow: false

persona:
  systemPrompt: |
    You are Kumo, a news and web search agent. Your role is to search for and
    retrieve recent news, articles, and public information about the companies,
    tickers, or topics you are given. Summarise the key findings clearly and
    concisely in a structured format. Focus on factual reporting. Do not
    speculate or add commentary beyond what the sources say. Always include
    source references for every claim.
  llmProvider: openai
  model: gpt-4o
  temperature: 0.3
  apiKeySecretRef: kumo.openai-api-key

secrets:
  - name: kumo.openai-api-key
    envVar: OPENAI_API_KEY
    required: true
  - name: kumo.brave-api-key
    envVar: BRAVE_API_KEY
    required: true

mcps:
  - name: brave-search
    command: npx
    args:
      - "-y"
      - "@modelcontextprotocol/server-brave-search"
    env:
      BRAVE_API_KEY: "${BRAVE_API_KEY}"
    autoRestart: true

  - name: fetch
    command: npx
    args:
      - "-y"
      - "@modelcontextprotocol/server-fetch"
    autoRestart: true
`

func TestParse_KumoAgentTemplate(t *testing.T) {
	cfg, err := gosuto.Parse([]byte(kumoRendered))
	if err != nil {
		t.Fatalf("Parse kumo-agent: unexpected error: %v", err)
	}

	if cfg.Metadata.Name != "kumo" {
		t.Errorf("name: got %q, want %q", cfg.Metadata.Name, "kumo")
	}
	if cfg.Metadata.Template != "kumo-agent" {
		t.Errorf("template: got %q, want %q", cfg.Metadata.Template, "kumo-agent")
	}
	if len(cfg.Capabilities) != 3 {
		t.Errorf("capabilities count: got %d, want 3", len(cfg.Capabilities))
	}
	if cfg.Capabilities[0].Name != "allow-brave-search" {
		t.Errorf("capability[0] name: got %q, want %q", cfg.Capabilities[0].Name, "allow-brave-search")
	}
	if !cfg.Capabilities[0].Allow {
		t.Error("allow-brave-search capability should have allow=true")
	}
	if cfg.Capabilities[1].Name != "allow-fetch-read" {
		t.Errorf("capability[1] name: got %q, want %q", cfg.Capabilities[1].Name, "allow-fetch-read")
	}
	if cfg.Capabilities[1].Constraints["method"] != "GET" {
		t.Errorf("fetch constraint method: got %q, want %q", cfg.Capabilities[1].Constraints["method"], "GET")
	}
	if cfg.Capabilities[2].Name != "deny-all-others" {
		t.Errorf("capability[2] name: got %q, want %q", cfg.Capabilities[2].Name, "deny-all-others")
	}
	if cfg.Capabilities[2].Allow {
		t.Error("deny-all-others capability should have allow=false")
	}
	if len(cfg.MCPs) != 2 {
		t.Errorf("mcps count: got %d, want 2", len(cfg.MCPs))
	}
	if cfg.MCPs[0].Name != "brave-search" {
		t.Errorf("mcp[0] name: got %q, want %q", cfg.MCPs[0].Name, "brave-search")
	}
	if !cfg.MCPs[0].AutoRestart {
		t.Error("brave-search MCP should have autoRestart=true")
	}
	if cfg.MCPs[1].Name != "fetch" {
		t.Errorf("mcp[1] name: got %q, want %q", cfg.MCPs[1].Name, "fetch")
	}
	if len(cfg.Secrets) != 2 {
		t.Errorf("secrets count: got %d, want 2", len(cfg.Secrets))
	}
	if cfg.Secrets[0].Name != "kumo.openai-api-key" {
		t.Errorf("secret[0] name: got %q, want %q", cfg.Secrets[0].Name, "kumo.openai-api-key")
	}
	if cfg.Secrets[1].Name != "kumo.brave-api-key" {
		t.Errorf("secret[1] name: got %q, want %q", cfg.Secrets[1].Name, "kumo.brave-api-key")
	}
	if cfg.Persona.Model != "gpt-4o" {
		t.Errorf("model: got %q, want %q", cfg.Persona.Model, "gpt-4o")
	}
	if cfg.Persona.Temperature == nil || *cfg.Persona.Temperature != 0.3 {
		t.Errorf("temperature: got %v, want 0.3", cfg.Persona.Temperature)
	}
	if cfg.Limits.MaxRequestsPerMinute != 10 {
		t.Errorf("maxRequestsPerMinute: got %d, want 10", cfg.Limits.MaxRequestsPerMinute)
	}
	if cfg.Limits.MaxMonthlyCostUSD != 10.00 {
		t.Errorf("maxMonthlyCostUSD: got %g, want 10.00", cfg.Limits.MaxMonthlyCostUSD)
	}
}

// ── Kairo agent template tests ────────────────────────────────────────────────

// kairoRendered is the kairo-agent gosuto.yaml template with Go template vars
// replaced by concrete values, as produced by the template loader at
// provisioning time.
const kairoRendered = `
apiVersion: gosuto/v1
metadata:
  name: kairo
  template: kairo-agent
  description: >
    Financial analysis agent. Queries stock market data via Finnhub, writes
    structured analysis results to a local SQLite database, and delivers
    concise reports to the user. Triggered by Saito on a schedule; works with
    Kumo for news context enrichment.

trust:
  allowedRooms:
    - "!admin:example.com"
  allowedSenders:
    - "*"
  requireE2EE: false
  adminRoom: "!admin:example.com"

limits:
  maxRequestsPerMinute: 10
  maxTokensPerRequest: 16384
  maxConcurrentRequests: 2
  maxMonthlyCostUSD: 20.00

capabilities:
  - name: allow-finnhub-all
    mcp: finnhub
    tool: "*"
    allow: true

  - name: allow-database-read
    mcp: database
    tool: read_records
    allow: true

  - name: allow-database-write
    mcp: database
    tool: create_record
    allow: true

  - name: allow-database-update
    mcp: database
    tool: update_records
    allow: true

  - name: allow-database-query
    mcp: database
    tool: query
    allow: true

  - name: allow-database-schema
    mcp: database
    tool: get_table_schema
    allow: true

  - name: allow-database-info
    mcp: database
    tool: db_info
    allow: true

  - name: allow-database-tables
    mcp: database
    tool: list_tables
    allow: true

  - name: deny-all-others
    mcp: "*"
    tool: "*"
    allow: false

approvals:
  requireApproval: false

persona:
  systemPrompt: |
    You are Kairo, a meticulous financial analyst. Your responsibilities are:
    1. Retrieve stock market data (prices, financials, news) using your finnhub tools.
    2. Analyse portfolio performance and market conditions objectively and rigorously.
    3. Store structured analysis results in the database for historical tracking.
    4. Deliver concise, actionable reports to the user — only when there is something
       meaningful to report (material price movements, significant news, or notable trends).
    5. Collaborate with Kumo for news context when deeper narrative context is needed.

    Principles:
    - Focus on verifiable, data-backed facts. Never speculate beyond what the data supports.
    - Always cite the data source and timestamp for every claim.
    - Be conservative in assessments. "Unclear" is a valid conclusion.
    - Persist every analysis run to the database before sending any report.
    - Keep reports brief: headline finding, supporting data, confidence level.
  llmProvider: openai
  model: gpt-4o
  temperature: 0.2
  apiKeySecretRef: kairo.openai-api-key

secrets:
  - name: kairo.openai-api-key
    envVar: OPENAI_API_KEY
    required: true
  - name: kairo.finnhub-api-key
    envVar: FINNHUB_API_KEY
    required: true

mcps:
  - name: finnhub
    command: uv
    args:
      - "--directory"
      - "/opt/mcps/stock-market-mcp-server"
      - "run"
      - "stock_market_server.py"
    env:
      FINNHUB_API_KEY: "${FINNHUB_API_KEY}"
    autoRestart: true

  - name: database
    command: npx
    args:
      - "-y"
      - "mcp-sqlite"
      - "/data/kairo.db"
    autoRestart: true
`

func TestParse_KairoAgentTemplate(t *testing.T) {
	cfg, err := gosuto.Parse([]byte(kairoRendered))
	if err != nil {
		t.Fatalf("Parse kairo-agent: unexpected error: %v", err)
	}

	if cfg.Metadata.Name != "kairo" {
		t.Errorf("name: got %q, want %q", cfg.Metadata.Name, "kairo")
	}
	if cfg.Metadata.Template != "kairo-agent" {
		t.Errorf("template: got %q, want %q", cfg.Metadata.Template, "kairo-agent")
	}

	// ── Capabilities ─────────────────────────────────────────────────────────
	if len(cfg.Capabilities) != 9 {
		t.Errorf("capabilities count: got %d, want 9", len(cfg.Capabilities))
	}
	if cfg.Capabilities[0].Name != "allow-finnhub-all" {
		t.Errorf("capability[0] name: got %q, want %q", cfg.Capabilities[0].Name, "allow-finnhub-all")
	}
	if !cfg.Capabilities[0].Allow {
		t.Error("allow-finnhub-all capability should have allow=true")
	}
	if cfg.Capabilities[0].MCP != "finnhub" {
		t.Errorf("capability[0] mcp: got %q, want %q", cfg.Capabilities[0].MCP, "finnhub")
	}
	// Last rule must deny everything else.
	last := cfg.Capabilities[len(cfg.Capabilities)-1]
	if last.Name != "deny-all-others" {
		t.Errorf("last capability name: got %q, want %q", last.Name, "deny-all-others")
	}
	if last.Allow {
		t.Error("deny-all-others capability should have allow=false")
	}

	// ── MCP servers ──────────────────────────────────────────────────────────
	if len(cfg.MCPs) != 2 {
		t.Fatalf("mcps count: got %d, want 2", len(cfg.MCPs))
	}

	finnhub := cfg.MCPs[0]
	if finnhub.Name != "finnhub" {
		t.Errorf("mcp[0] name: got %q, want %q", finnhub.Name, "finnhub")
	}
	if finnhub.Command != "uv" {
		t.Errorf("mcp[0] command: got %q, want %q", finnhub.Command, "uv")
	}
	if len(finnhub.Args) < 4 {
		t.Errorf("mcp[0] args: got %d args, want at least 4", len(finnhub.Args))
	}
	if finnhub.Env["FINNHUB_API_KEY"] == "" {
		t.Error("mcp[0] env FINNHUB_API_KEY should not be empty")
	}
	if !finnhub.AutoRestart {
		t.Error("finnhub MCP should have autoRestart=true")
	}

	database := cfg.MCPs[1]
	if database.Name != "database" {
		t.Errorf("mcp[1] name: got %q, want %q", database.Name, "database")
	}
	if database.Command != "npx" {
		t.Errorf("mcp[1] command: got %q, want %q", database.Command, "npx")
	}
	// Args should include mcp-sqlite package name and database path.
	argsStr := strings.Join(database.Args, " ")
	if !strings.Contains(argsStr, "mcp-sqlite") {
		t.Errorf("database MCP args should contain mcp-sqlite: %v", database.Args)
	}
	if !strings.Contains(argsStr, "kairo.db") {
		t.Errorf("database MCP args should contain agent-specific db path: %v", database.Args)
	}
	if !database.AutoRestart {
		t.Error("database MCP should have autoRestart=true")
	}

	// ── Secrets ───────────────────────────────────────────────────────────────
	if len(cfg.Secrets) != 2 {
		t.Fatalf("secrets count: got %d, want 2", len(cfg.Secrets))
	}
	if cfg.Secrets[0].Name != "kairo.openai-api-key" {
		t.Errorf("secret[0] name: got %q, want %q", cfg.Secrets[0].Name, "kairo.openai-api-key")
	}
	if !cfg.Secrets[0].Required {
		t.Error("openai-api-key secret should be required")
	}
	if cfg.Secrets[1].Name != "kairo.finnhub-api-key" {
		t.Errorf("secret[1] name: got %q, want %q", cfg.Secrets[1].Name, "kairo.finnhub-api-key")
	}
	if !cfg.Secrets[1].Required {
		t.Error("finnhub-api-key secret should be required")
	}

	// ── Persona ───────────────────────────────────────────────────────────────
	if cfg.Persona.Model != "gpt-4o" {
		t.Errorf("model: got %q, want %q", cfg.Persona.Model, "gpt-4o")
	}
	if cfg.Persona.Temperature == nil || *cfg.Persona.Temperature != 0.2 {
		t.Errorf("temperature: got %v, want 0.2", cfg.Persona.Temperature)
	}
	if cfg.Persona.APIKeySecretRef != "kairo.openai-api-key" {
		t.Errorf("apiKeySecretRef: got %q, want %q", cfg.Persona.APIKeySecretRef, "kairo.openai-api-key")
	}

	// ── Limits ────────────────────────────────────────────────────────────────
	if cfg.Limits.MaxRequestsPerMinute != 10 {
		t.Errorf("maxRequestsPerMinute: got %d, want 10", cfg.Limits.MaxRequestsPerMinute)
	}
	if cfg.Limits.MaxTokensPerRequest != 16384 {
		t.Errorf("maxTokensPerRequest: got %d, want 16384", cfg.Limits.MaxTokensPerRequest)
	}
	if cfg.Limits.MaxMonthlyCostUSD != 20.00 {
		t.Errorf("maxMonthlyCostUSD: got %g, want 20.00", cfg.Limits.MaxMonthlyCostUSD)
	}
}

// ── Gateway round-trip tests ──────────────────────────────────────────────────

const gatewayValid = `
apiVersion: gosuto/v1
metadata:
  name: gateway-agent
trust:
  allowedRooms:
    - "*"
  allowedSenders:
    - "*"
limits:
  maxEventsPerMinute: 30
gateways:
  - name: scheduler
    type: cron
    config:
      expression: "*/15 * * * *"
      payload: "Trigger scheduled check"
  - name: inbound
    type: webhook
    config:
      authType: hmac-sha256
      hmacSecretRef: gateway-agent.webhook-secret
  - name: external-gw
    command: /usr/local/bin/my-gateway
    args:
      - "--verbose"
    env:
      CUSTOM_VAR: "value"
    config:
      key: val
    autoRestart: true
`

func TestParse_GatewayRoundTrip(t *testing.T) {
	cfg, err := gosuto.Parse([]byte(gatewayValid))
	if err != nil {
		t.Fatalf("Parse gateway config: unexpected error: %v", err)
	}

	if cfg.Limits.MaxEventsPerMinute != 30 {
		t.Errorf("maxEventsPerMinute: got %d, want 30", cfg.Limits.MaxEventsPerMinute)
	}

	if len(cfg.Gateways) != 3 {
		t.Fatalf("gateways count: got %d, want 3", len(cfg.Gateways))
	}

	// cron gateway
	cron := cfg.Gateways[0]
	if cron.Name != "scheduler" {
		t.Errorf("gateway[0].name: got %q, want %q", cron.Name, "scheduler")
	}
	if cron.Type != "cron" {
		t.Errorf("gateway[0].type: got %q, want %q", cron.Type, "cron")
	}
	if cron.Command != "" {
		t.Errorf("gateway[0].command: got %q, want empty", cron.Command)
	}
	if cron.Config["expression"] != "*/15 * * * *" {
		t.Errorf("gateway[0].config.expression: got %q, want %q", cron.Config["expression"], "*/15 * * * *")
	}
	if cron.Config["payload"] != "Trigger scheduled check" {
		t.Errorf("gateway[0].config.payload: got %q, want %q", cron.Config["payload"], "Trigger scheduled check")
	}

	// webhook gateway
	webhook := cfg.Gateways[1]
	if webhook.Name != "inbound" {
		t.Errorf("gateway[1].name: got %q, want %q", webhook.Name, "inbound")
	}
	if webhook.Type != "webhook" {
		t.Errorf("gateway[1].type: got %q, want %q", webhook.Type, "webhook")
	}
	if webhook.Config["authType"] != "hmac-sha256" {
		t.Errorf("gateway[1].config.authType: got %q, want %q", webhook.Config["authType"], "hmac-sha256")
	}
	if webhook.Config["hmacSecretRef"] != "gateway-agent.webhook-secret" {
		t.Errorf("gateway[1].config.hmacSecretRef: got %q, want %q", webhook.Config["hmacSecretRef"], "gateway-agent.webhook-secret")
	}

	// external gateway
	ext := cfg.Gateways[2]
	if ext.Name != "external-gw" {
		t.Errorf("gateway[2].name: got %q, want %q", ext.Name, "external-gw")
	}
	if ext.Type != "" {
		t.Errorf("gateway[2].type: got %q, want empty", ext.Type)
	}
	if ext.Command != "/usr/local/bin/my-gateway" {
		t.Errorf("gateway[2].command: got %q, want %q", ext.Command, "/usr/local/bin/my-gateway")
	}
	if len(ext.Args) != 1 || ext.Args[0] != "--verbose" {
		t.Errorf("gateway[2].args: got %v, want [--verbose]", ext.Args)
	}
	if ext.Env["CUSTOM_VAR"] != "value" {
		t.Errorf("gateway[2].env.CUSTOM_VAR: got %q, want %q", ext.Env["CUSTOM_VAR"], "value")
	}
	if ext.Config["key"] != "val" {
		t.Errorf("gateway[2].config.key: got %q, want %q", ext.Config["key"], "val")
	}
	if !ext.AutoRestart {
		t.Error("gateway[2].autoRestart: got false, want true")
	}
}

func TestParse_GatewayMaxEventsPerMinuteZero(t *testing.T) {
	// MaxEventsPerMinute omitted (zero/unlimited) should still parse cleanly.
	cfg, err := gosuto.Parse([]byte(minimalValid))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if cfg.Limits.MaxEventsPerMinute != 0 {
		t.Errorf("maxEventsPerMinute default: got %d, want 0", cfg.Limits.MaxEventsPerMinute)
	}
	if len(cfg.Gateways) != 0 {
		t.Errorf("gateways default: got %d, want 0", len(cfg.Gateways))
	}
}

// ── R11.2 gateway validation tests ───────────────────────────────────────────

func gatewayBase() string {
	return `
apiVersion: gosuto/v1
metadata:
  name: x
trust:
  allowedRooms: ["*"]
  allowedSenders: ["*"]
`
}

func TestValidate_Gateway_MissingName(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - type: cron
    config:
      expression: "* * * * *"
`))
	if err == nil {
		t.Fatal("expected error for gateway with empty name, got nil")
	}
}

func TestValidate_Gateway_BothTypeAndCommand(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: both
    type: cron
    command: /bin/gw
    config:
      expression: "* * * * *"
`))
	if err == nil {
		t.Fatal("expected error when both type and command are set, got nil")
	}
}

func TestValidate_Gateway_NeitherTypeNorCommand(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: neither
`))
	if err == nil {
		t.Fatal("expected error when neither type nor command is set, got nil")
	}
}

func TestValidate_Gateway_UnknownType(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: bad-type
    type: imap
`))
	if err == nil {
		t.Fatal("expected error for unknown gateway type, got nil")
	}
}

func TestValidate_Gateway_CronMissingExpression(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: no-expr
    type: cron
`))
	if err == nil {
		t.Fatal("expected error for cron gateway without expression, got nil")
	}
}

func TestValidate_Gateway_WebhookHMACMissingSecretRef(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: no-ref
    type: webhook
    config:
      authType: hmac-sha256
`))
	if err == nil {
		t.Fatal("expected error for hmac-sha256 webhook without hmacSecretRef, got nil")
	}
}

func TestValidate_Gateway_WebhookHMACValid(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: sig-hook
    type: webhook
    config:
      authType: hmac-sha256
      hmacSecretRef: x.webhook-secret
`))
	if err != nil {
		t.Fatalf("valid hmac webhook should pass: %v", err)
	}
}

func TestValidate_Gateway_WebhookBearerNoRef(t *testing.T) {
	// webhook with bearer (or no authType) needs no hmacSecretRef
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: bearer-hook
    type: webhook
    config:
      authType: bearer
`))
	if err != nil {
		t.Fatalf("webhook with bearer auth should be valid: %v", err)
	}
}

func TestValidate_Gateway_ExternalCommandValid(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: ext-gw
    command: /usr/local/bin/my-gateway
    args: ["--verbose"]
    autoRestart: true
`))
	if err != nil {
		t.Fatalf("external gateway with command should be valid: %v", err)
	}
}

func TestValidate_Gateway_DuplicateGatewayName(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
gateways:
  - name: dup
    type: cron
    config:
      expression: "* * * * *"
  - name: dup
    type: cron
    config:
      expression: "*/5 * * * *"
`))
	if err == nil {
		t.Fatal("expected error for duplicate gateway names, got nil")
	}
}

func TestValidate_Gateway_NameCollidesWithMCP(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
mcps:
  - name: scheduler
    command: /bin/my-mcp
gateways:
  - name: scheduler
    type: cron
    config:
      expression: "* * * * *"
`))
	if err == nil {
		t.Fatal("expected error when gateway name collides with MCP name, got nil")
	}
}

func TestValidate_Limits_NegativeMaxEventsPerMinute(t *testing.T) {
	_, err := gosuto.Parse([]byte(gatewayBase() + `
limits:
  maxEventsPerMinute: -1
`))
	if err == nil {
		t.Fatal("expected error for negative maxEventsPerMinute, got nil")
	}
}

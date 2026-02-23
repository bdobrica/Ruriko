# Copilot Instructions — Ruriko

## What This Project Is

Ruriko is a self-hosted conversational control plane for secure, capability-scoped AI agents over Matrix. A human talks to **Ruriko** (the control plane) over Matrix, and Ruriko plans and configures specialized LLM-powered agents called **Gitai** that collaborate **peer-to-peer** — messaging each other directly over Matrix.

The project is written in **Go 1.25**, uses **SQLite** (WAL mode) for state, **mautrix-go** for Matrix communication, and deploys via **Docker Compose** with a bundled **Tuwunel** Matrix homeserver.

---

## Key Concepts (Glossary)

| Term | Meaning |
|------|---------|
| **Ruriko** | The control plane. Manages agent lifecycle, secrets, policy, approvals. Deterministic — no LLM in the control path. |
| **Gitai** | The agent runtime. Each agent is a single Go binary running in its own container. Connects to Matrix, calls LLMs, runs MCP tools, enforces policy. |
| **Gosuto** | Versioned YAML config (`gosuto/v1`) that defines an agent's trust boundaries, capabilities, persona, instructions, MCP servers, gateways, and secrets. |
| **ACP** | Agent Control Protocol — HTTP API between Ruriko and Gitai for lifecycle control, config push, health checks. Private to the Docker network. |
| **Kuze** | One-time link secret entry system. Secrets never travel through Matrix. |
| **MCP** | Model Context Protocol — tool integration standard. Agents run MCP server processes (supervised by Gitai) to access external tools. |
| **Gosuto three-layer model** | **Policy** (authoritative, code-enforced) > **Instructions** (operational workflow, auditable) > **Persona** (cosmetic tone/style). See `docs/invariants.md` §2. |

---

## Repository Layout

```
cmd/                              # Entrypoints (main.go files)
  ruriko/                         # Ruriko control plane binary
  gitai/                          # Gitai agent runtime binary
  gateway/ruriko-gw-imap/         # External IMAP gateway binary
  tools/                          # CLI utilities (gosuto-validate, envelope-lint, keygen)

internal/                         # Private application code (the bulk of the logic)
  ruriko/                         # --- Ruriko control plane ---
    app/                          #   Top-level wiring, startup
    commands/                     #   Deterministic Matrix command router (/ruriko <cmd>)
    config/                       #   Runtime config store
    store/                        #   SQLite store (agents, secrets, audit, gosuto versions)
    secrets/                      #   Encrypted secret storage + distribution
    kuze/                         #   One-time secret entry links
    matrix/                       #   mautrix-go wrapper for Ruriko
    provisioning/                 #   Matrix account provisioning (homeserver admin API)
    runtime/                      #   Agent lifecycle (Docker/K8s/local adapters), reconciler
      docker/                     #     Docker runtime adapter
      acp/                        #     ACP HTTP client (Ruriko → Gitai)
    approvals/                    #   Approval workflow engine
    audit/                        #   Audit log writer
    nlp/                          #   Natural language interface (LLM-powered command translation)
    templates/                    #   Template loading and rendering
    webhook/                      #   Webhook proxy for agents

  gitai/                          # --- Gitai agent runtime ---
    app/                          #   Main turn loop: message → policy → LLM → tools → reply
    control/                      #   ACP HTTP server (receives commands from Ruriko)
    matrix/                       #   mautrix-go wrapper for agents
    llm/                          #   LLM provider abstraction (OpenAI, etc.)
    policy/                       #   Capability-based policy engine (first-match-wins)
    mcp/                          #   MCP client (JSON-RPC over stdio)
    supervisor/                   #   MCP + gateway process supervisor
    gateway/                      #   Built-in cron + webhook gateways
    gosuto/                       #   Gosuto config loader
    secrets/                      #   Agent-side secret manager
    approvals/                    #   Approval request tracking
    envelope/                     #   Structured message envelope parser
    store/                        #   Agent-local SQLite store
    observability/                #   Structured logging (slog)

common/                           # Shared packages (used by both Ruriko and Gitai)
  crypto/                         #   AES-GCM encryption, keystore
  environment/                    #   Env var helpers (StringOr, BoolOr, etc.)
  redact/                         #   Secret redaction for logs
  retry/                          #   Retry with backoff
  spec/                           #   Shared specs
    gosuto/                       #     Gosuto types + validation (Config, Trust, Capabilities…)
    envelope/                     #     Event envelope types
    policy/                       #     (reserved)
  trace/                          #   Trace ID generation
  validate/                       #   (reserved)
  version/                        #   Build version info (injected via ldflags)

templates/                        # Gosuto YAML templates for canonical agents
  saito-agent/gosuto.yaml         #   Cron/trigger agent
  kairo-agent/gosuto.yaml         #   Finance/analysis agent (finnhub)
  kumo-agent/gosuto.yaml          #   News/search agent (Brave Search)
  browser-agent/gosuto.yaml       #   Headless browser agent
  email-agent/gosuto.yaml         #   Email-reactive agent
  cron-agent/gosuto.yaml          #   Generic cron agent

schemas/                          # JSON schemas
  gosuto/                         #   Gosuto JSON schema
  envelope/                       #   Event envelope JSON schema

migrations/                       # SQL migration files (embedded via go:embed)
  ruriko/                         #   Ruriko database migrations
  gitai/                          #   Gitai database migrations

deploy/                           # Deployment artifacts
  docker/                         #   Dockerfiles (Dockerfile.ruriko, Dockerfile.gitai), entrypoint
  helm/ruriko/                    #   Helm chart (WIP)
  systemd/                        #   systemd units

examples/docker-compose/          # Reference docker-compose.yaml + Tuwunel config
test/                             # Test artifacts
  fixtures/                       #   Test data files
  integration/                    #   Integration test scripts
  e2e/                            #   End-to-end tests

docs/                             # Architecture and design documents (see below)
```

---

## Key Documents — Read These First

| Document | What it tells you |
|----------|-------------------|
| `docs/invariants.md` | **The 12 system invariants.** The non-negotiable rules of the architecture. Read this before making any design decision. Contains the enforcement checklist for new features. |
| `docs/architecture.md` | System architecture, component interactions, data flow, communication protocols. |
| `docs/preamble.md` | Product story, UX contract, canonical user story (Saito → Kairo → Kumo workflow), glossary. |
| `docs/gosuto-spec.md` | Gosuto v1 specification — all YAML fields, validation rules, examples. |
| `docs/threat-model.md` | Security analysis and threat scenarios. |
| `TODO.md` | **Active roadmap.** Shows current phase, what's done, what's in progress, and what's next. Check this before starting any task. |
| `CHANGELOG.md` | Completed phases with full task lists. Historical reference for what was built and when. |
| `OPERATIONS.md` | Step-by-step guide for spinning up the stack locally (Docker Compose, account creation, testing flows). |
| `README.md` | High-level overview, architecture summary, deployment philosophy. |

---

## Coding Conventions

### Go Style
- **Go 1.25** — use modern idioms (structured logging with `log/slog`, errors with `%w`, etc.)
- Standard library preferred over external dependencies when possible
- Tabs for indentation in Go files (as per `.editorconfig`)
- Imports: stdlib first, then external, then internal (`github.com/bdobrica/Ruriko/...`) — enforced by `goimports` with `local-prefixes: github.com/bdobrica/Ruriko`
- Every package has a doc comment explaining its purpose
- Error messages start lowercase, don't end with punctuation: `fmt.Errorf("failed to open database: %w", err)`

### Testing
- Tests live next to the code they test (`foo.go` → `foo_test.go`)
- Use the standard `testing` package — no test frameworks
- Table-driven tests preferred for parameterized cases
- Test function names: `TestFunctionName_Scenario` (e.g., `TestBuildGatewayEnv_InjectsSecretEnvAndSpecEnv`)
- Run tests: `make test` or `go test -v -race ./...`

### Linting
- `golangci-lint` with config in `.golangci.yml`
- Cyclomatic complexity limit: 15
- Security checks via `gosec`
- Run: `make lint`

### Build
- `make build` builds all binaries to `bin/`
- `make docker-build` builds Docker images (`ruriko:latest`, `gitai:latest`)
- Version info injected via ldflags from git tags — see `common/version/version.go`
- Build vars: `GIT_COMMIT`, `GIT_TAG`, `BUILD_TIME`

### Database
- SQLite with WAL mode, foreign keys enabled
- Migrations in `migrations/{ruriko,gitai}/` embedded via `//go:embed`
- Auto-migration on startup — see `internal/{ruriko,gitai}/store/store.go`
- Ruriko store split into domain files: `agents.go`, `audit.go`, `secrets.go`, etc.

### Configuration
- All configuration via environment variables — see `common/environment/` helpers
- No config files for runtime settings (Gosuto is the agent config, not a runtime config)
- `.env.example` documents all variables

---

## Architecture Rules (Invariants Summary)

These are enforced throughout the codebase. Violating them is a bug:

1. **Deterministic control boundary** — Ruriko's command parser is deterministic. No LLM decides lifecycle/secrets/policy operations.
2. **Policy > Instructions > Persona** — Gosuto enforces a three-layer authority model. Policy is code-enforced, instructions are operational, persona is cosmetic.
3. **Immutable runtime** — Gitai cannot self-modify its binary, config, or privileges.
4. **No root secrets** — Agents only get scoped, leased secrets. Never the master key.
5. **Explicit approval for destructive ops** — Delete, rotate, risky capabilities require human approval.
6. **Audit everything** — Every command, tool call, and approval is logged with a trace ID.
7. **Fail safely** — Default deny. Errors → refusal, not silent failure.
8. **Secrets never leak** — No raw secrets in logs, Matrix messages, or tool traces.
9. **Trust contexts are explicit** — Room/sender allowlists checked before processing.
10. **Configuration is versioned** — Every Gosuto change is hashed, versioned, and diffable.
11. **Secrets never enter via Matrix** — Secrets use Kuze one-time links only.
12. **Inter-agent communication is policy-gated** — Agents can only message rooms explicitly allowed in their Gosuto.

Full details with enforcement checklists: `docs/invariants.md`

---

## Communication Channels

The system uses three distinct channels — never mix them:

| Channel | Purpose | Implementation |
|---------|---------|----------------|
| **Matrix** | Human ↔ Ruriko dialogue, agent ↔ agent peer-to-peer collaboration | mautrix-go |
| **ACP** | Lifecycle control, config push, health checks, event delivery | HTTP, private Docker network |
| **Kuze** | One-time secret entry (human) and redemption (agent) | HTTP with single-use tokens |

---

## Data Flow: The Turn Loop (Gitai)

The core processing loop in `internal/gitai/app/app.go`:

1. Matrix message (or event gateway trigger) arrives
2. Trust context check (allowed room? allowed sender?)
3. Policy evaluation (capability rules, first-match-wins, default deny)
4. System prompt assembly (persona + instructions + peer context)
5. LLM call with available tools
6. Tool call execution (MCP tools → MCP client; built-in tools → local handler)
7. Policy check on each tool call
8. Loop back to LLM if more tool calls needed (max 10 rounds)
9. Send reply to Matrix

---

## How to Navigate for Common Tasks

| Task | Start here |
|------|-----------|
| Add a Ruriko command | `internal/ruriko/commands/` — add handler, register in router |
| Modify Gosuto schema | `common/spec/gosuto/types.go` + `validate.go`, update `docs/gosuto-spec.md` |
| Change agent policy logic | `internal/gitai/policy/engine.go` |
| Add an MCP tool integration | Template in `templates/`, wired in Gosuto YAML under `mcps:` |
| Modify the LLM prompt | `internal/gitai/app/prompt.go` |
| Add a database table | New migration in `migrations/{ruriko,gitai}/`, update store package |
| Add a built-in tool to Gitai | (Being built in R15) — tool registry in Gitai runtime |
| Agent lifecycle changes | `internal/ruriko/runtime/` (adapters) + `internal/ruriko/runtime/acp/` (client) |
| Secret management | `internal/ruriko/secrets/` (Ruriko-side) + `internal/gitai/secrets/` (agent-side) |
| Event gateway work | `internal/gitai/gateway/` (built-in) + `internal/gitai/supervisor/gateway.go` (external) |
| Template changes | `templates/<agent>/gosuto.yaml` |

---

## Active Work

Always check `TODO.md` for the current phase and what's in progress. The roadmap is organized in numbered phases (R0, R1, … R15, etc.) with sub-tasks. `CHANGELOG.md` has the full history of completed phases.

The canonical end-to-end workflow being built: **Saito** (cron trigger) → **Kairo** (finance analysis) → **Kumo** (news search) → report delivered to the user.

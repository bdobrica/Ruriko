# Ruriko Implementation TODO

> Roadmap for building a conversational control plane for secure agentic automation over Matrix.

**Project Goal**: Build Ruriko, a self-hosted system where a human talks to Ruriko over Matrix, and Ruriko coordinates specialized LLM-powered agents (Gitai) that collaborate like a small team — with secrets handled securely and control operations kept off the conversation layer.

See [docs/preamble.md](docs/preamble.md) for the full product story and [REALIGNMENT_PLAN.md](REALIGNMENT_PLAN.md) for the realignment rationale.

---

## 🎯 MVP Definition of Done

The MVP is ready when:

- A user can deploy with `docker compose up -d`
- The Matrix homeserver is Tuwunel, federation OFF, registration OFF
- The user can chat with Ruriko over Matrix
- The user can store secrets via Kuze one-time links (never in chat)
- Ruriko can provision Saito/Kairo/Kumo agents and apply Gosuto config via ACP
- ACP is authenticated and idempotent
- Saito triggers Kairo every 15 minutes
- Kairo fetches data from finnhub and stores results in DB
- Kumo fetches news for relevant tickers
- Bogdan receives a final report that combines market data + news
- No secrets appear in Matrix history, ACP payloads, or logs

---

## 🎯 Infrastructure Scope (COMPLETED)

The following foundations were built in Phases 0–9 and are functional:

- ✅ Ruriko control plane (Matrix-based command interface)
- ✅ SQLite-backed agent inventory and audit logging
- ✅ Secrets management (encrypted at rest, push to agents via ACP)
- ✅ Agent lifecycle control (spawn/stop/respawn via Docker)
- ✅ Gosuto configuration versioning and application
- ✅ Approval workflow for sensitive operations
- ✅ Basic observability (audit log, trace correlation)
- ✅ Gitai agent runtime (Matrix + ACP server + MCP + policy engine)
- ✅ Docker image and Compose example (Synapse-based, pre-realignment)

These are **real, working subsystems** — not scaffolding. The realignment phases below build on this foundation.

---

## 📋 Phase 0: Project Setup & Foundations ✅

### 0.1 Project Initialization
- [x] Initialize Go module (`go mod init github.com/bdobrica/Ruriko`)
- [x] Set up basic project structure following [REPO_STRUCTURE.md](REPO_STRUCTURE.md)
- [x] Create directory structure:
  - [x] `cmd/ruriko/`
  - [x] `internal/ruriko/`
  - [x] `common/`
  - [x] `migrations/ruriko/`
  - [x] `templates/`
  - [x] `docs/`
- [x] Set up `.gitignore` (Go binaries, SQLite dbs, secrets, IDE files)
- [x] Create `Makefile` with basic targets (build, test, lint, run)
- [x] Set up `.golangci.yml` for linting

### 0.2 Documentation - Lock the Invariants
- [x] Create `docs/invariants.md` documenting Ruriko's hard boundaries:
  - [x] Ruriko is deterministic for control actions (no LLM decides lifecycle/secrets/policy)
  - [x] Agents never get root secrets (scoped/leased only)
  - [x] Gitai runtime is immutable (signed), Gosuto is mutable (versioned)
  - [x] All destructive operations require explicit approval
- [x] Create `docs/architecture.md` with high-level system design
- [x] Create `docs/threat-model.md` with security considerations

### 0.3 Dependencies
- [x] Add `mautrix-go` for Matrix client (`go get maunium.net/go/mautrix`)
- [x] Add SQLite driver (`go get modernc.org/sqlite`)
- [x] Add crypto libraries for secrets encryption (standard library AES-GCM)
- [x] Add migration tool (custom embedded migration runner)
- [x] Add structured logging library (standard `log/slog`)

---

## 📋 Phase 1: Ruriko MVP - Matrix Control + Inventory ✅

**Goal**: Get Ruriko online and responding to basic commands via Matrix.

### 1.1 Basic Matrix Connection
- [x] Create `internal/ruriko/matrix/client.go` - mautrix-go wrapper
- [x] Implement Matrix login (password or access token)
- [x] Implement room joining logic
- [x] Create `cmd/ruriko/main.go` - basic binary that connects and logs events
- [x] Test: Binary connects to homeserver and joins configured admin room

### 1.2 Command Router
- [x] Create `internal/ruriko/commands/router.go` - deterministic command parser
- [x] Implement command structure: `/ruriko <subcommand> [args]`
- [x] Create command registry pattern
- [x] Implement initial commands:
  - [x] `/ruriko help` - list available commands
  - [x] `/ruriko ping` - health check
  - [x] `/ruriko version` - runtime version info
- [x] Add permission checking (admin room sender filtering)
- [x] Test: Commands parse correctly (router_test.go — 6 tests)

### 1.3 SQLite Schema + Migrations
- [x] Create `internal/ruriko/store/migrations/0001_init.sql`:
  - [x] `agents` table (id, mxid, display_name, template, status, last_seen, runtime_version, gosuto_version, created_at, updated_at)
  - [x] `agent_endpoints` table (agent_id, control_url, matrix_room_id, pubkey, last_heartbeat)
  - [x] `secrets` table (name, type, encrypted_blob, rotation_version, created_at, updated_at)
  - [x] `agent_secret_bindings` table (agent_id, secret_name, scope, last_pushed_version)
  - [x] `gosuto_versions` table (agent_id, version, hash, yaml_blob, created_at, created_by_mxid)
  - [x] `audit_log` table (id, ts, actor_mxid, action, target, trace_id, payload_json, result)
- [x] Create `internal/ruriko/store/store.go` - database wrapper
- [x] Create migration runner (embedded SQL via `//go:embed`)
- [x] Implement auto-migration on startup
- [x] Test: Database initializes correctly (store_test.go — 12 tests)

### 1.4 Agent Inventory Commands
- [x] `/ruriko agents list` - show all agents with status
- [x] `/ruriko agents show <name>` - detailed agent info
- [x] Create `internal/ruriko/store/agents.go` - agent CRUD operations
- [x] Add trace_id generation to all commands (`common/trace/trace.go`)
- [x] Test: Can query empty inventory

### 1.5 Audit Logging
- [x] Audit event writer in `internal/ruriko/store/audit.go`
- [x] Log all commands to `audit_log` table
- [x] Include: timestamp, actor MXID, action, target, trace_id, payload, result
- [x] `/ruriko audit tail [n]` - show recent audit entries
- [x] `/ruriko trace <trace_id>` - show all events for a trace
- [x] Test: All commands appear in audit log

---

## 📋 Phase 2: Secrets Management ✅

**Goal**: Securely store and distribute secrets to agents.

### 2.1 Secrets Store Implementation
- [x] Create `common/crypto/encrypt.go` - AES-GCM encryption helpers
- [x] Create `common/crypto/keystore.go` - master key loading from env (`RURIKO_MASTER_KEY`)
- [x] Create `internal/ruriko/secrets/store.go` - encrypted secret storage
- [x] Implement secret types:
  - [x] `matrix_token` (for agent Matrix accounts)
  - [x] `api_key` (for LLM/tool services)
  - [x] `generic_json` (arbitrary credentials)
- [x] Add rotation versioning support
- [x] Test: Secrets roundtrip (encrypt/decrypt) correctly (encrypt_test.go — 8 tests)

### 2.2 Secrets Commands
- [x] `/ruriko secrets list` - show secret names (not values) and metadata
- [x] `/ruriko secrets set <name>` - store secret (via file attachment or encrypted DM)
- [x] `/ruriko secrets rotate <name>` - increment rotation version
- [x] `/ruriko secrets delete <name>` - remove secret
- [x] `/ruriko secrets info <name>` - show metadata only
- [x] Ensure raw secrets are NEVER printed to Matrix
- [x] Test: Secrets can be stored and retrieved (secrets/store_test.go — 11 tests)

### 2.3 Secret Distribution Model
- [ ] Create `internal/ruriko/secrets/distributor.go` - push updates to agents
- [ ] Implement push model (send encrypted update to agent control endpoint)
- [x] Create `agent_secret_bindings` management
- [x] `/ruriko secrets bind <agent> <secret_name>` - grant agent access
- [x] `/ruriko secrets unbind <agent> <secret_name>` - revoke access
- [ ] `/ruriko secrets push <agent>` - force secret sync
- [x] Test: Secret bindings are tracked correctly

---

## 📋 Phase 3: Agent Lifecycle Control ✅

**Goal**: Create, start, stop, and monitor agent containers.

### 3.1 Runtime Abstraction Layer
- [x] Create `internal/ruriko/runtime/interface.go` - define `Runtime` interface:
  ```go
  type Runtime interface {
      Spawn(spec AgentSpec) (AgentHandle, error)
      Stop(handle AgentHandle) error
      Restart(handle AgentHandle) error
      Status(handle AgentHandle) (RuntimeStatus, error)
      List() ([]AgentHandle, error)
      Remove(handle AgentHandle) error
  }
  ```
- [x] Create `internal/ruriko/runtime/types.go` - `AgentSpec`, `AgentHandle`, `RuntimeStatus`
- [x] Test: Interface compiles

### 3.2 Docker Runtime Adapter
- [x] Create `internal/ruriko/runtime/docker/adapter.go`
- [x] Implement Docker Engine API client (use `github.com/docker/docker/client`)
- [x] Implement `Spawn()` - create and start container with:
  - [x] Agent image (Gitai binary)
  - [x] Environment variables (Matrix creds, control endpoint)
  - [ ] Volume mounts (Gosuto config, SQLite db)
  - [x] Network configuration (`ruriko` bridge network, auto-created)
  - [x] Labels (agent name, template, version)
- [x] Implement `Stop()` - graceful stop with timeout (10s)
- [x] Implement `Restart()` - stop + start
- [x] Implement `Status()` - query container state
- [x] Implement `List()` - enumerate managed agents
- [x] Test: Can spawn/stop/list dummy containers

### 3.3 Agent Control Protocol (ACP) Client
- [x] Create `internal/ruriko/runtime/acp/client.go` - HTTP client for agent control
- [x] Implement ACP endpoints (agent-side will implement these later):
  - [x] `GET /health` - health check
  - [x] `GET /status` - runtime info (version, gosuto hash, MCPs, uptime)
  - [x] `POST /config/apply` - push Gosuto config
  - [x] `POST /secrets/apply` - push secrets
  - [x] `POST /process/restart` - graceful restart
- [ ] Add mTLS or authentication support (optional for MVP)
- [x] Test: Client can make requests (mock server)

### 3.4 Lifecycle Commands
- [x] `/ruriko agents create --template <name> --name <agent_name>` - provision new agent
  - [ ] Load template from `templates/<name>/`
  - [x] Generate agent ID
  - [x] Spawn container via Runtime interface
  - [x] Store in `agents` table
  - [ ] Apply initial Gosuto config via ACP
- [x] `/ruriko agents stop <name>` - stop agent container
- [x] `/ruriko agents start <name>` - start stopped agent
- [x] `/ruriko agents respawn <name>` - stop + start (fresh state)
- [x] `/ruriko agents delete <name>` - remove agent
- [x] `/ruriko agents status <name>` - detailed runtime status
- [x] Test: Full lifecycle works end-to-end

### 3.5 Reconciliation Loop
- [x] Create `internal/ruriko/runtime/reconciler.go` - periodic state sync
- [x] Check agent container status every N seconds (configurable via `RECONCILE_INTERVAL`)
- [x] Update `agents` table with current status
- [x] Detect died agents and update status to ERROR
- [ ] Optional: implement auto-respawn policy
- [x] Alert on unexpected state changes
- [x] Test: Reconciler detects stopped containers (reconciler_test.go — 6 tests)

---

## 📋 Phase 4: Matrix Identity Provisioning

**Goal**: Automatically create Matrix accounts for agents.

### 4.1 Homeserver Admin API Integration
- [x] Create `internal/ruriko/provisioning/matrix.go` - Matrix admin API client
- [x] Implement account creation (Synapse shared-secret API + generic open-registration fallback)
- [x] Generate secure random passwords/tokens
- [x] Store agent `mxid` and `access_token` as secrets
- [ ] Test: Can create Matrix account programmatically

### 4.2 Agent Account Management
- [x] Extend `/ruriko agents create` to accept `--mxid <existing>` flag
- [x] `/ruriko agents matrix register <name>` - provision account for existing agent
- [x] Set agent display name during registration
- [x] Invite agent to required rooms (admin room) via `InviteToRooms`
- [ ] Test: Agent account is created and joins rooms

### 4.3 Deprovisioning
- [x] `/ruriko agents disable <name>` - soft disable agent
  - [x] Stop container if running
  - [x] Kick from rooms via `RemoveFromRooms`
  - [x] Deactivate Matrix account (Synapse admin API; no-op for other types)
  - [x] Mark as disabled in database
  - [x] Remove stored matrix_token secret
- [ ] Test: Agent is removed from rooms

---

## 📋 Phase 5: Gosuto - Versioned Configuration

**Goal**: Apply and version agent policies and personas.

### 5.1 Gosuto Specification
- [x] Create `docs/gosuto-spec.md` - formal specification for Gosuto v1
- [x] Create `common/spec/gosuto/types.go` - Go structs for Gosuto schema:
  - [x] `Config` struct (root type)
  - [x] `Trust` (allowed rooms, senders, E2EE requirements)
  - [x] `Limits` (rate, cost, concurrency)
  - [x] `Capability` rules
  - [x] `Approval` requirements
  - [x] `Persona` (LLM prompt)
- [x] Create `common/spec/gosuto/validate.go` - schema validator
- [x] Test: Valid Gosuto configs parse correctly (validate_test.go — 13 tests)

### 5.2 Template System
- [x] Create `templates/cron-agent/gosuto.yaml` - example cron agent config
- [x] Create `templates/browser-agent/gosuto.yaml` - example browser agent config
- [x] Create `templates/saito-agent/gosuto.yaml` - canonical cron/trigger agent
- [x] Create `templates/kumo-agent/gosuto.yaml` - canonical news/search agent
- [x] Create `internal/ruriko/templates/loader.go` - template registry
- [x] Implement template interpolation (agent name, room IDs, etc.)
- [x] Test: Templates load and validate (loader_test.go — 9 tests)

### 5.3 Gosuto Commands
- [x] `/ruriko gosuto show <agent> [--version <n>]` - display current (or specific) Gosuto config
- [x] `/ruriko gosuto versions <agent>` - list all stored versions
- [x] `/ruriko gosuto diff <agent> --from <v1> --to <v2>` - show config changes
- [x] `/ruriko gosuto set <agent> --content <base64yaml>` - store new version
- [x] `/ruriko gosuto rollback <agent> --to <version>` - revert to previous version
- [x] `/ruriko gosuto push <agent>` - apply current Gosuto to running agent via ACP
- [x] Test: Gosuto versioning works end-to-end (gosuto_test.go — 8 store tests)

### 5.4 Gosuto Storage and Versioning
- [x] Compute SHA-256 hash of Gosuto content
- [x] Store versions in `gosuto_versions` table
- [x] Keep last N versions (configurable; default 20 via `GosutoVersionsRetainN`)
- [x] Track who changed what and when
- [x] Implement version comparison logic
- [x] Test: Rollback works correctly

### Phase 2 Deferred (completed in Phase 5)
- [x] Create `internal/ruriko/secrets/distributor.go` - push updates to agents
- [x] `/ruriko secrets push <agent>` - force secret sync

---

## 📋 Phase 6: Approval Workflow

**Goal**: Require human approval for sensitive operations.

### 6.1 Approval Objects
- [x] Create `internal/ruriko/approvals/types.go` - approval structs:
  - [x] `Approval` (id, action, target, params, requestor, status, created, expires, decisions)
  - [x] `ApprovalDecision` (approver, decision, reason, timestamp)
- [x] Create `internal/ruriko/approvals/store.go` - approval persistence
- [x] Create approval table migration (`store/migrations/0003_approvals.sql`)
- [x] Test: Approvals can be stored and retrieved

### 6.2 Approval Command Parser
- [x] Create `internal/ruriko/approvals/parser.go` - deterministic approval parsing
- [x] Parse commands:
  - [x] `approve <approval_id> [reason]`
  - [x] `deny <approval_id> reason="..."` (reason required)
- [x] Verify sender is in approvers list
- [x] Enforce TTL (expired approvals auto-deny)
- [x] Test: Approval decisions parse correctly

### 6.3 Gated Operations
- [x] Identify operations requiring approval:
  - [x] Agent deletion
  - [x] Secret deletion/rotation (for critical secrets)
  - [x] Gosuto changes (configurable)
  - [x] Gosuto rollback
- [x] Create `internal/ruriko/approvals/gate.go` - approval gating middleware
- [x] When gated operation requested:
  - [x] Generate approval request
  - [x] Return approval ID to requester
  - [x] Block operation until approval received
  - [x] Store approval in database
- [x] Test: Gated operations block until approved

### 6.4 Approval Commands
- [x] `/ruriko approvals list [--status pending|approved|denied]` - list approvals
- [x] `/ruriko approvals show <id>` - detailed approval info
- [x] `approve <id>` - approve operation
- [x] `deny <id> reason="..."` - deny operation
- [x] Test: Full approval workflow works

---

## 📋 Phase 7: Observability and Safety Polish (MOSTLY COMPLETE)

**Goal**: Make Ruriko production-ready with robust logging and monitoring.

### 7.1 Trace Correlation ✅
- [x] Create `common/trace/trace.go` - trace ID generation
- [x] Generate unique `trace_id` for each command/request
- [x] Propagate trace IDs to:
  - [x] Agent control API calls (`X-Trace-ID` header injected by ACP client)
  - [x] Reconciler passes (each reconcile cycle gets its own trace ID)
  - [x] Log statements (trace_id logged in reconciler and ACP callers)
- [x] `/ruriko trace <trace_id>` - show all related events
- [x] Test: Trace correlation works across subsystems

### 7.2 Audit Room Integration ✅
- [x] Add optional audit room configuration (`MATRIX_AUDIT_ROOM` env var)
- [x] Post human-friendly audit messages to room for major events:
  - [x] Agent created/started/stopped/respawned/deleted/disabled
  - [x] Secrets rotated/pushed
  - [x] Approvals requested/granted/denied
- [x] Include trace IDs in messages
- [x] Create `internal/ruriko/audit/notifier.go` - MatrixNotifier + Noop
- [x] Wire notifier into all key handlers (lifecycle, secrets, approvals)
- [x] Test: Audit messages appear in room (notifier_test.go — 3 tests)

### 7.3 Structured Logging ✅
- [x] Implement consistent log levels (debug, info, warn, error)
- [x] Add context to all log statements (trace_id, actor, action)
- [x] Redact secrets from logs (`common/redact/redact.go`)
- [x] Add log filtering by level (`LOG_LEVEL` env var)
- [x] Add log format control (`LOG_FORMAT=json|text` env var)
- [x] Test: Logs are clean and useful (redact_test.go — 5 tests)

### 7.4 Health and Status Endpoints ✅
- [x] Create optional HTTP server for metrics/health (`HTTP_ADDR` env var)
- [x] `/health` - basic health check (version, commit)
- [x] `/status` - Ruriko status (uptime, agent count, version, build time)
- [ ] Optional: Prometheus metrics export
- [x] Test: Status endpoint works (health_test.go — 2 tests)

### 7.5 Error Handling and Recovery ✅
- [x] Implement graceful shutdown (SIGTERM handling in `cmd/ruriko/main.go`)
- [x] Handle Matrix disconnections gracefully (reconnect with exponential backoff)
- [x] Add retry logic for transient failures (`common/retry/retry.go` — applied to ACP calls)
- [x] Test: Ruriko recovers from common error scenarios (retry_test.go — 5 tests)
- [ ] Handle database errors gracefully (deferred)

---

## 📋 Phase 8: Deployment and Documentation ✅

**Goal**: Make it easy to deploy and operate Ruriko.

### 8.1 Docker Image ✅
- [x] Create `deploy/docker/Dockerfile.ruriko`
- [x] Build multi-stage Docker image (build + runtime)
- [x] Support configuring via environment variables
- [x] Create `deploy/docker/entrypoint.sh` script
- [x] Test: Docker image runs correctly

### 8.2 Docker Compose Example ✅
- [x] Create `examples/docker-compose/docker-compose.yaml`
- [x] Include:
  - [x] Ruriko service
  - [x] Example agent (Gitai) - placeholder for now
  - [x] Optional: local Synapse/Dendrite instance
- [x] Create `.env.example` with required configuration
- [x] Test: Docker Compose stack starts (image verified; full Compose test requires live homeserver)

### 8.3 Configuration Documentation ✅
- [x] Document required environment variables
- [x] Document Matrix homeserver setup
- [x] Document admin room creation and configuration
- [x] Document approvals room setup
- [x] Create quickstart guide

### 8.4 Operational Documentation ✅
- [x] Create `docs/ops/deployment-docker.md`
- [x] Create `docs/ops/backup-restore.md` (SQLite backup)
- [x] Document disaster recovery procedures
- [x] Document upgrading Ruriko
- [x] Document common troubleshooting steps

---

## 📋 Phase 9: Gitai Agent Runtime ✅

**Note**: Started after Phase 3 of Ruriko. See RURIKO_COMPONENTS.md for details.

### Completed:
1. ✅ Basic Matrix connection + message handling
2. ✅ Agent Control Protocol (HTTP server)
3. ✅ Gosuto loading and hot-reload
4. ✅ Policy engine and constraints
5. ✅ LLM interface and tool proposal loop
6. ✅ MCP client and supervisor
7. ✅ Approval workflow (agent-side)
8. ✅ Secrets handling
9. ✅ Observability and auditing

---
---

# 🔄 REALIGNMENT PHASES

> The phases below realign the project toward the MVP described in
> [docs/preamble.md](docs/preamble.md) and [REALIGNMENT_PLAN.md](REALIGNMENT_PLAN.md).
>
> The infrastructure built in Phases 0–9 is solid. What's missing is:
> security hardening (ACP auth, Kuze, token-based secrets), the Tuwunel
> switch, and the actual canonical workflow (Saito → Kairo → Kumo).

---

## 📋 Phase R0: Project Hygiene and Config Alignment (0.5–1 day)

**Goal**: Remove config drift and make shared code consistent across binaries.

> Maps to REALIGNMENT_PLAN Phase 0 + Phase 1.

### R0.1 Update Docs to Match Preamble
- [x] Create `docs/preamble.md` (product story + glossary)
- [x] Update `docs/architecture.md` to match preamble terminology (Tuwunel, Kuze, ACP, Gosuto glossary)
- [x] Update `docs/invariants.md` to include "secrets never in Matrix" invariant
- [x] Update `docs/threat-model.md` to reflect single-host MVP topology (federation OFF, registration OFF)
- [x] Update `README.md` to point to preamble and use consistent terminology

### R0.2 Create `common/environment` Package
- [x] Create `common/environment/env.go` with shared helpers:
  - [x] `String(name) (string, bool)`
  - [x] `StringOr(name, default) string`
  - [x] `RequiredString(name) (string, error)`
  - [x] `BoolOr(name, default) bool`
  - [x] `IntOr(name, default) int`
  - [x] `DurationOr(name, default) time.Duration`
- [x] Migrate `cmd/ruriko/main.go` to use `common/environment` + `loadConfig() (Config, error)`
- [x] Migrate `cmd/gitai/main.go` to use `common/environment` + `loadConfig() (Config, error)`
- [x] Remove duplicated `getEnv`/`envOr`/`requireEnv` helpers from both binaries
- [x] Remove `os.Exit` from helper functions (return errors instead)

### R0.3 Decouple Crypto from Environment
- [x] Remove `os.Getenv(RURIKO_MASTER_KEY)` from `common/crypto/keystore.go`
- [x] Change `keystore.go` to accept master key as a parameter: `ParseMasterKey(rawHex string) ([]byte, error)`
- [x] Update `cmd/ruriko/main.go` to load master key in config and pass into keystore
- [x] Update `cmd/gitai/main.go` if it uses keystore
- [x] Test: Crypto module has zero env dependencies

### R0.4 DB Schema Drift Cleanup
- [x] Audit `migrations/ruriko/0001_init.sql` vs actual Go struct fields (`ContainerID`, `ControlURL`, `Image`)
- [x] Create migration or update init migration for missing columns (already exists: `0002_agent_runtime.sql`)
- [x] Remove or repurpose unused `agent_endpoints` table (migration `0004_cleanup_agent_endpoints.sql`)
- [x] Test: Clean `go test ./...` passes

### Definition of done
- Both binaries use `common/environment` for all env access
- Crypto packages are pure libraries with no env coupling
- DB schema matches Go structs without drift
- All tests pass

---

## 📋 Phase R1: Matrix Stack Realignment — Tuwunel Default (1–2 days)

**Goal**: Make Tuwunel the default homeserver. Keep Synapse as an optional path.

> Maps to REALIGNMENT_PLAN Phase 2.

### R1.1 Docker Compose — Tuwunel
- [x] Replace Synapse with Tuwunel container in `examples/docker-compose/docker-compose.yaml`
- [x] Configure Tuwunel with federation disabled
- [x] Configure Tuwunel with registration disabled
- [x] Add persistent volume for Tuwunel data
- [x] Keep Synapse as a commented-out alternative or separate compose override
- [x] Update `.env.example` with Tuwunel-relevant variables
- [ ] Test: `docker compose up -d` boots a working Matrix homeserver

### R1.2 Provisioning — Tuwunel Support
- [x] Add `tuwunel` homeserver type to `internal/ruriko/provisioning/matrix.go`
- [x] Research Tuwunel admin API for account creation (token-based registration documented)
- [x] Set default `HomeserverType` to `tuwunel` (was `synapse`)
- [x] Support registration token flow (`m.login.registration_token`) for account creation
- [x] Document manual account creation steps for MVP
- [x] Keep `synapse` and `generic` paths working as fallbacks
- [x] Update `cmd/ruriko/main.go` to read `TUWUNEL_REGISTRATION_TOKEN` env var
- [ ] Test: Ruriko can log in to Tuwunel homeserver

### R1.3 Documentation
- [x] Update quickstart guide for Tuwunel
- [x] Document how to create Ruriko + agent Matrix accounts on Tuwunel
- [x] Update `docs/ops/deployment-docker.md`

### Definition of done
- `docker compose up -d` boots Tuwunel (not Synapse) with federation OFF, registration OFF
- Ruriko can log in and send messages on Tuwunel
- Agent accounts can be created (manually or via provisioning)
- ✅ Code changes complete; live homeserver test deferred to integration phase

---

## 📋 Phase R2: ACP Hardening — Auth, Idempotency, Timeouts (2–4 days)

**Goal**: Make ACP safe, authenticated, retry-friendly, and private-by-default.

> Maps to REALIGNMENT_PLAN Phase 3.

### R2.1 ACP Authentication
- [x] Choose auth mechanism:
  - Preferred: **mTLS** (mutual TLS with per-agent certs)
  - Fallback: **signed bearer token** (JWT-like, short TTL, agent_id audience)
  - Chosen: **Bearer token** (128-bit random, hex-encoded, per-agent)
- [x] Implement server-side auth middleware in `internal/gitai/control/server.go`
- [x] Implement client-side auth in `internal/ruriko/runtime/acp/client.go`
- [x] Generate/distribute agent credentials during provisioning
- [x] Test: Unauthenticated ACP requests are rejected

### R2.2 Idempotency Headers
- [x] Add `X-Request-ID` header to all ACP requests (unique per call)
- [x] Add `X-Idempotency-Key` header to mutating operations (`/config/apply`, `/process/restart`, `/secrets/apply`)
- [x] Add server middleware: cache responses by idempotency key for a TTL window
- [x] Prevent duplicate restarts / duplicate config applies within the window
- [x] Test: Duplicate requests return cached response

### R2.3 Per-Operation Timeouts
- [x] Remove global `http.Client.Timeout = 10s` from ACP client
- [x] Implement per-operation timeouts using `context.WithTimeout`:
  - [x] Health: 2s
  - [x] Status: 3s
  - [x] ApplyConfig: 30s
  - [x] Restart: 30s
  - [x] SecretsApply: 15s
- [x] Test: Slow operations don't get killed prematurely; fast checks fail fast

### R2.4 Response Safety
- [x] Add `io.LimitReader` to all ACP response body reads (limit: 1MB)
- [x] Include HTTP status text + truncated error body in error messages
- [x] Test: Oversized response bodies don't crash the client

### R2.5 New Endpoints
- [x] Add `POST /tasks/cancel` to Gitai ACP server (cancels current task)
- [x] Add cancel client call to Ruriko ACP client
- [x] Wire into `/ruriko agents cancel <name>` command
- [x] Test: Cancel endpoint works

### Definition of done
- ACP endpoints require authentication
- Ruriko can safely retry commands (idempotent)
- Per-operation timeouts are in effect
- Cancel endpoint exists and works

---

## 📋 Phase R3: Kuze — Human Secret Entry (2–4 days)

**Goal**: Users can add secrets via one-time secure links, never by pasting into Matrix.

> Maps to REALIGNMENT_PLAN Phase 4.

### R3.1 Kuze HTTP Server
- [x] Create `internal/ruriko/kuze/` package
- [x] Embed Kuze HTTP endpoints into Ruriko's existing HTTP server:
  - [x] `POST /kuze/issue/human?secret_ref=<name>` — internal: generate one-time link
  - [x] `GET /s/<token>` — serve HTML form for secret entry
  - [x] `POST /s/<token>` — receive secret value, encrypt+store, burn token
- [x] Implement one-time tokens:
  - [x] Cryptographically random, URL-safe
  - [x] TTL: 5–10 minutes (configurable)
  - [x] Single-use: token is deleted after first use or expiry
  - [x] Scoped to a specific `secret_ref`
- [x] Store pending tokens in SQLite (token, secret_ref, created_at, expires_at, used)
- [x] Create migration for `kuze_tokens` table
- [x] Test: Token generation, HTML form render, secret submission, token burn

### R3.2 Matrix UX Integration
- [x] Implement `/ruriko secrets set <name>` to generate a Kuze link instead of accepting inline values
- [x] Ruriko replies with one-time link: "Use this link to enter the secret: https://…/s/<token>"
- [x] On successful secret storage, Ruriko confirms in Matrix: "✓ Secret '<name>' stored securely."
- [x] On token expiry, Ruriko optionally notifies: "Token for '<name>' expired. Use `/ruriko secrets set <name>` to try again."
- [x] Test: Full flow — command → link → form → store → confirmation

### R3.3 Secret-in-Chat Guardrail
- [x] Add message filter: if an incoming Matrix message looks like a secret (API key pattern, long base64, etc.), refuse to process it
- [x] Reply with: "That looks like a secret. I won't store it from chat. Use: `/ruriko secrets set <name>`"
- [x] Add pattern matching for common secret formats (OpenAI `sk-…`, base64 > 40 chars, etc.)
- [x] Test: Secret-like messages are refused

### R3.4 HTML Form
- [x] Create minimal, self-contained HTML form template (no external dependencies)
- [x] Form displays: secret ref name, single password field, submit button
- [x] On success: "Secret stored. You can close this page."
- [x] On expired/used token: "This link has expired."
- [x] Embed template in Go binary via `embed.FS`
- [x] Test: Form works in a browser (all server-side paths covered by automated tests; visual rendering verified manually)

### Definition of done
- User can store OpenAI/finnhub/brave keys via one-time link
- Secrets never appear in Matrix history
- Expired/used links show appropriate error

---

## 📋 Phase R4: Token-Based Secret Distribution to Agents ✅

**Goal**: Agents fetch secrets on demand via one-time redemption tokens. Secrets never traverse ACP payloads.

> Maps to REALIGNMENT_PLAN Phase 5.

### R4.1 Kuze Agent Redemption Endpoints
- [x] Add to Kuze HTTP server:
  - [x] `POST /kuze/issue/agent` — internal, Ruriko-only: issue token for agent+secret_ref
  - [x] `GET /kuze/redeem/<token>` — agent fetches secret value once, token is burned
- [x] Token scope includes:
  - [x] `agent_id`
  - [x] `secret_ref`
  - [x] `ttl` (short: 30–60 seconds)
  - [x] optional: `task_id` / `purpose`
- [x] Validate agent identity on redemption (match token's `agent_id` against requesting agent)
- [x] Test: Agent can redeem token exactly once; second attempt fails

### R4.2 Replace `/secrets/apply` Push Model
- [x] Add ACP endpoint on Gitai: `POST /secrets/token` or `POST /secrets/lease`
  - Agent receives a list of `{secret_ref, redemption_token, kuze_url}` instead of raw secrets
- [x] Agent redeems each token against Kuze to fetch the actual secret
- [x] Update `internal/ruriko/secrets/distributor.go` to issue tokens via Kuze instead of sending raw secrets
- [x] Test: Secrets flow via token redemption, not raw ACP payload

### R4.3 Agent Secret Manager
- [x] Create `internal/gitai/secrets/manager.go`:
  - [x] In-memory cache with TTL for redeemed secrets
  - [x] `GetSecret(ref string) (string, error)` — returns cached or redeems token
  - [x] Never logs secret values
- [x] Wire secret manager into MCP tool calls (tools that need API keys call `GetSecret`)
  - [x] `{{secret:ref}}` placeholder syntax in tool call arguments resolved at call time via `resolveSecretArgs`
  - [x] `APIKeySecretRef` added to Gosuto `Persona` — LLM provider rebuilt after every secret refresh
  - [x] `rebuildLLMProvider` called from `ApplySecrets` and `ApplyConfig` ACP callbacks
  - [x] Thread-safe provider accessor (`llmProvMu` / `provider()` / `setProvider()`)
- [x] Test: Secret manager caches, respects TTL, never logs values
- [x] Test: `resolveSecretArgs` resolves placeholders, propagates not-found/expired errors, leaves non-string args unchanged
- [x] Test: `rebuildLLMProvider` is no-op with no ref, warns on missing secret, replaces provider when secret available

### R4.4 Deprecate Direct Secret Push
- [x] Add `FEATURE_DIRECT_SECRET_PUSH=false` flag (default OFF)
- [x] If flag is ON, old `/secrets/apply` still works (dev/migration use)
- [x] If flag is OFF (production default), `/secrets/apply` returns 410 Gone
- [x] Add test ensuring direct push is disabled by default
- [ ] Remove old push code path in a later cleanup phase

### Definition of done
- ✅ Agents retrieve secrets only via Kuze redemption tokens (R4.1 + R4.2 complete)
- ✅ Secrets never appear in ACP request/response bodies (production mode) (R4.4 complete — 410 Gone by default)
- ✅ Secret manager caches and provides secrets to tool calls (R4.3 complete)
  - Tool call arguments with `{{secret:ref}}` are resolved at call time via `resolveSecretArgs`
  - LLM provider API key can be sourced from the secret manager via `Persona.APIKeySecretRef`
  - Provider is rebuilt automatically after every `ApplySecrets` / `ApplyConfig` ACP call

---

## 📋 Phase R5: Agent Provisioning UX — Saito, Kairo, Kumo (2–6 days)

**Goal**: Ruriko can provision the canonical agents deterministically. Users request creation via chat.

> Maps to REALIGNMENT_PLAN Phase 6.

### R5.1 Canonical Agent Templates
- [x] Create `templates/saito-agent/gosuto.yaml` — cron/trigger agent:
  - [x] No MCP tools (single `deny-all-tools` capability rule)
  - [x] No LLM reasoning (intentionally deterministic; `temperature: 0.0`)
  - [x] Secret refs: `<agent>.openai-api-key`
- [ ] Create `templates/kairo-agent/gosuto.yaml` — finance agent (**deferred — skipped for now**):
  - [ ] MCP: finnhub, database
  - [ ] Capabilities: query market data, write analysis to DB, report to Ruriko
  - [ ] Persona: financial analyst
  - [ ] Secret refs: `<agent>.finnhub-api-key`
- [x] Create `templates/kumo-agent/gosuto.yaml` — news/search agent:
  - [x] MCP: brave-search + fetch (GET-only, constrained)
  - [x] Capabilities: allow brave-search.\*, allow fetch.fetch (GET), deny all others
  - [x] Persona: research assistant (`gpt-4o`, `temperature: 0.3`)
  - [x] Secret refs: `<agent>.openai-api-key`, `<agent>.brave-api-key`
- [x] Validate saito-agent and kumo-agent templates pass Gosuto schema validation
- [x] Test: Templates load, render, and validate correctly (validate_test.go +2 tests; loader_test.go +5 tests)

### R5.2 Automated Provisioning Pipeline ✅
- [x] Implement sequential provisioning in `/ruriko agents create`:
  1. Create DB record
  2. Create Docker container
  3. Wait for container healthy
  4. Wait for ACP `/health` to respond
  5. Apply Gosuto config via ACP `/config/apply`
  6. Verify ACP `/status` reflects correct config hash
  7. Push secret tokens via ACP `/secrets/token`
- [x] Add provisioning state machine (pending → creating → configuring → healthy → error)
- [x] Post Matrix breadcrumbs at each step:
  - "Provisioned Kairo" / "Applied config hash …" / "Kairo healthy"
- [x] Test: Full provisioning pipeline from template to healthy agent

### R5.3 Agent Registry in Ruriko DB
- [x] Extend `agents` table (or create `agent_desired_state` table):
  - [x] assigned gosuto hash (desired vs actual)
  - [x] enabled/disabled flag
  - [x] last health check timestamp
  - [x] provisioning state (`migration 0008_provisioning_state.sql` — `pending | creating | configuring | healthy | error`)
- [x] Reconciler compares desired state vs actual state and alerts on drift
- [x] Test: Registry tracks desired vs actual state correctly

### R5.4 Chat-Driven Creation ✅
- [x] Handle natural language requests (stretch goal — implemented as deterministic keyword matching, no LLM for control decisions):
  - `/ruriko agents create --template saito --name Saito` (command path, unchanged)
  - Free-form: "set up Saito", "create a news agent called kumo2", "I need a browser agent", etc.
  - Supports saito-agent, kumo-agent, browser-agent, kairo-agent, cron-agent, research-agent
  - Explicit agent naming via "called <name>", "named <name>", "name it <name>"
- [x] Guide user through required secrets if not yet stored:
  - Ruriko detects which required secrets are missing from the store and lists them with set commands
  - "Saito needs saito.openai-api-key. Use `/ruriko secrets set saito.openai-api-key` to add it."
  - User replies **yes** after storing secrets; Ruriko re-checks before provisioning
  - User can reply **no** to cancel at any point
- [x] Conversation state: 5-minute TTL in-memory sessions, keyed per room+sender
- [x] Test: `ParseIntent` — 18 cases covering all templates + naming patterns
- [x] Test: `ParseIntent_NoIntent` — 9 negative cases (no false positives for ordinary chat)
- [x] Test: `conversationStore` — session lifecycle + TTL expiry
- [x] Test: `buildConfirmationPrompt` — all-present and missing-secret variants
- [x] Test: User can create Saito/Kairo/Kumo via natural language; agents appear in Matrix and ACP

### Definition of done
- User can ask Ruriko to create Saito, Kairo, and Kumo
- Ruriko provisions them fully (container + config + secrets)
- Agents appear in Matrix and respond to ACP health checks

---

## 📋 Phase R6: Canonical Workflow — Saito → Kairo → Kumo (3–8 days)

**Goal**: Deliver the reference story end-to-end. Make it feel like "agents collaborating as people."

> Maps to REALIGNMENT_PLAN Phase 7.

### R6.1 Saito Scheduling
- [ ] Saito emits a trigger every N minutes (configurable, default 15)
  - Pre-R12: Saito uses its LLM persona to send periodic Matrix messages (current approach)
  - Post-R12: Saito uses a built-in cron gateway to trigger turns without LLM polling overhead
- [ ] Trigger is sent as a Matrix DM to Kairo (human-readable but structured enough for parsing)
- [ ] Saito is intentionally deterministic: no LLM reasoning, only schedule + notify
- [ ] Saito should handle: start, stop, interval change via Gosuto
- [ ] Test: Saito sends periodic triggers visible in Matrix

### R6.2 Kairo Analysis Pipeline
- [ ] Kairo receives trigger from Saito
- [ ] Kairo checks for portfolio config in DB:
  - If missing, asks Bogdan in Matrix DM for portfolio (tickers, allocations)
  - Stores portfolio in DB for subsequent runs
- [ ] Kairo queries finnhub MCP for market data (prices, changes, fundamentals)
- [ ] Kairo writes analysis to DB (structured: tickers, metrics, commentary)
- [ ] Kairo sends summary report to Ruriko (or to a shared Matrix room)
- [ ] Test: Kairo produces a portfolio analysis from finnhub data

### R6.3 Ruriko Orchestration
- [ ] Ruriko receives Kairo's report
- [ ] Ruriko extracts tickers/topics from the report
- [ ] Ruriko asks Kumo to search for related news
- [ ] Ruriko forwards Kumo's news results back to Kairo for revision
- [ ] Kairo revises analysis with news context
- [ ] Ruriko decides whether to notify Bogdan based on:
  - [ ] Significance threshold (material changes, big news)
  - [ ] Rate limiting (no more than N notifications per hour)
- [ ] If significant: Ruriko sends Bogdan a concise final report
- [ ] If not significant: Ruriko logs but does not notify
- [ ] Test: Full orchestration loop produces a final report

### R6.4 Kumo News Search
- [ ] Kumo receives search request from Ruriko (tickers/company names)
- [ ] Kumo uses Kumo Search MCP to fetch news
- [ ] Kumo summarizes results (structured output + short narrative)
- [ ] Kumo returns results to Ruriko
- [ ] Test: Kumo searches and returns relevant news summaries

### R6.5 End-to-End Story Validation
- [ ] Full cycle test: Saito triggers → Kairo analyzes → Kumo searches → Kairo revises → Bogdan gets report
- [ ] Validate: No secrets visible in any Matrix room
- [ ] Validate: Control operations happen via ACP, not Matrix
- [ ] Validate: Report is coherent, timely, and actionable
- [ ] Validate: User can intervene mid-cycle (e.g., "stop", "skip this one")

### Definition of done
- The full Saito → Kairo → Kumo workflow runs reliably
- The user receives a coherent, useful final report
- No secrets are visible in chat
- Control and orchestration do not depend on Matrix reliability

---

## 📋 Phase R7: Observability, Safety, and Polish (ongoing)

**Goal**: Make the system debuggable, safe for non-technical users, and production-reliable.

> Maps to REALIGNMENT_PLAN Phase 8. Extends earlier Phase 7 work.

### R7.1 Extended Audit Breadcrumbs
- [ ] Post non-sensitive control events to an audit breadcrumbs room:
  - [ ] Agent provisioned/started/stopped
  - [ ] Config applied (hash only)
  - [ ] Secret token issued (ref + TTL, not value)
  - [ ] Orchestration steps (trigger received, analysis started, news fetched, report sent)
- [ ] Test: Audit room has full non-sensitive trace of system activity

### R7.2 Action Gating and Safety
- [ ] No destructive actions without explicit user confirmation
- [ ] No "autonomous trading" or real-money actions in MVP
- [ ] Agent tool calls are bounded by Gosuto capabilities (already implemented)
- [ ] Add circuit breaker: if an agent errors N times in a row, Ruriko pauses it and notifies user
- [ ] Test: Destructive actions require approval; error loop triggers circuit breaker

### R7.3 Rate Limiting
- [ ] Prevent notification spam to Bogdan (configurable: max N reports per hour)
- [ ] Prevent tool API abuse (per-agent call limits in Gosuto)
- [ ] Prevent runaway orchestration loops (max iterations per cycle)
- [ ] Test: Rate limits are enforced

### R7.4 Tool Call Logging (Safe)
- [ ] Log tool name + timing + status for all MCP calls
- [ ] Never log request/response bodies containing secrets
- [ ] Add timing metrics for orchestration cycle (total time, per-step time)
- [ ] Test: Logs are useful for debugging without leaking secrets

### R7.5 Prometheus Metrics (Optional)
- [ ] Export key metrics: agent count, health status, orchestration cycle time, error rate
- [ ] Add `/metrics` endpoint to Ruriko HTTP server
- [ ] Test: Metrics are scrapable

### Definition of done
- System is debuggable via audit trail + logs
- Safe for non-technical users (no surprise actions)
- Rate-limited and bounded

---

## 📋 Phase R8: Integration and End-to-End Testing

**Goal**: Validate the full system works as described in the preamble.

### R8.1 Docker Compose Full Stack Test
- [ ] `docker compose up -d` boots: Tuwunel + Ruriko + (optionally pre-provisioned agents)
- [ ] Ruriko connects to Tuwunel and is responsive
- [ ] User can chat with Ruriko over a Matrix client
- [ ] Test: Stack comes up clean with no manual intervention

### R8.2 Secret Entry Flow
- [ ] User runs `/ruriko secrets set openai_api_key`
- [ ] User receives one-time link, opens in browser, enters key
- [ ] Secret is stored and never appears in Matrix
- [ ] Test: Secret stored via Kuze, verified in encrypted store

### R8.3 Agent Provisioning Flow
- [ ] User runs `/ruriko agents create --template kairo --name Kairo`
- [ ] Ruriko provisions container, applies config, pushes secret tokens
- [ ] Kairo appears in Matrix and responds to ACP health check
- [ ] Test: Full provisioning from command to healthy agent

### R8.4 Canonical Workflow Flow
- [ ] Saito triggers Kairo every 15 minutes
- [ ] Kairo queries finnhub, writes analysis, reports to Ruriko
- [ ] Ruriko asks Kumo for news, forwards to Kairo, sends report to user
- [ ] Test: At least 3 consecutive cycles complete successfully

### R8.5 Failure and Recovery
- [ ] Kill an agent container → reconciler detects → status updates → user notified
- [ ] Matrix disconnection → Ruriko reconnects → resumes operation
- [ ] Expired secret token → agent requests new one → continues working
- [ ] Test: System recovers from each failure scenario

### R8.6 Security Validation
- [ ] Grep all Matrix room history for secret values → none found
- [ ] Grep all ACP request logs for secret values → none found
- [ ] Grep all application logs for secret values → none found
- [ ] Verify ACP rejects unauthenticated requests
- [ ] Test: Security invariants hold

### Definition of done
- Full MVP scenario works end-to-end
- System recovers from failures
- Security invariants are verified

---

## 🎯 MVP Success Criteria (Updated)

The MVP is ready when **all** of the following are true:

✅ **Deployment**: `docker compose up -d` boots Tuwunel + Ruriko on a single host
✅ **Conversation**: User can chat with Ruriko over Matrix — naturally (R9) or via commands
✅ **Secrets**: User stores secrets via Kuze one-time links; secrets never in chat
✅ **Agents**: Ruriko provisions Saito/Kairo/Kumo via ACP with Gosuto config
✅ **ACP**: Authenticated, idempotent, private to Docker network
✅ **Workflow**: Saito triggers Kairo → Kairo analyzes → Kumo searches → report delivered
✅ **Memory**: Ruriko remembers active conversations; recalls relevant past context (R10)
✅ **Security**: No secrets in Matrix history, ACP payloads, or logs

---

## 📋 Phase R9: Natural Language Interface — LLM-Powered Command Translation (3–6 days)

**Goal**: Let users talk to Ruriko naturally instead of memorising `/ruriko` commands. The LLM translates intent into structured commands; all existing security guardrails remain intact.

> Depends on: R0–R5 (command infrastructure, guardrails, approval workflow).
> Independent of: R6 (canonical workflow), R7/R8 (observability, integration).
>
> **Core invariant**: The LLM **proposes** commands; it never **executes** them
> directly. Every mutation flows through the same deterministic pipeline
> (validation → approval gate → audit) as a hand-typed `/ruriko` command.

### R9.0 Design Decisions

The natural-language layer sits between the Matrix message and the command
router. Its sole job is **translation**: convert a free-form sentence into a
structured `Command` (action key + args + flags) that the existing `Router.Dispatch`
can process.

**Security architecture** (why this does not weaken the threat model):

| Existing control                       | Status with NL layer         |
|----------------------------------------|------------------------------|
| Sender allowlist                       | Unchanged — checked *before* NL |
| Secret-in-chat guardrail              | Unchanged — checked *before* NL |
| Internal flag stripping (`--_*`)      | Unchanged — runs on every `Parse` |
| Approval gate (6 gated actions)       | Unchanged — fires on dispatch    |
| Self-approval prevention              | Unchanged                        |
| Agent ID / input validation           | Unchanged — runs inside handlers |
| Audit logging + trace IDs             | Extended — `source: nl` annotation |
| Kuze-only secret entry                | Unchanged — NL never sees values |

**What the LLM sees**: the command catalogue (help text), the list of known
agents, and the list of available templates. It **never** sees secret values,
approval tokens, or internal state.

**What the LLM produces**: a JSON-schema-constrained response:
```json
{
  "intent": "command",
  "action": "agents.create",
  "args": ["saito"],
  "flags": {"template": "saito-agent"},
  "explanation": "Create a new agent named Saito using the saito-agent template.",
  "confidence": 0.95
}
```
or
```json
{
  "intent": "conversational",
  "response": "You currently have 3 agents running. Would you like details on any of them?",
  "read_queries": ["agents.list"]
}
```

### R9.1 LLM Provider Integration for Ruriko

- [x] Create `internal/ruriko/nlp/provider.go` — LLM provider interface:
  ```go
  type Provider interface {
      Classify(ctx context.Context, req ClassifyRequest) (*ClassifyResponse, error)
  }
  ```
- [x] Create `internal/ruriko/nlp/openai.go` — OpenAI-compatible implementation
  - Reuse patterns from `internal/gitai/llm/openai.go` (HTTP client, retry, error handling)
  - Support configurable model (default: `gpt-4o-mini` for cost efficiency)
  - Support configurable endpoint (OpenAI, Azure, local ollama, etc.)
- [x] Add config fields to `app.Config`:
  - `NLPProvider` (optional — when nil, NL falls back to keyword matching)
  - `NLPModel`, `NLPEndpoint`, `NLPAPIKeySecretRef`
- [x] API key loaded from environment (`RURIKO_NLP_API_KEY`), never from chat
- [x] Add per-sender rate limiting on NL classification calls:
  - Configurable: `NLP_RATE_LIMIT` (default: 20 calls/minute per sender)
  - When exceeded: "I'm processing too many requests. Try again in a moment."
- [x] Test: Provider interface is mockable; OpenAI client handles errors
- [x] Test: Rate limiting rejects excessive calls

> **Follow-up (tracked in R9.7):** environment variables are the bootstrap-only
> fallback. Once the stack is running the API key should be stored as the Kuze
> secret `ruriko.nlp-api-key` and loaded at classify-call time (no restart
> required). Model, endpoint, and rate-limit are non-secret configuration values
> managed through `/ruriko config set/get` (also R9.7).

### R9.2 System Prompt and Command Catalogue

- [x] Create `internal/ruriko/nlp/prompt.go` — system prompt builder:
  - Enumerate all registered command handlers with descriptions and argument specs
  - Include list of available templates (from `templates.Registry.List()`)
  - Include list of existing agents (from `store.ListAgents()`) for context
  - Explicit instructions:
    - "You translate user requests into Ruriko commands. You never execute anything."
    - "For mutations (create, delete, stop, config changes), always show the command and ask for confirmation."
    - "For read-only queries (list, show, status, audit), answer directly using query results."
    - "Never generate flags starting with `--_` (these are internal)."
    - "Never include secret values in commands or responses."
    - "If unsure, ask a clarifying question. Do not guess."
- [x] Refresh agent/template context on each call (lightweight — just names and statuses)
- [x] Test: System prompt includes all registered commands
- [x] Test: Prompt explicitly forbids internal flags and secret values

### R9.3 NL Classifier and Intent Router

- [x] Create `internal/ruriko/nlp/classifier.go` — intent classification:
  ```go
  type ClassifyResponse struct {
      Intent       string            // "command" | "conversational" | "unclear"
      Action       string            // handler key, e.g. "agents.create"
      Args         []string
      Flags        map[string]string
      Explanation  string            // human-readable description of what will happen
      Confidence   float64           // 0.0–1.0
      ReadQueries  []string          // for conversational: which read-only handlers to call
      Response     string            // for conversational/unclear: direct text response
  }
  ```
- [x] JSON schema enforcement on LLM output (structured output / function calling)
- [x] Confidence thresholds:
  - `≥ 0.8` → proceed with confirmation (mutations) or direct answer (reads)
  - `0.5–0.8` → "I think you want to [X]. Is that right?"
  - `< 0.5` → "I'm not sure what you'd like. Here are some things I can help with: …"
- [x] Sanitise LLM output: strip any `--_*` flags (defense in depth)
- [x] Validate produced action key exists in `Router` handler map
- [x] Test: Classifier handles all intent types
- [x] Test: Low-confidence responses surface clarification prompts
- [x] Test: Invalid/malicious LLM output is rejected gracefully

### R9.4 Conversation-Aware Dispatch

- [x] Extend `HandleNaturalLanguage` to call LLM classifier when `NLPProvider` is configured:
  - If provider is nil → fall back to existing keyword-based `ParseIntent` (R5.4)
  - If provider is available → call `Classify()` with conversation context
- [x] **Read-only path** (no confirmation needed):
  - Classifier returns `intent: conversational` + `read_queries: ["agents.list"]`
  - NL handler calls `Router.Dispatch` for each read query, collects results
  - LLM summarises results in natural language (second call, or single-shot with function results)
  - Reply sent to Matrix
- [x] **Mutation path** (confirmation required):
  - Classifier returns `intent: command` + structured command
  - NL handler shows user: "I'll run: `/ruriko agents create --name saito --template saito-agent`. Proceed?"
  - Store pending command in `conversationStore` (reuse existing session infra)
  - On "yes" → `Router.Dispatch(ctx, action, cmd, evt)` (same path as approval re-exec)
  - On "no" → cancel session
- [x] **Multi-step mutations** (e.g., "set up Saito and Kumo"):
  - Classifier decomposes into ordered steps
  - Each step requires individual confirmation
  - Steps are NOT batched — user sees and approves each one
- [x] **Approval integration**: if a confirmed mutation hits the approval gate, the
  approval flow proceeds normally — the NL layer does not bypass it
- [x] Audit annotation: all NL-mediated commands include `source: nl` and `llm_intent: <raw>`
  in the audit log payload so the reasoning chain is traceable
- [x] Test: Mutation commands require confirmation before dispatch
- [x] Test: Read-only queries are answered directly
- [x] Test: Multi-step requests are decomposed into individual confirmations
- [x] Test: Approval-gated operations still require approval after NL confirmation
- [x] Test: Audit log records NL source and raw LLM output

### R9.5 Graceful Degradation and Fallbacks

- [x] If LLM provider is unreachable → fall back to keyword-based matching (R5.4)
- [x] If LLM returns malformed output → reply "I didn't quite understand that. You can also use `/ruriko help` for available commands."
- [x] If LLM rate limit is exceeded → reply with rate-limit message + command hint
- [x] `/ruriko` commands always work regardless of NL layer status (additive, not replacing)
- [x] Health endpoint reports NL provider status (`nlp_provider: ok | degraded | unavailable`)
- [x] Test: NL layer degrades gracefully when LLM is down
- [x] Test: Raw commands bypass NL entirely

### R9.6 LLM Cost Controls

- [x] Track token usage per sender per day (in-memory counter, reset at midnight UTC)
- [x] Configurable daily token budget per sender (`NLP_TOKEN_BUDGET`, default: 50k tokens/day)
- [x] When budget exceeded: "I've reached my daily conversation limit. You can still use `/ruriko` commands directly."
- [x] Log token usage in audit trail (input tokens, output tokens, model, latency)
- [x] Test: Token budget enforcement works
- [x] Test: Usage is logged accurately

### R9.7 Runtime Configuration Store — NLP Key, Model, and Tuning Knobs

The env-var approach (R9.1) is a necessary bootstrap mechanism, but it requires
a container restart to rotate a key or switch models. This section replaces it
with a two-tier model:

- **API key** → stored as the Kuze secret `ruriko.nlp-api-key`, loaded lazily
  on each classify call. The env var `RURIKO_NLP_API_KEY` remains as a
  bootstrap-only fallback (useful on first boot before Kuze is set up).
  If both are present the Kuze secret takes precedence and a warning is logged.
- **Non-secret tuning knobs** (model, endpoint, rate-limit) → stored in a new
  lightweight `config` key-value table in SQLite, managed via `/ruriko config`.
  No restart needed; changes take effect on the next classify call via a lazy
  provider rebuild.

**Why not all in Kuze?** Kuze's invariant is "everything stored here is
sensitive". Model names and endpoint URLs are not credentials — mixing them in
blurs that boundary and makes the security audit harder.

**Why not all in env vars?** Env vars require a container restart and are
invisible to the operator at runtime. A DB-backed config table is inspectable
via `/ruriko config get` and auditable.

#### Key/value config store

- [x] Create `internal/ruriko/config/` package:
  ```go
  type Store interface {
      Get(ctx context.Context, key string) (string, error)  // ErrNotFound when absent
      Set(ctx context.Context, key string, value string) error
      Delete(ctx context.Context, key string) error
      List(ctx context.Context) (map[string]string, error)
  }
  ```
- [x] SQLite-backed implementation — new `config` table (key TEXT PRIMARY KEY, value TEXT, updated_at DATETIME)
- [x] Migration: `migrations/ruriko/NNNN_config_store.sql`
- [x] Wire `config.Store` into `app.Config` and `HandlersConfig`
- [x] Test: CRUD operations, concurrent access

#### `/ruriko config` command namespace

- [x] `config.set <key> <value>` — store a config value
  - Allowlist of permitted keys (reject unknown keys to prevent misuse):
    `nlp.model`, `nlp.endpoint`, `nlp.rate-limit`
  - On success: "✓ `nlp.model` set to `gpt-4o`."
- [x] `config.get <key>` — retrieve a config value (or "(not set — using default)")
- [x] `config.list` — show all non-default config values
- [x] `config.unset <key>` — delete a value, reverting to default
- [x] Test: Set/get/unset round-trip; unknown key returns an error

#### NLP API key via Kuze

- [x] Operator runs `/ruriko secrets set ruriko.nlp-api-key` to store the key
  - Follows the standard Kuze flow: one-time browser link, value never in chat
- [x] Provider lookup order on each `Classify` call:
  1. `ruriko.nlp-api-key` secret from the encrypted secrets store (preferred)
  2. `RURIKO_NLP_API_KEY` env var (bootstrap fallback)
  3. Neither present → NL layer stays in keyword-matching mode, no error
- [x] Log a `warn` if both sources are present (helps operators spot stale config)
- [x] Test: Secret takes precedence over env var
- [x] Test: Absent key degrades gracefully to keyword matching

#### Lazy provider rebuild

- [x] `Handlers` holds a `providerCache` (current provider + the config snapshot
  it was built from)
- [x] On each `HandleNaturalLanguage` call, compare current config (key + model +
  endpoint) to the cached snapshot; rebuild the provider if anything changed
- [x] Rebuild is cheap (~zero overhead: just constructs a new `http.Client` wrapper)
- [x] Thread-safe: use a `sync.RWMutex` around the cache
- [x] Test: Provider is rebuilt when model changes; not rebuilt when config is unchanged
- [x] Test: Concurrent calls during a rebuild do not race (run with `-race`)

#### Definition of done (R9.7)
- `/ruriko config set nlp.model gpt-4o` takes effect on the next NL message, no restart
- `/ruriko secrets set ruriko.nlp-api-key` rotates the key without restarting
- Env var `RURIKO_NLP_API_KEY` still works on a fresh deployment before Kuze is configured
- Secret value never appears in audit logs, Matrix history, or command output

### Definition of done
- User can say "show me my agents" and get a natural-language answer
- User can say "create a news agent called kumo2" and Ruriko confirms before creating
- User can say "delete saito" and the approval gate still fires
- `/ruriko` commands still work unchanged
- All NL-mediated actions appear in the audit log with `source: nl`
- LLM is down → keyword matching still works, commands always work
- Operator can rotate the NLP key and change the model without restarting Ruriko

---

## 📋 Phase R10: Conversation Memory — Short-Term / Long-Term Architecture (2–5 days)

**Goal**: Give Ruriko the ability to remember conversations naturally. Short-term memory keeps active discussions coherent; long-term memory lets Ruriko recall relevant past context on demand.

> Depends on: R9 (NL interface — memory feeds context to the LLM classifier).
> The memory layer is **pluggable**: R10 defines the interface and wires stubs
> so that persistence and embedding backends can be swapped in later.

### R10.0 Design Decisions

Humans expect conversation partners to remember what was said. LLMs don't —
they only see what's in the context window. This phase introduces a two-tier
memory model:

**Sharp short-term memory** — the current "contiguous" conversation is kept
whole in the LLM context window. As long as messages flow without significant
delay, Ruriko maintains full conversational fidelity.

**Fuzzy long-term memory** — when a conversation cools down (no message for a
configurable cooldown period), the session is *sealed*, summarised, and stored
with an embedding vector. When a future conversation seems to reference
something from the past, Ruriko searches long-term memory by embedding
similarity and injects the relevant context.

```
┌──────────────────────────────────────────────────────┐
│  Matrix message arrives                               │
│                                                       │
│  1. Resolve active conversation (room + sender)       │
│  2. Is it contiguous? (last msg < cooldown)           │
│     YES → append to short-term buffer                 │
│     NO  → seal previous conversation → store LTM      │
│           start new short-term buffer                 │
│  3. Assemble LLM context:                             │
│     [system prompt]                                   │
│     [retrieved LTM snippets, if relevant]             │
│     [full short-term buffer]                          │
│     [current message]                                 │
│  4. Send to NL classifier (R9)                        │
└──────────────────────────────────────────────────────┘
```

**Why this split matters**:
- Short-term: high fidelity, low cost (only the current exchange)
- Long-term: lossy but cheap (embeddings + summaries, not raw transcripts)
- Context window stays bounded regardless of total conversation history
- Cost scales with *active* conversation length, not *total* history

### R10.1 Conversation Lifecycle and Contiguity Detection

- [ ] Create `internal/ruriko/memory/conversation.go`:
  ```go
  type Conversation struct {
      ID        string           // unique conversation ID (UUID)
      RoomID    string
      SenderID  string
      Messages  []Message        // ordered message buffer
      StartedAt time.Time
      LastMsgAt time.Time
      Sealed    bool             // true once cooldown expires
  }

  type Message struct {
      Role      string           // "user" | "assistant"
      Content   string
      Timestamp time.Time
  }
  ```
- [ ] Create `internal/ruriko/memory/tracker.go` — conversation lifecycle manager:
  - `RecordMessage(roomID, senderID, role, content)` — append or start new conversation
  - `GetActiveConversation(roomID, senderID) *Conversation` — returns current buffer
  - `SealExpired(now time.Time)` — seals conversations past cooldown
- [ ] Contiguity detection:
  - Configurable cooldown period (`MEMORY_COOLDOWN`, default: 15 minutes)
  - If `now - lastMsgAt > cooldown` → seal previous conversation, start fresh
  - Sealed conversations are handed to the long-term memory pipeline
- [ ] Short-term buffer size limit:
  - Configurable max messages per conversation (`MEMORY_STM_MAX_MESSAGES`, default: 50)
  - Configurable max token estimate (`MEMORY_STM_MAX_TOKENS`, default: 8000)
  - When exceeded: oldest messages are dropped from the buffer (sliding window),
    and a summary of dropped messages is prepended (when LTM summariser is available)
- [ ] In-memory storage initially (same pattern as `conversationStore` from R5.4)
- [ ] Test: Contiguous messages accumulate in the same conversation
- [ ] Test: Cooldown gap triggers seal + new conversation
- [ ] Test: Buffer size limits are enforced

### R10.2 Long-Term Memory Interface (Pluggable)

- [ ] Create `internal/ruriko/memory/ltm.go` — long-term memory interface:
  ```go
  type LongTermMemory interface {
      // Store persists a sealed conversation with its embedding and summary.
      Store(ctx context.Context, entry MemoryEntry) error
      // Search finds the top-k most relevant past conversations for the query.
      Search(ctx context.Context, query string, roomID string, senderID string, topK int) ([]MemoryEntry, error)
  }

  type MemoryEntry struct {
      ConversationID string
      RoomID         string
      SenderID       string
      Summary        string    // human-readable summary of the conversation
      Embedding      []float32 // vector embedding of the summary
      Messages       []Message // optional: full transcript for high-fidelity recall
      SealedAt       time.Time
      Metadata       map[string]string // template used, agents mentioned, etc.
  }
  ```
- [ ] Create `internal/ruriko/memory/ltm_noop.go` — no-op stub implementation:
  - `Store()` → logs and discards (conversation summary logged at DEBUG)
  - `Search()` → returns empty slice
  - This is the **default** until an embedding backend is wired
- [ ] Test: Noop implementation satisfies interface
- [ ] Test: Interface is mockable for downstream tests

### R10.3 Embedding and Summarisation Interface (Pluggable)

- [ ] Create `internal/ruriko/memory/embedder.go`:
  ```go
  type Embedder interface {
      // Embed produces a vector embedding for the given text.
      Embed(ctx context.Context, text string) ([]float32, error)
  }

  type Summariser interface {
      // Summarise produces a concise summary of a conversation transcript.
      Summarise(ctx context.Context, messages []Message) (string, error)
  }
  ```
- [ ] Create `internal/ruriko/memory/embedder_noop.go` — stub implementations:
  - `Embed()` → returns nil vector (disables similarity search)
  - `Summarise()` → returns concatenation of last 3 messages (crude but functional)
- [ ] Future implementations (not in this phase, but the interface supports them):
  - `embedder_openai.go` — OpenAI `text-embedding-3-small` (cheap, 1536-dim)
  - `summariser_llm.go` — LLM-based summarisation via same provider as R9
  - `ltm_sqlite.go` — SQLite-backed storage with cosine similarity via an extension
  - `ltm_pgvector.go` — PostgreSQL + pgvector for production-scale deployments
- [ ] Test: Noop embedder and summariser satisfy interfaces
- [ ] Test: Summariser stub produces reasonable output from sample messages

### R10.4 Memory-Aware Context Assembly

- [ ] Create `internal/ruriko/memory/context.go` — context assembler:
  ```go
  type ContextAssembler struct {
      STM       *ConversationTracker
      LTM       LongTermMemory
      Embedder  Embedder
      MaxTokens int // total budget for memory in the LLM context window
  }

  // Assemble produces the memory block to inject into the LLM prompt.
  func (a *ContextAssembler) Assemble(ctx context.Context, roomID, senderID, currentMsg string) ([]Message, error)
  ```
- [ ] Assembly strategy:
  1. Get active short-term conversation → include all messages (sharp recall)
  2. If `Embedder` is available and non-noop:
     - Embed `currentMsg`
     - Search LTM for top-3 relevant past conversations (same room+sender)
     - Inject retrieved summaries as `[system]` context: "Previous relevant conversation (from [date]): [summary]"
  3. If `Embedder` is noop → skip LTM retrieval (no embedding = no search)
  4. Respect `MaxTokens` budget: short-term has priority, LTM fills remaining space
- [ ] Wire `ContextAssembler` into R9's `HandleNaturalLanguage`:
  - Before calling `Classify()`, call `Assemble()` to get conversation history
  - Pass assembled messages as context to the LLM provider
  - After getting the LLM response, call `RecordMessage(role: "assistant", content: response)`
- [ ] Test: Context includes full STM buffer
- [ ] Test: Context includes LTM results when embedder is available
- [ ] Test: Token budget is respected (STM prioritised over LTM)
- [ ] Test: Noop embedder means no LTM retrieval (graceful)

### R10.5 Conversation Seal and Archive Pipeline

- [ ] On conversation seal (cooldown expired):
  1. Call `Summariser.Summarise(messages)` → summary text
  2. Call `Embedder.Embed(summary)` → embedding vector
  3. Call `LTM.Store(MemoryEntry{...})` → persist
  4. Clear the short-term buffer for that room+sender
- [ ] Run seal check on a timer (every 60 seconds) or lazily on next message arrival
- [ ] Log sealed conversations at INFO: "Conversation sealed (room=…, sender=…, messages=N, duration=…)"
- [ ] Never log message *content* at INFO — only at DEBUG and only when redacted
- [ ] Test: Sealed conversation flows through summarise → embed → store pipeline
- [ ] Test: Noop backends handle the pipeline without errors

### R10.6 Configuration and Wiring

- [ ] Add config fields to `app.Config`:
  - `MemoryCooldown` (duration, default: 15 min)
  - `MemorySTMMaxMessages` (int, default: 50)
  - `MemorySTMMaxTokens` (int, default: 8000)
  - `MemoryLTMTopK` (int, default: 3)
  - `MemoryEnabled` (bool, default: true when NLP provider is configured)
- [ ] Wire in `app.New()`:
  - Create `ConversationTracker` (always, when NLP is enabled)
  - Create `LongTermMemory` (noop stub by default)
  - Create `Embedder` + `Summariser` (noop stubs by default)
  - Create `ContextAssembler` → inject into `HandlersConfig`
- [ ] Add `HandlersConfig.Memory *memory.ContextAssembler` field
- [ ] Test: App starts cleanly with noop memory backends
- [ ] Test: App starts cleanly with memory disabled (nil assembler)

### R10.7 Future: Persistent Backends (stubs only in this phase)

> These items are **documented but not implemented** in R10. The interfaces
> from R10.2 and R10.3 are designed to accommodate them.

- [ ] `ltm_sqlite.go` — SQLite with JSON1 and a custom cosine-similarity function
  - Conversations table: id, room_id, sender_id, summary, embedding (BLOB), sealed_at, metadata
  - Search: brute-force cosine similarity (fine for hundreds of conversations)
- [ ] `embedder_openai.go` — calls OpenAI Embeddings API (`text-embedding-3-small`)
  - Same API key as R9 NLP provider (or separate `RURIKO_EMBEDDING_API_KEY`)
  - 1536-dimensional vectors, ~$0.02 per 1M tokens
- [ ] `summariser_llm.go` — uses R9's LLM provider to summarise sealed conversations
  - System prompt: "Summarise this conversation in 2–3 sentences, focusing on decisions made and actions taken."
- [ ] `ltm_pgvector.go` — PostgreSQL + pgvector (for larger deployments)

### Definition of done
- Active conversations are tracked per room+sender with contiguity detection
- Short-term memory is included in every NL classifier call (full buffer)
- Long-term memory interface exists with a noop stub
- Cooldown triggers conversation seal → summarise → embed → store pipeline (noop endpoints)
- All interfaces are pluggable — swapping SQLite/pgvector/OpenAI embeddings requires no structural changes
- System works end-to-end with noop backends (no external dependencies required)
- Memory is disabled gracefully when NLP provider is not configured

---

## 📋 Phase R11: Event Gateways — Gosuto Schema, Types, and Validation (1–2 days)

**Goal**: Extend the Gosuto specification and Go types to support inbound event gateways. No runtime changes yet — this phase is pure schema, validation, and documentation.

> **Context**: Event gateways are the inbound complement to MCPs. Where MCPs let
> agents call outbound tools, gateways let external events (cron ticks, emails,
> webhooks, social media) trigger agent turns. Gateways POST a normalised event
> envelope to the agent's local ACP endpoint (`POST /events/{source}`). This is
> the same principle as MCPs — supervised processes, Gosuto-configured, credential-
> managed via Kuze — but for inbound event ingress instead of outbound tool access.
>
> See [docs/gosuto-spec.md](docs/gosuto-spec.md) for the full specification.

### R11.1 Gosuto Types Extension
- [ ] Add `Gateway` struct to `common/spec/gosuto/types.go`:
  ```go
  type Gateway struct {
      Name        string            `yaml:"name" json:"name"`
      Type        string            `yaml:"type,omitempty" json:"type,omitempty"`           // "cron" | "webhook" (built-in)
      Command     string            `yaml:"command,omitempty" json:"command,omitempty"`     // external gateway binary
      Args        []string          `yaml:"args,omitempty" json:"args,omitempty"`
      Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
      Config      map[string]string `yaml:"config,omitempty" json:"config,omitempty"`
      AutoRestart bool              `yaml:"autoRestart,omitempty" json:"autoRestart,omitempty"`
  }
  ```
- [ ] Add `Gateways []Gateway` field to `Config` struct (between `MCPs` and `Secrets`)
- [ ] Add `MaxEventsPerMinute int` field to `Limits` struct
- [ ] Test: YAML round-trip — gateway configs marshal and unmarshal correctly

### R11.2 Gosuto Validation Extension
- [ ] Add `validateGateway(g Gateway) error` to `common/spec/gosuto/validate.go`:
  - Name must not be empty
  - Exactly one of `type` or `command` must be set (not both, not neither)
  - If `type` is set: must be `"cron"` or `"webhook"` (known built-in types)
  - If `type` is `"cron"`: `config["expression"]` must be present and non-empty
  - If `type` is `"webhook"` and `config["authType"]` is `"hmac-sha256"`: `config["hmacSecretRef"]` must be present
  - If `command` is set: must not be empty string
  - Names must be unique across all gateways (no duplicates)
  - Names must not collide with MCP server names (they share the supervisor namespace)
- [ ] Wire `validateGateway` into `Validate()` loop (same pattern as MCPs)
- [ ] Validate `MaxEventsPerMinute >= 0` in `validateLimits`
- [ ] Test: Valid gateway configs pass validation (cron, webhook, external)
- [ ] Test: Invalid configs fail — missing name, both type+command, unknown type, missing cron expression, duplicate names, MCP name collision

### R11.3 Event Envelope Types
- [ ] Create `common/spec/envelope/event.go` (or extend existing envelope package):
  ```go
  type Event struct {
      Source   string          `json:"source"`
      Type     string          `json:"type"`
      TS       time.Time       `json:"ts"`
      Payload  EventPayload    `json:"payload"`
  }

  type EventPayload struct {
      Message string                 `json:"message"`           // human-readable, goes to LLM
      Data    map[string]interface{} `json:"data,omitempty"`    // structured metadata
  }
  ```
- [ ] Add validation: `Source` must not be empty, `Type` must not be empty, `TS` must not be zero
- [ ] Test: Event envelope marshals/unmarshals correctly
- [ ] Test: Invalid envelopes (missing source, missing type) are rejected

### R11.4 Update Existing Templates
- [ ] Update `templates/saito-agent/gosuto.yaml` to use a built-in cron gateway instead of relying on LLM-based periodic messaging:
  ```yaml
  gateways:
    - name: scheduler
      type: cron
      config:
        expression: "*/15 * * * *"
        payload: "Trigger scheduled check for all coordinated agents"
  ```
  - Saito keeps its LLM persona (for coordination reasoning) but is now *woken* by the cron gateway rather than running its own timer
- [ ] Verify all existing templates still pass validation after schema changes
- [ ] Test: Updated Saito template validates correctly

### Definition of done
- Gosuto types include `Gateway` struct and `Gateways` field
- Gosuto validation rejects invalid gateway configurations
- Event envelope type is defined with validation
- Existing templates updated and validated
- No runtime changes yet — this is schema-only

---

## 📋 Phase R12: Event Gateways — Gitai Runtime Integration (3–6 days)

**Goal**: Gitai agents can receive and process inbound events from gateways. The turn engine handles events alongside Matrix messages.

> Depends on: R11 (schema + types), Phase 9 (Gitai runtime).

### R12.1 ACP Event Ingress Endpoint
- [ ] Add `POST /events/{source}` endpoint to `internal/gitai/control/server.go`:
  - Accepts an `Event` envelope (JSON body)
  - Validates envelope structure (source, type, ts)
  - Validates `{source}` matches a configured gateway name in the active Gosuto
  - Authenticates: built-in gateways bypass auth (localhost origin); external gateways and webhook deliveries use ACP bearer token or HMAC
  - Passes validated event to the app's event handler
  - Returns 202 Accepted (event queued) or 429 Too Many Requests (rate limit exceeded)
- [ ] Add rate limiter: token-bucket per gateway + global `maxEventsPerMinute`
- [ ] Test: Valid events are accepted and forwarded to handler
- [ ] Test: Unknown source names are rejected (404)
- [ ] Test: Malformed envelopes are rejected (400)
- [ ] Test: Rate limiter drops excess events (429)
- [ ] Test: Unauthenticated requests are rejected

### R12.2 Event-to-Turn Bridge in App
- [ ] Add `handleEvent(ctx context.Context, evt Event)` method to `internal/gitai/app/app.go`:
  - Generates trace ID for the event turn
  - Constructs LLM messages:
    - System prompt (from Gosuto persona, unchanged)
    - User message: `evt.Payload.Message` (or a formatted version of the event for events without a message)
  - Calls the same `runTurn` pipeline as `handleMessage`
  - Posts the response to the agent's admin room (Matrix) or a configured output room
  - Logs the turn (source=gateway, gateway_name, event_type)
- [ ] If `Payload.Message` is empty, auto-generate a prompt from structured data:
  - `"Event received from {source} (type: {type}). Data: {json(data)}"`
- [ ] Wire `handleEvent` into the ACP server's `/events/{source}` handler
- [ ] Test: Cron event triggers a full LLM turn
- [ ] Test: Event response is posted to the admin room
- [ ] Test: Event turn is logged with gateway metadata
- [ ] Test: Event without message field auto-generates a prompt

### R12.3 Built-in Cron Gateway
- [ ] Create `internal/gitai/gateway/cron.go`:
  - Parses 5-field cron expression from `config["expression"]`
  - Runs as a goroutine within the Gitai process
  - On each tick: constructs an `Event{Source: name, Type: "cron.tick", TS: now, Payload: {Message: config["payload"]}}` and POSTs it to `localhost:<acp_port>/events/{name}`
  - Respects context cancellation for clean shutdown
- [ ] Use a lightweight cron parser (e.g. `robfig/cron/v3` or a minimal custom parser)
- [ ] Reconcile on Gosuto config change: stop old cron, start new one with updated expression
- [ ] Test: Cron fires at correct intervals (accelerated clock in tests)
- [ ] Test: Cron stops cleanly on shutdown
- [ ] Test: Cron reconfigures on Gosuto update

### R12.4 Built-in Webhook Gateway
- [ ] Add configurable webhook sub-routes to the ACP server:
  - When a gateway has `type: webhook`, expose its path (default `/events/{name}`, or `config["path"]`)
  - Auth: `bearer` (default, uses ACP token) or `hmac-sha256` (validates `X-Hub-Signature-256` header against `hmacSecretRef`)
  - Parse incoming POST body as the `payload.data` field of the event envelope
  - Auto-generate `payload.message` from the webhook body (configurable template, or JSON summary)
- [ ] Test: Webhook with bearer auth accepts valid token
- [ ] Test: Webhook with HMAC auth validates signature
- [ ] Test: Webhook without valid auth is rejected

### R12.5 External Gateway Supervisor
- [ ] Extend `internal/gitai/supervisor/supervisor.go` (or create `internal/gitai/gateway/supervisor.go`) to also manage gateway processes:
  - Start gateway binaries with: command, args, env (from Gosuto + injected secrets)
  - Inject `GATEWAY_TARGET_URL=http://localhost:{acp_port}/events/{name}` environment variable
  - Inject `GATEWAY_*` prefixed config entries as environment variables
  - Monitor process health, restart on crash (when `autoRestart` is true)
  - Stop all gateways on agent shutdown
- [ ] Reconcile gateway processes on Gosuto config change (same pattern as MCP reconciliation):
  - Stop gateways no longer in config
  - Start newly added gateways
  - Restart gateways whose config changed
- [ ] Test: External gateway process starts and receives correct environment
- [ ] Test: Gateway process restarts on crash (when autoRestart=true)
- [ ] Test: Gateway processes are stopped on shutdown
- [ ] Test: Reconcile adds/removes gateway processes correctly

### R12.6 Event-to-Matrix Bridging
- [ ] When an event triggers a turn, post the response to Matrix for observability:
  - Use the agent's admin room (from `trust.adminRoom`) by default
  - Format: breadcrumb header ("⚡ Event: {source}/{type}") + LLM response
  - Never include raw event payloads that might contain sensitive data — only the LLM's processed response
- [ ] If the event references other agents (e.g. a coordination trigger), the agent sends messages to those agents' Matrix rooms as it normally would
- [ ] Test: Event-triggered responses appear in the admin room
- [ ] Test: Sensitive event data is not leaked to Matrix

### R12.7 Observability and Auditing
- [ ] Log all gateway events at INFO level:
  - `"event received"` — source, type, timestamp (never log payload content at INFO)
  - `"event processed"` — source, type, duration, tool_calls, status
  - `"event dropped"` — source, type, reason (rate limit, unknown source, etc.)
- [ ] Include gateway metadata in turn audit records:
  - `trigger: "gateway"`, `gateway_name: "..."`, `event_type: "..."`
- [ ] Distinguish gateway turns from Matrix turns in the store's turn log
- [ ] Test: Audit records include gateway metadata

### Definition of done
- Agents can receive events via `POST /events/{source}` and process them through the LLM turn engine
- Built-in cron gateway fires on schedule and triggers turns without LLM polling overhead
- Built-in webhook gateway accepts authenticated HTTP deliveries
- External gateway processes are supervised alongside MCP processes
- Event responses are posted to Matrix for observability
- Rate limiting prevents event flooding
- All event turns are auditable with source attribution

---

## 📋 Phase R13: Ruriko-Side Gateway Wiring (2–4 days)

**Goal**: Ruriko forwards internet-facing webhooks to agents, and the provisioning pipeline handles gateway-bearing Gosuto configs.

> Depends on: R12 (Gitai gateway runtime), R5 (provisioning).

### R13.1 Webhook Reverse Proxy
- [ ] Add `POST /webhooks/{agent}/{source}` endpoint to Ruriko's HTTP server:
  - Validate `{agent}` exists and is healthy
  - Validate `{source}` matches a gateway with `type: webhook` in the agent's active Gosuto
  - Forward the request body to the agent's ACP `POST /events/{source}` endpoint
  - Authenticate the inbound webhook (HMAC signature or shared secret, per gateway config)
  - Return the agent's response status to the webhook sender
- [ ] Rate limit inbound webhooks per agent (configurable, default: 60/minute)
- [ ] Log webhook forwarding in audit trail (source, agent, status — never payload content)
- [ ] Test: Webhook reaches agent via Ruriko proxy
- [ ] Test: Unknown agent or source returns 404
- [ ] Test: Rate limiting is enforced
- [ ] Test: Invalid HMAC signature is rejected

### R13.2 Provisioning Pipeline — Gateway Awareness
- [ ] Update provisioning pipeline (R5.2) to handle gateway-bearing Gosuto configs:
  - After applying Gosuto via ACP, verify that gateway processes are running via `/status`
  - Include gateway process status in the health/status reporting
  - If a gateway references secrets (e.g. IMAP credentials, webhook HMAC secret), push those secret tokens alongside other secrets during provisioning
- [ ] Update `agents status` command to show active gateways alongside MCPs
- [ ] Test: Provisioning a gateway-bearing Gosuto results in running gateway processes

### R13.3 Container Image Building — Gateway Binaries
- [ ] Document the pattern for including gateway binaries in agent container images:
  - Same approach as MCPs: gateway binaries are baked into the Gitai Docker image at build time
  - `Dockerfile.gitai` copies vetted gateway binaries alongside MCP binaries
  - Gateway binaries are listed in a vetted manifest (same vetting process as MCPs)
- [ ] Add example gateway binary to Docker build (e.g. `ruriko-gw-imap` placeholder)
- [ ] Update `deploy/docker/Dockerfile.gitai` with gateway binary layer
- [ ] Test: Built image contains gateway binaries at expected paths

### R13.4 Documentation
- [ ] Update `docs/architecture.md` with gateway architecture diagram
- [ ] Update `docs/threat-model.md` with new attack surface analysis:
  - Gateway process surface (same mitigations as MCP: vetted, supervised, sandboxed)
  - Webhook endpoint surface (authentication, rate limiting, Ruriko proxy)
  - Event payload injection (untrusted input → policy engine, same as Matrix messages)
- [ ] Update `README.md` to mention event gateways in the architecture overview
- [ ] Add gateway template examples (cron-triggered agent, email-reactive agent)

### Definition of done
- Ruriko proxies internet webhooks to agents securely
- Provisioning handles gateway-bearing configs end-to-end
- Container images include vetted gateway binaries
- Documentation covers architecture, security, and usage

---

## 🚀 Post-MVP Roadmap (explicitly not required now)

- [ ] Reverse RPC broker (agents behind NAT without inbound connectivity)
- [ ] Appservice-based Matrix provisioning (cleaner agent account lifecycle)
- [ ] Fine-grained policy engine (per-secret/per-tool/per-task permissions)
- [ ] Multi-tenant support
- [ ] Web UI in addition to Matrix
- [ ] E2EE for Matrix communication
- [ ] Kubernetes runtime adapter
- [ ] Codex integration (template generation)
- [ ] Advanced MCP tool ecosystem
- [ ] Enhanced observability (distributed tracing, Prometheus)
- [ ] Persistent LTM backends (SQLite cosine similarity, pgvector)
- [ ] OpenAI Embeddings integration for long-term memory search
- [ ] LLM-powered conversation summarisation for memory archival
- [ ] Multi-user memory isolation and per-room memory scoping
- [ ] Voice-to-text Matrix messages → NL pipeline
- [ ] Additional gateway binaries (MQTT, RSS poller, Mastodon streaming, Slack events)
- [ ] Gateway marketplace / vetted registry for community-contributed gateways

---

## 📝 Notes

- **Ship the canonical story**: Saito → Kairo → Kumo is the north star
- **Security by default**: Secrets never in chat, ACP always authenticated
- **Conversation-first**: Everything important should be explainable in chat
- **Non-technical friendly**: Setup must not require engineering expertise
- **Boring control plane**: ACP is reliable, authenticated, idempotent
- **Fail safely**: Better to refuse an action than execute it incorrectly
- **LLM translates, code decides**: The NL layer proposes commands; the deterministic pipeline executes them
- **Memory is bounded**: Short-term is sharp; long-term is fuzzy; context window stays predictable
- **Graceful degradation**: LLM down → keyword matching; memory down → no recall; commands always work
- **Three ingress patterns, one turn engine**: Matrix messages, webhook events, and gateway events all feed into the same policy → LLM → tool call pipeline
- **MCPs for outbound, gateways for inbound**: Symmetric supervised-process model, same credential management, same Gosuto configuration
- **Document as you go**: Keep preamble and architecture docs up to date

---

## 🔄 Status Tracking

### Infrastructure Phases (completed before realignment)

- [x] Phase 0: Project Setup & Foundations ✅
- [x] Phase 1: Ruriko MVP — Matrix Control + Inventory ✅
- [x] Phase 2: Secrets Management ✅
- [x] Phase 3: Agent Lifecycle Control ✅
- [x] Phase 4: Matrix Identity Provisioning ✅
- [x] Phase 5: Gosuto — Versioned Configuration ✅
- [x] Phase 6: Approval Workflow ✅
- [x] Phase 7: Observability and Safety Polish ✅ (mostly — Prometheus deferred)
- [x] Phase 8: Deployment and Documentation ✅
- [x] Phase 9: Gitai Agent Runtime ✅

### Realignment Phases

- [x] Phase R0: Project Hygiene and Config Alignment ✅
- [x] Phase R1: Matrix Stack Realignment — Tuwunel Default ✅
- [x] Phase R2: ACP Hardening — Auth, Idempotency, Timeouts ✅
- [x] Phase R3: Kuze — Human Secret Entry ✅
- [x] Phase R4: Token-Based Secret Distribution to Agents ✅
- [ ] Phase R5: Agent Provisioning UX — Saito, Kairo, Kumo 🔄 *(R5.1 partial: saito-agent ✅ + kumo-agent ✅; kairo-agent deferred; R5.2 ✅; R5.3 ✅; R5.4 ✅)*
- [ ] Phase R6: Canonical Workflow — Saito → Kairo → Kumo
- [ ] Phase R7: Observability, Safety, and Polish
- [ ] Phase R8: Integration and End-to-End Testing
- [ ] Phase R9: Natural Language Interface — LLM-Powered Command Translation
- [ ] Phase R10: Conversation Memory — Short-Term / Long-Term Architecture
- [ ] Phase R11: Event Gateways — Gosuto Schema, Types, and Validation
- [ ] Phase R12: Event Gateways — Gitai Runtime Integration
- [ ] Phase R13: Ruriko-Side Gateway Wiring

---

**Last Updated**: 2026-02-22
**Current Focus**: Phase R9 — Natural Language Interface (design complete; R5.4 keyword-based NL ✅; LLM-powered translation next)

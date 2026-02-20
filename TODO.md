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
- Ruriko can provision Tim/Warren/Brave agents and apply Gosuto config via ACP
- ACP is authenticated and idempotent
- Tim triggers Warren every 15 minutes
- Warren fetches data from finnhub and stores results in DB
- Brave fetches news for relevant tickers
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
- [x] Test: Valid Gosuto configs parse correctly (validate_test.go — 11 tests)

### 5.2 Template System
- [x] Create `templates/cron-agent/gosuto.yaml` - example cron agent config
- [x] Create `templates/browser-agent/gosuto.yaml` - example browser agent config
- [x] Create `internal/ruriko/templates/loader.go` - template registry
- [x] Implement template interpolation (agent name, room IDs, etc.)
- [x] Test: Templates load and validate (loader_test.go — 4 tests)

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
> switch, and the actual canonical workflow (Tim → Warren → Brave).

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

## 📋 Phase R4: Token-Based Secret Distribution to Agents (3–6 days)

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
- [ ] Add ACP endpoint on Gitai: `POST /secrets/token` or `POST /secrets/lease`
  - Agent receives a list of `{secret_ref, redemption_token, kuze_url}` instead of raw secrets
- [ ] Agent redeems each token against Kuze to fetch the actual secret
- [ ] Update `internal/ruriko/secrets/distributor.go` to issue tokens via Kuze instead of sending raw secrets
- [ ] Test: Secrets flow via token redemption, not raw ACP payload

### R4.3 Agent Secret Manager
- [ ] Create `internal/gitai/secrets/manager.go`:
  - [ ] In-memory cache with TTL for redeemed secrets
  - [ ] `GetSecret(ref string) (string, error)` — returns cached or redeems token
  - [ ] Never logs secret values
- [ ] Wire secret manager into MCP tool calls (tools that need API keys call `GetSecret`)
- [ ] Test: Secret manager caches, respects TTL, never logs values

### R4.4 Deprecate Direct Secret Push
- [ ] Add `FEATURE_DIRECT_SECRET_PUSH=false` flag (default OFF)
- [ ] If flag is ON, old `/secrets/apply` still works (dev/migration use)
- [ ] If flag is OFF (production default), `/secrets/apply` returns 410 Gone
- [ ] Add test ensuring direct push is disabled by default
- [ ] Remove old push code path in a later cleanup phase

### Definition of done
- Agents retrieve secrets only via Kuze redemption tokens
- Secrets never appear in ACP request/response bodies (production mode)
- Secret manager caches and provides secrets to tool calls

---

## 📋 Phase R5: Agent Provisioning UX — Tim, Warren, Brave (2–6 days)

**Goal**: Ruriko can provision the canonical agents deterministically. Users request creation via chat.

> Maps to REALIGNMENT_PLAN Phase 6.

### R5.1 Canonical Agent Templates
- [ ] Create `templates/tim/gosuto.yaml` — cron/trigger agent:
  - [ ] Schedule: configurable interval (default 15 min)
  - [ ] Capabilities: send Matrix DM, trigger other agents
  - [ ] No tools, no LLM reasoning (intentionally simple)
- [ ] Create `templates/warren/gosuto.yaml` — finance agent:
  - [ ] MCP: finnhub, database
  - [ ] Capabilities: query market data, write analysis to DB, report to Ruriko
  - [ ] Persona: financial analyst
  - [ ] Secret refs: `finnhub_api_key`
- [ ] Create `templates/brave/gosuto.yaml` — news/search agent:
  - [ ] MCP: brave-search
  - [ ] Capabilities: search news, summarize, return structured output
  - [ ] Persona: research assistant
  - [ ] Secret refs: `brave_api_key`
- [ ] Validate all templates pass Gosuto schema validation
- [ ] Test: Templates load and validate correctly

### R5.2 Automated Provisioning Pipeline
- [ ] Implement sequential provisioning in `/ruriko agents create`:
  1. Create DB record
  2. Create Docker container
  3. Wait for container healthy
  4. Wait for ACP `/health` to respond
  5. Apply Gosuto config via ACP `/config/apply`
  6. Verify ACP `/status` reflects correct config hash
  7. Push secret tokens via ACP `/secrets/token`
- [ ] Add provisioning state machine (pending → creating → configuring → healthy → error)
- [ ] Post Matrix breadcrumbs at each step:
  - "Provisioned Warren" / "Applied config hash …" / "Warren healthy"
- [ ] Test: Full provisioning pipeline from template to healthy agent

### R5.3 Agent Registry in Ruriko DB
- [ ] Extend `agents` table (or create `agent_desired_state` table):
  - [ ] assigned gosuto hash (desired vs actual)
  - [ ] enabled/disabled flag
  - [ ] last health check timestamp
  - [ ] provisioning state
- [ ] Reconciler compares desired state vs actual state and alerts on drift
- [ ] Test: Registry tracks desired vs actual state correctly

### R5.4 Chat-Driven Creation
- [ ] Handle natural language requests (stretch goal — can be command-based for MVP):
  - `/ruriko agents create --template tim --name Tim`
  - `/ruriko agents create --template warren --name Warren`
  - `/ruriko agents create --template brave --name Brave`
- [ ] Guide user through required secrets if not yet stored:
  - "Warren needs a finnhub API key. Use `/ruriko secrets set finnhub_api_key` to add it."
- [ ] Test: User can create Tim/Warren/Brave via commands; agents appear in Matrix and ACP

### Definition of done
- User can ask Ruriko to create Tim, Warren, and Brave
- Ruriko provisions them fully (container + config + secrets)
- Agents appear in Matrix and respond to ACP health checks

---

## 📋 Phase R6: Canonical Workflow — Tim → Warren → Brave (3–8 days)

**Goal**: Deliver the reference story end-to-end. Make it feel like "agents collaborating as people."

> Maps to REALIGNMENT_PLAN Phase 7.

### R6.1 Tim Scheduling
- [ ] Tim emits a trigger every N minutes (configurable, default 15)
- [ ] Trigger is sent as a Matrix DM to Warren (human-readable but structured enough for parsing)
- [ ] Tim is intentionally deterministic: no LLM reasoning, only schedule + notify
- [ ] Tim should handle: start, stop, interval change via Gosuto
- [ ] Test: Tim sends periodic triggers visible in Matrix

### R6.2 Warren Analysis Pipeline
- [ ] Warren receives trigger from Tim
- [ ] Warren checks for portfolio config in DB:
  - If missing, asks Bogdan in Matrix DM for portfolio (tickers, allocations)
  - Stores portfolio in DB for subsequent runs
- [ ] Warren queries finnhub MCP for market data (prices, changes, fundamentals)
- [ ] Warren writes analysis to DB (structured: tickers, metrics, commentary)
- [ ] Warren sends summary report to Ruriko (or to a shared Matrix room)
- [ ] Test: Warren produces a portfolio analysis from finnhub data

### R6.3 Ruriko Orchestration
- [ ] Ruriko receives Warren's report
- [ ] Ruriko extracts tickers/topics from the report
- [ ] Ruriko asks Brave to search for related news
- [ ] Ruriko forwards Brave's news results back to Warren for revision
- [ ] Warren revises analysis with news context
- [ ] Ruriko decides whether to notify Bogdan based on:
  - [ ] Significance threshold (material changes, big news)
  - [ ] Rate limiting (no more than N notifications per hour)
- [ ] If significant: Ruriko sends Bogdan a concise final report
- [ ] If not significant: Ruriko logs but does not notify
- [ ] Test: Full orchestration loop produces a final report

### R6.4 Brave News Search
- [ ] Brave receives search request from Ruriko (tickers/company names)
- [ ] Brave uses Brave Search MCP to fetch news
- [ ] Brave summarizes results (structured output + short narrative)
- [ ] Brave returns results to Ruriko
- [ ] Test: Brave searches and returns relevant news summaries

### R6.5 End-to-End Story Validation
- [ ] Full cycle test: Tim triggers → Warren analyzes → Brave searches → Warren revises → Bogdan gets report
- [ ] Validate: No secrets visible in any Matrix room
- [ ] Validate: Control operations happen via ACP, not Matrix
- [ ] Validate: Report is coherent, timely, and actionable
- [ ] Validate: User can intervene mid-cycle (e.g., "stop", "skip this one")

### Definition of done
- The full Tim → Warren → Brave workflow runs reliably
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
- [ ] User runs `/ruriko agents create --template warren --name Warren`
- [ ] Ruriko provisions container, applies config, pushes secret tokens
- [ ] Warren appears in Matrix and responds to ACP health check
- [ ] Test: Full provisioning from command to healthy agent

### R8.4 Canonical Workflow Flow
- [ ] Tim triggers Warren every 15 minutes
- [ ] Warren queries finnhub, writes analysis, reports to Ruriko
- [ ] Ruriko asks Brave for news, forwards to Warren, sends report to user
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
✅ **Conversation**: User can chat with Ruriko over Matrix
✅ **Secrets**: User stores secrets via Kuze one-time links; secrets never in chat
✅ **Agents**: Ruriko provisions Tim/Warren/Brave via ACP with Gosuto config
✅ **ACP**: Authenticated, idempotent, private to Docker network
✅ **Workflow**: Tim triggers Warren → Warren analyzes → Brave searches → report delivered
✅ **Security**: No secrets in Matrix history, ACP payloads, or logs

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

---

## 📝 Notes

- **Ship the canonical story**: Tim → Warren → Brave is the north star
- **Security by default**: Secrets never in chat, ACP always authenticated
- **Conversation-first**: Everything important should be explainable in chat
- **Non-technical friendly**: Setup must not require engineering expertise
- **Boring control plane**: ACP is reliable, authenticated, idempotent
- **Fail safely**: Better to refuse an action than execute it incorrectly
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
- [ ] Phase R4: Token-Based Secret Distribution to Agents
- [ ] Phase R5: Agent Provisioning UX — Tim, Warren, Brave
- [ ] Phase R6: Canonical Workflow — Tim → Warren → Brave
- [ ] Phase R7: Observability, Safety, and Polish
- [ ] Phase R8: Integration and End-to-End Testing

---

**Last Updated**: 2026-02-19
**Current Focus**: Phase R3 — Kuze

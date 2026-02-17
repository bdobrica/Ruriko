# Ruriko Implementation TODO

> Roadmap for building a distributed control plane for secure, capability-scoped AI agents running over Matrix.

**Project Goal**: Build Ruriko, a control plane that manages AI agent lifecycle, secrets, policies, and approvals with deterministic, policy-first enforcement.

---

## 🎯 MVP Scope

The first working release should include:

- ✅ Ruriko control plane (Matrix-based command interface)
- ✅ SQLite-backed agent inventory and audit logging
- ✅ Secrets management (encrypted at rest, push updates)
- ✅ Agent lifecycle control (spawn/stop/respawn via Docker)
- ✅ Gosuto configuration versioning and application
- ✅ Approval workflow for sensitive operations
- ✅ Basic observability (audit log, trace correlation)

Gitai (agent runtime) will be built in parallel but can start after Ruriko foundations are in place.

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
- [ ] Create `internal/ruriko/provisioning/matrix.go` - Matrix admin API client
- [ ] Implement account creation (based on homeserver - Synapse, Dendrite, Conduit)
- [ ] Generate secure random passwords/tokens
- [ ] Store agent `mxid` and `access_token` as secrets
- [ ] Test: Can create Matrix account programmatically

### 4.2 Agent Account Management
- [ ] Extend `/ruriko agents create` to also provision Matrix account
- [ ] Add `--mxid <existing>` flag to use pre-existing account
- [ ] `/ruriko agents matrix register <name>` - provision account for existing agent
- [ ] Set agent display name and avatar (optional)
- [ ] Join agent to required rooms (admin room, approvals room)
- [ ] Test: Agent account is created and joins rooms

### 4.3 Deprovisioning
- [ ] `/ruriko agents disable <name>` - soft disable agent
  - [ ] Revoke Matrix token (if supported by homeserver)
  - [ ] Remove from rooms
  - [ ] Mark as disabled in database
- [ ] Test: Agent is removed from rooms

---

## 📋 Phase 5: Gosuto - Versioned Configuration

**Goal**: Apply and version agent policies and personas.

### 5.1 Gosuto Specification
- [ ] Create `docs/gosuto-spec.md` - formal specification for Gosuto v1
- [ ] Create `common/spec/gosuto/types.go` - Go structs for Gosuto schema:
  - [ ] `GosutoConfig` struct
  - [ ] `Trust` (allowed rooms, senders, E2EE requirements)
  - [ ] `Limits` (rate, cost, concurrency)
  - [ ] `Capability` rules
  - [ ] `Approval` requirements
  - [ ] `Persona` (LLM prompt)
- [ ] Create `common/spec/gosuto/validate.go` - schema validator
- [ ] Test: Valid Gosuto configs parse correctly

### 5.2 Template System
- [ ] Create `templates/cron-agent/gosuto.yaml` - example cron agent config
- [ ] Create `templates/browser-agent/gosuto.yaml` - example browser agent config
- [ ] Create `internal/ruriko/templates/loader.go` - template registry
- [ ] Implement template interpolation (agent name, room IDs, etc.)
- [ ] Test: Templates load and validate

### 5.3 Gosuto Commands
- [ ] `/ruriko gosuto show <agent>` - display current Gosuto config
- [ ] `/ruriko gosuto diff <agent> --from <v1> --to <v2>` - show config changes
- [ ] `/ruriko gosuto set <agent>` - update config (via file attachment)
- [ ] `/ruriko gosuto rollback <agent> --to <version>` - revert to previous version
- [ ] `/ruriko gosuto push <agent>` - apply current Gosuto to running agent via ACP
- [ ] Test: Gosuto versioning works end-to-end

### 5.4 Gosuto Storage and Versioning
- [ ] Compute SHA-256 hash of Gosuto content
- [ ] Store versions in `gosuto_versions` table
- [ ] Keep last N versions (configurable)
- [ ] Track who changed what and when
- [ ] Implement version comparison logic
- [ ] Test: Rollback works correctly

---

## 📋 Phase 6: Approval Workflow

**Goal**: Require human approval for sensitive operations.

### 6.1 Approval Objects
- [ ] Create `internal/ruriko/approvals/types.go` - approval structs:
  - [ ] `Approval` (id, action, target, params, requestor, approvers, status, created, expires, decisions)
  - [ ] `ApprovalDecision` (approver, decision, reason, timestamp)
- [ ] Create `internal/ruriko/approvals/store.go` - approval persistence
- [ ] Create approval table migration
- [ ] Test: Approvals can be stored and retrieved

### 6.2 Approval Command Parser
- [ ] Create `internal/ruriko/approvals/parser.go` - deterministic approval parsing
- [ ] Parse commands:
  - [ ] `approve <approval_id> [reason]`
  - [ ] `deny <approval_id> reason="..."` (reason required)
- [ ] Verify sender is in approvers list
- [ ] Enforce TTL (expired approvals auto-deny)
- [ ] Test: Approval decisions parse correctly

### 6.3 Gated Operations
- [ ] Identify operations requiring approval:
  - [ ] Agent deletion
  - [ ] Secret deletion/rotation (for critical secrets)
  - [ ] Enabling risky MCP tools (browser, shell, filesystem write)
  - [ ] Gosuto changes (optional, configurable)
- [ ] Create `internal/ruriko/approvals/gate.go` - approval gating middleware
- [ ] When gated operation requested:
  - [ ] Generate approval request
  - [ ] Post to approvals room
  - [ ] Block operation until approval received
  - [ ] Store approval in database
- [ ] Test: Gated operations block until approved

### 6.4 Approval Commands
- [ ] `/ruriko approvals list [--status pending|approved|denied]` - list approvals
- [ ] `/ruriko approvals show <id>` - detailed approval info
- [ ] `approve <id>` - approve operation
- [ ] `deny <id> reason="..."` - deny operation
- [ ] Test: Full approval workflow works

---

## 📋 Phase 7: Observability and Safety Polish

**Goal**: Make Ruriko production-ready with robust logging and monitoring.

### 7.1 Trace Correlation
- [x] Create `common/trace/trace.go` - trace ID generation
- [x] Generate unique `trace_id` for each command/request
- [ ] Propagate trace IDs to:
  - [ ] Agent control API calls
  - [ ] Approval requests
  - [ ] Log statements
- [x] `/ruriko trace <trace_id>` - show all related events
- [ ] Test: Trace correlation works across subsystems

### 7.2 Audit Room Integration
- [ ] Add optional audit room configuration
- [ ] Post human-friendly audit messages to room for major events:
  - [ ] Agent created/stopped/deleted
  - [ ] Secrets added/rotated/deleted
  - [ ] Gosuto changes
  - [ ] Approvals requested/granted/denied
- [ ] Include trace IDs in messages
- [ ] Test: Audit messages appear in room

### 7.3 Structured Logging
- [ ] Implement consistent log levels (debug, info, warn, error)
- [ ] Add context to all log statements (trace_id, actor, action)
- [ ] Redact secrets from logs
- [ ] Add log filtering by level (config option)
- [ ] Test: Logs are clean and useful

### 7.4 Health and Status Endpoints
- [ ] Create optional HTTP server for metrics/health
- [ ] `/health` - basic health check
- [ ] `/status` - Ruriko status (uptime, agent count, recent errors)
- [ ] Optional: Prometheus metrics export
- [ ] Test: Status endpoint works

### 7.5 Error Handling and Recovery
- [x] Implement graceful shutdown (SIGTERM handling in `cmd/ruriko/main.go`)
- [ ] Handle Matrix disconnections gracefully (reconnect)
- [ ] Handle database errors gracefully
- [ ] Add retry logic for transient failures
- [ ] Test: Ruriko recovers from common error scenarios

---

## 📋 Phase 8: Deployment and Documentation

**Goal**: Make it easy to deploy and operate Ruriko.

### 8.1 Docker Image
- [ ] Create `deploy/docker/Dockerfile.ruriko`
- [ ] Build multi-stage Docker image (build + runtime)
- [ ] Support configuring via environment variables
- [ ] Create `deploy/docker/entrypoint.sh` script
- [ ] Test: Docker image runs correctly

### 8.2 Docker Compose Example
- [ ] Create `examples/docker-compose/docker-compose.yaml`
- [ ] Include:
  - [ ] Ruriko service
  - [ ] Example agent (Gitai) - placeholder for now
  - [ ] Optional: local Synapse/Dendrite instance
- [x] Create `.env.example` with required configuration
- [ ] Test: Docker Compose stack starts

### 8.3 Configuration Documentation
- [ ] Document required environment variables
- [ ] Document Matrix homeserver setup
- [ ] Document admin room creation and configuration
- [ ] Document approvals room setup
- [ ] Create quickstart guide

### 8.4 Operational Documentation
- [ ] Create `docs/ops/deployment-docker.md`
- [ ] Create `docs/ops/backup-restore.md` (SQLite backup)
- [ ] Document disaster recovery procedures
- [ ] Document upgrading Ruriko
- [ ] Document common troubleshooting steps

---

## 📋 Phase 9: Gitai Agent Runtime (Parallel Development)

**Note**: This can start after Phase 3 of Ruriko is complete. See separate TODO in RURIKO_COMPONENTS.md.

### High-Level Gitai Phases:
1. Basic Matrix connection + message handling
2. Agent Control Protocol (HTTP server)
3. Gosuto loading and hot-reload
4. Policy engine and constraints
5. LLM interface and tool proposal loop
6. MCP client and supervisor
7. Approval workflow (agent-side)
8. Secrets handling
9. Observability and auditing

---

## 📋 Phase 10: Integration and End-to-End Testing

**Goal**: Ensure Ruriko and Gitai work together seamlessly.

### 10.1 Agent Template Implementation
- [ ] Implement working cron-agent template
- [ ] Implement working browser-agent template
- [ ] Test agent creation from templates

### 10.2 End-to-End Scenarios
- [ ] Test: Create agent → provision Matrix account → spawn container → joins rooms → responds to messages
- [ ] Test: Update Gosuto → push to agent → agent behavior changes
- [ ] Test: Rotate secret → push to agent → agent uses new secret
- [ ] Test: Approval workflow → sensitive operation → approve → operation executes
- [ ] Test: Agent dies → reconciler detects → respawn → resumes operation
- [ ] Test: Full audit trail → trace correlation works

### 10.3 Load and Resilience Testing
- [ ] Test: Multiple agents running simultaneously
- [ ] Test: Matrix reconnection after disconnect
- [ ] Test: Database corruption/recovery
- [ ] Test: Container runtime failures

---

## 🎯 Success Criteria for MVP

✅ **Ruriko can**:
- Connect to Matrix and process commands deterministically
- Create, start, stop, and monitor agent containers (Docker)
- Securely store and distribute secrets
- Version and apply Gosuto configurations
- Require and process approvals for sensitive operations
- Maintain audit logs with trace correlation

✅ **Gitai can**:
- Connect to Matrix and receive messages
- Load and apply Gosuto configurations
- Communicate with Ruriko via ACP
- Execute basic tool calls (MCP)
- Enforce policy constraints
- Handle approval workflows

✅ **Together they**:
- Implement at least one working agent template (e.g., cron-agent)
- Demonstrate safe, policy-controlled operation
- Provide full audit trails
- Run in Docker Compose for easy testing

---

## 🚀 Next Steps After MVP

- [ ] Kubernetes runtime adapter
- [ ] Codex integration (template generation)
- [ ] Advanced MCP tool ecosystem
- [ ] Multi-agent coordination (envelope-based communication)
- [ ] Web UI for Ruriko management
- [ ] Enhanced secret management (Vault integration, lease model)
- [ ] Enhanced observability (distributed tracing, metrics)

---

## 📝 Notes

- **Start small**: Focus on Docker runtime for MVP, add K8s later
- **Determinism first**: Never let LLM output control Ruriko logic
- **Security by default**: Default deny, explicit approvals, audit everything
- **Fail safely**: Better to refuse an action than execute it incorrectly
- **Document as you go**: Keep invariants and threat model up to date

---

## 🔄 Status Tracking

Update this section as phases are completed:

- [x] Phase 0: Project Setup & Foundations ✅ **COMPLETED**
- [x] Phase 1: Ruriko MVP - Matrix Control + Inventory ✅ **COMPLETED**
- [x] Phase 2: Secrets Management ✅ **COMPLETED** (secrets push distributor deferred to Phase 5)
- [x] Phase 3: Agent Lifecycle Control ✅ **COMPLETED** (template loading + volume mounts deferred to Phase 5)
- [ ] Phase 4: Matrix Identity Provisioning
- [ ] Phase 5: Gosuto - Versioned Configuration
- [ ] Phase 6: Approval Workflow
- [ ] Phase 7: Observability and Safety Polish
- [ ] Phase 8: Deployment and Documentation
- [ ] Phase 9: Gitai Agent Runtime
- [ ] Phase 10: Integration and E2E Testing

---

**Last Updated**: 2026-02-17
**Current Focus**: Phase 4 - Matrix Identity Provisioning

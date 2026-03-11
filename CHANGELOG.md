# Ruriko — Completed Phases

> Historical record of all completed implementation phases.
> For active work, see [TODO.md](TODO.md).
> For detailed task lists, see git history.

---

## Maintenance Updates

### 2026-03-11 — REFACTOR Retirement
- Removed the legacy R6 refactor-plan document after consolidating planning/validation contract details into canonical docs.
- Updated `docs/workflow-step-spec.md` references to point to `TODO.md` as the active R6 tracker.
- Marked the R6 legacy refactor-plan retirement task as completed in `TODO.md`.

### 2026-03-11 — R6 Plan Canonicalization
- Consolidated remaining R6 workflow-schema validation contract details into `TODO.md` so task tracking no longer depends on a separate refactor-plan document.
- Updated the R6 phase note in `TODO.md` to point at canonical tracking docs (`TODO.md`, `docs/workflow-step-spec.md`).
- Replaced the "keep refactor plan in sync" TODO item with an explicit retirement task once canonical docs remained complete.

### 2026-03-09 — Vulnerability Remediation
Bumped Go to 1.25.8, upgraded `golang.org/x/net` to v0.51.0 (GO-2026-4559), aligned Docker builder images. `govulncheck` clean.

### 2026-03-08 — Phase 7 Hardening Kickoff
- Replaced canonical live compose verification with operator-driven Saito→Kumo chain (NL bootstrap, Kuze secret entry, fast cron ticks, Kumo delivery verification).
- Added standalone Saito and Kumo live integration harnesses (no Ruriko dependency).
- Added strict 3-cycle canonical verification (`make test-canonical-workflow-live-compose-3cycles`).
- Added Node.js/npm to Gitai runtime image for `npx`-based MCP servers.
- Fixed `kumo-agent` peer placeholder substitution in live bootstrap.

### 2026-03-08 — Workflow Engine Status Alignment
- Closed workflow-engine Phase 4 (Gosuto schema/validator formalization).
- Added peer-generic closure proof for Kumo template with non-Kairo peer overrides.
- Added NL dispatch guard refusing `topology.*` mutation actions from LLM intent.
- Added topology audit/version proof coverage.

### 2026-03-08 — Topology Operator Docs & Commands
- Added operator-facing peer-override examples for `agents create` in OPERATIONS.md and mesh-topology docs.
- Started deterministic topology command surface: `topology refresh`, `peer-set`, `peer-ensure`, `peer-remove` with approval-gated widening and optional `--push` orchestration.
- Added canonical provisioning ensure-if-missing flow for `kumo-agent`.

### 2026-03-02 — Deterministic Live Scheduling
- Added operator → Ruriko → Saito live integration flow with DB schedule assertions and Matrix delivery verification.
- Added quote-aware deterministic command parsing for schedule flags.
- Hardened live tests with fail-fast/convergence detection and OpenAI capture-proxy auditing.

### 2026-03-02 — DB-Backed Cron & Schedule Commands
- Added `schedule.upsert`, `schedule.disable`, `schedule.list` built-in tools in Gitai with SQLite `cron_schedules` table.
- Extended cron gateway with `config.source: db` mode for deterministic execution.
- Added ACP `POST /tools/call` endpoint and Ruriko schedule control commands wired through ACP.
- Updated Saito template to use DB-backed scheduler source.

### 2026-03-01 — Cross-Binary De-duplication
Extracted shared packages into `common/`:
- **webhookauth**: HMAC signature validation (`common/webhookauth/`).
- **ratelimit**: Keyed fixed-window rate limiter (`common/ratelimit/`).
- **sqliteutil**: SQLite bootstrap + migration runner (`common/sqliteutil/`).
- **llm/openai**: OpenAI-compatible chat transport core (`common/llm/openai/`).
- **matrixcore**: Matrix low-level client core (`common/matrixcore/`).
- **memory**: Shared memory contracts/context assembler (`common/memory/`); added Gitai memory-context hook behind `GITAI_MEMORY_CONTEXT_ENABLE`.

Refactored both Ruriko and Gitai integrations to consume shared packages. Added unit tests for all new shared packages.

### 2026-03-01 — Canonical Script Renaming
Renamed live verification scripts/targets/env vars from `R6_*` to `CANONICAL_*`. Extracted bootstrap logic into `canonical_workflow_bootstrap.py`.

---

## Infrastructure Phases (0–9)

### Phase 0: Project Setup & Foundations ✅
Go module, project structure, Makefile, linting config, core docs (`invariants.md`, `architecture.md`, `threat-model.md`), dependencies (mautrix-go, SQLite, AES-GCM, slog).

### Phase 1: Ruriko MVP — Matrix Control + Inventory ✅
Matrix connection via mautrix-go, deterministic command router (`/ruriko <cmd>`), SQLite schema with auto-migration (agents, secrets, gosuto_versions, audit_log tables), agent inventory commands (`agents list/show`), audit logging with trace ID correlation.

### Phase 2: Secrets Management ✅
AES-GCM encrypted secret storage (`common/crypto/`), secret types (matrix_token, api_key, generic_json), rotation versioning, secret commands (list/set/rotate/delete/info/bind/unbind), raw-secret-never-in-Matrix invariant enforced.

### Phase 3: Agent Lifecycle Control ✅
Runtime abstraction layer (`Runtime` interface: Spawn/Stop/Restart/Status/List/Remove), Docker runtime adapter via Docker Engine API, ACP HTTP client (health/status/config/secrets/restart/cancel), lifecycle commands (create/stop/start/respawn/delete/status), periodic reconciliation loop.

### Phase 4: Matrix Identity Provisioning ✅
Homeserver admin API integration (Synapse shared-secret + generic fallback), automatic account creation with secure random credentials, agent MXID/token storage, room invitation, deprovisioning with account deactivation.

### Phase 5: Gosuto — Versioned Configuration ✅
Gosuto v1 specification and schema (Trust, Limits, Capabilities, Approvals, Persona), template system with interpolation (saito/kumo/browser/cron agents), Gosuto commands (show/versions/diff/set/rollback/push), SHA-256 hashing, version retention, secret distributor.

### Phase 6: Approval Workflow ✅
Approval objects with decision tracking, deterministic approval parser (approve/deny with reason), gated operations (agent deletion, secret rotation, Gosuto changes), approval commands (list/show/approve/deny), TTL-based auto-deny.

### Phase 7: Observability and Safety Polish ✅ (mostly)
Trace ID generation and correlation across ACP/reconciler/logs, optional Matrix audit room with human-friendly event notifications, structured logging (slog, log levels, secret redaction), HTTP health/status endpoints, graceful shutdown, reconnection with backoff, retry logic. Prometheus metrics deferred.

### Phase 8: Deployment and Documentation ✅
Multi-stage Docker image (`Dockerfile.ruriko`), Docker Compose example with homeserver, `.env.example`, operational docs (`deployment-docker.md`, `backup-restore.md`), quickstart guide.

### Phase 9: Gitai Agent Runtime ✅
Matrix connection + message handling, ACP HTTP server, Gosuto loading and hot-reload, first-match-wins policy engine (allow/deny/requireApproval, default deny), LLM interface with tool proposal loop, MCP client and process supervisor, agent-side approval workflow, secret manager, observability and auditing.

---

## Realignment Phases

### Phase R0: Project Hygiene and Config Alignment ✅
Shared `common/environment` package replacing duplicated env helpers, crypto decoupled from environment (`ParseMasterKey` accepts parameter), DB schema drift cleanup (`agent_endpoints` removed), docs aligned to preamble terminology.

### Phase R1: Matrix Stack Realignment — Tuwunel Default ✅
Docker Compose switched from Synapse to Tuwunel (federation OFF, registration OFF), Tuwunel provisioning support via registration token flow, documentation updated for Tuwunel quickstart.

### Phase R2: ACP Hardening — Auth, Idempotency, Timeouts ✅
Bearer token authentication (128-bit random, per-agent), idempotency headers with response caching (`X-Request-ID`, `X-Idempotency-Key`), per-operation context timeouts (2s health → 30s config apply), `io.LimitReader` response safety, `POST /tasks/cancel` endpoint.

### Phase R3: Kuze — Human Secret Entry ✅
Kuze HTTP endpoints embedded in Ruriko (token issue, HTML form serve, secret submission with token burn), one-time cryptographic tokens with TTL, Matrix UX integration (`/ruriko secrets set` → one-time link), secret-in-chat guardrail (pattern matching for API keys/base64), minimal embedded HTML form.

### Phase R4: Token-Based Secret Distribution ✅
Kuze agent redemption endpoints (issue + single-use redeem), replaced direct `/secrets/apply` push with token-based model (410 Gone by default), agent-side secret manager with in-memory cache and TTL, `{{secret:ref}}` placeholder resolution in tool call arguments, automatic LLM provider rebuild on secret refresh.

### Phase R5: Agent Provisioning UX ✅
Canonical agent templates (saito-agent, kumo-agent with Brave Search/Fetch MCPs), automated provisioning pipeline (DB record → Docker container → ACP health → Gosuto apply → secret token push) with Matrix breadcrumbs, agent registry with desired-vs-actual state tracking, chat-driven creation via keyword matching ("set up Saito", "create a news agent called kumo2") with conversation sessions.

### Phase R9: Natural Language Interface ✅
LLM-powered NL classifier (OpenAI-compatible, `gpt-4o-mini` default) translating natural language to structured command intents with JSON schema enforcement. Three-tier confidence gating (≥0.8 proceed, 0.5–0.8 confirm interpretation, <0.5 clarify). Read-only queries answered directly; mutations require confirmation. Multi-step decomposition with per-step approval. Graceful degradation to keyword matching when LLM unavailable. Per-sender rate limiting and daily token budgets. Runtime config store (`/ruriko config set/get`) for model/endpoint/rate-limit without restart. NLP API key via Kuze secret (`ruriko.nlp-api-key`) with env var fallback. Lazy provider rebuild on config change.

### Phase R11: Event Gateways — Schema & Types ✅
`Gateway` struct added to Gosuto types (cron/webhook built-in, external binary), validation (mutually exclusive type/command, cron expression required, HMAC secret ref, unique names, no MCP collision), `Event` envelope types with validation, Saito template updated to use cron gateway.

### Phase R12: Event Gateways — Gitai Runtime Integration ✅
ACP `POST /events/{source}` endpoint with rate limiting and authentication, event-to-turn bridge (events trigger LLM turns via same `runTurn` pipeline), built-in cron gateway (goroutine, respects context cancellation, reconciles on config change), built-in webhook gateway (bearer/HMAC-SHA256 auth), external gateway process supervisor (same pattern as MCPs — start/monitor/restart/reconcile), event-to-Matrix bridging (responses posted to admin room), gateway event auditing with source attribution.

### Phase R13: Ruriko-Side Gateway Wiring ✅
Webhook reverse proxy (`POST /webhooks/{agent}/{source}`) with HMAC validation and per-agent rate limiting, provisioning pipeline updated for gateway-bearing Gosuto configs, gateway binary inclusion in Docker image documented and implemented, architecture/threat-model/README updated.

### Phase R5.1: Kairo Agent Template ✅
Finance agent template with Finnhub and database MCPs, capability rules (allow finnhub, allow DB CRUD no deletes), financial analyst persona (`gpt-4o`, temperature 0.2).

### Phase R14: Gosuto Persona / Instructions Separation ✅
Added `instructions` section to Gosuto (role, workflow triggers/actions, context with user and peer descriptions). Three-layer model enforced: Policy (authoritative, code-enforced) > Instructions (operational workflow) > Persona (cosmetic tone). System prompt assembled from persona + instructions + peer context + messaging targets. All canonical templates updated with instruction blocks. Instructions versioned and diffable alongside Gosuto.

### Phase R15: Built-in Matrix Messaging Tool ✅
`matrix.send_message` built-in tool with alias → room ID resolution, rate limiting, and policy gating via `builtin` pseudo-MCP namespace. Messaging policy in Gosuto (`messaging.allowedTargets`, `maxMessagesPerMinute`). Policy engine integration (same first-match-wins rules as MCP tools). Mesh topology injection at provision time (`InjectMeshTopology` from agent store). Audit logging with trace IDs and admin-room breadcrumbs. Canonical templates wired with `allow-matrix-send` capability and peer room placeholders.

### Phase R10: Conversation Memory ✅
Two-tier memory: sharp STM (full conversation in context window) + fuzzy LTM (sealed conversations summarised, embedded, stored). `ConversationTracker` per room+sender with configurable cooldown (15 min default) and sliding window with summary prepend. Pluggable `LongTermMemory`, `Embedder`, and `Summariser` interfaces with noop stubs. `ContextAssembler` wires STM + LTM into NL classifier calls with token budget. Seal pipeline: summarise → embed → store on cooldown. Persistent backends: SQLite LTM with brute-force cosine similarity, OpenAI embeddings (`text-embedding-3-small`), LLM-powered summariser. Config via env vars (`MEMORY_*`). pgvector deferred post-MVP.

### Phase R16: Canonical Agent Knowledge & NLP Planning Layer ✅
NLP system prompt enriched with canonical agent role knowledge derived from Gosuto template metadata (`templates.Registry.DescribeAll()`). Multi-agent workflow decomposition: complex requests parsed into ordered `plan` steps with per-step user confirmation. Natural language → cron expression mapping with validation and ambiguity clarification. Agent ID sanitisation (lowercase normalisation in NL dispatch path). Conversation history injected into NLP classifier calls (STM from R10). Re-query on validation failure with error context (max 2 retries).

---

## Status Tracking

### Infrastructure Phases
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
- [x] Phase R5: Agent Provisioning UX ✅
- [x] Phase R9: Natural Language Interface ✅
- [x] Phase R11: Event Gateways — Schema, Types, Validation ✅
- [x] Phase R12: Event Gateways — Gitai Runtime Integration ✅
- [x] Phase R13: Ruriko-Side Gateway Wiring ✅
- [x] Phase R14: Gosuto Persona / Instructions Separation ✅
- [x] Phase R15: Built-in Matrix Messaging Tool ✅
- [x] Phase R10: Conversation Memory — Short-Term / Long-Term Architecture ✅
- [x] Phase R16: Canonical Agent Knowledge & NLP Planning Layer ✅

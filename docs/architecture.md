# Ruriko System Architecture

> **High-level architecture and component interactions**

**Version**: 0.1.0  
**Last Updated**: 2026-02-22  
**Status**: Living Document

> For the canonical product story, user UX contract, and full glossary see
> [preamble.md](./preamble.md).

---

## Table of Contents

1. [Overview](#overview)
2. [System Components](#system-components)
3. [Architecture Diagrams](#architecture-diagrams)
4. [Component Details](#component-details)
5. [Data Flow](#data-flow)
6. [Communication Protocols](#communication-protocols)
7. [Deployment Models](#deployment-models)

---

## Overview

Ruriko is a distributed control plane for managing secure, capability-scoped AI agents over Matrix. The system follows a **control plane + data plane** architecture with a **peer-to-peer execution model**:

- **Ruriko (Control Plane)**: Plans workflows, manages agent lifecycle, secrets, policy, and approvals. Ruriko drafts agent configurations and knows the mesh topology, but does not relay agent-to-agent interactions.
- **Gitai (Data Plane)**: Individual agent runtimes that execute under policy constraints and **collaborate peer-to-peer by messaging each other directly over Matrix**.

**Key Design Principles**:
- Separation of control logic from agent logic
- Centralized secrets and policy management
- Deterministic, non-LLM control decisions
- Defense in depth with multiple security layers
- **Peer-to-peer agent collaboration** — Ruriko plans, agents execute
- **Built-in Matrix messaging** as a first-class agent capability

---

## System Components

### 1. Ruriko (Control Plane)

**Responsibilities**:
- Agent lifecycle management (create, start, stop, delete)
- Secrets storage, rotation, and distribution
- Gosuto (policy) versioning and application
- Matrix identity provisioning
- Approval workflow orchestration
- Audit logging and trace correlation
- Template management

**Key Subsystems**:
- Command Router: Deterministic Matrix command parser
- Runtime Adapters: Docker, Kubernetes, systemd
- Secret Store: Encrypted secrets with rotation
- Kuze: One-time link secret entry + agent secret token distribution
- Approval Engine: Workflow and decision tracking
- Agent Control Protocol (ACP) Client: HTTP client for agent management

**State Storage**:
- SQLite database (agent inventory, secrets, audit logs, approvals)
- Optional: Redis for caching (future)

---

### 2. Gitai (Agent Runtime)

**Responsibilities**:
- Matrix message handling and response
- **Peer-to-peer agent collaboration via built-in Matrix messaging tool**
- Policy enforcement (Gosuto rules)
- LLM interaction (OpenAI, local models)
- MCP tool supervision and execution
- Event gateway supervision and event processing
- Approval request generation
- Local audit logging

**Key Subsystems**:
- Matrix Client: Message ingestion and sending
- **Matrix Messaging Tool**: Built-in tool for sending messages to other agents' rooms or users (policy-gated)
- Policy Engine: Capability-based authorization
- Envelope Parser: Structured agent-to-agent communication
- LLM Interface: Provider abstraction
- MCP Supervisor: Tool process lifecycle
- Gateway Supervisor: Event gateway process lifecycle
- Agent Control Protocol Server: HTTP server for Ruriko commands

**State Storage**:
- SQLite database (local approvals cache, traces)
- In-memory: active Gosuto config, secrets, MCP processes

---

### 3. Gosuto (Configuration)

**Type**: Versioned YAML configuration

**Contents**:
- Trust contexts (allowed rooms, senders)
- Capability rules (what tools/actions are allowed)
- Constraints (rate limits, URL filters, payload sizes)
- Approval requirements
- MCP server definitions
- Persona and style (LLM prompt)

**Storage**: 
- Versioned in Ruriko's database
- Applied to agents via Agent Control Protocol
- Loaded into Gitai memory on startup/update

---

### 4. Matrix Homeserver

**Role**: Identity and messaging layer

**Responsibilities**:
- User and agent authentication
- Room management
- Message routing (E2EE optional)
- Presence and typing indicators

**Integration Points**:
- Ruriko connects as admin bot
- Agents connect as individual users
- Admin room for Ruriko commands
- Approval room for human decisions
- Optional audit room for transparency

---

### 5. MCP Servers (Tools)

**Role**: External tool capabilities

**Examples**:
- Browser automation (Playwright)
- Weather APIs
- File system access
- Database queries
- Custom enterprise connectors

**Integration**:
- Launched and supervised by Gitai
- Communicate via stdio or TCP
- Defined in Gosuto configuration
- Calls gated by policy engine

---

### 6. Event Gateways

**Role**: Inbound event ingestion — the symmetric inbound complement to MCPs

**Types**:
- **Built-in: Cron** — fires `cron.tick` events on a 5-field cron schedule (no external dependency)
- **Built-in: Webhook** — accepts HTTP POSTs from Ruriko's `/webhook/{agent}/{source}` proxy
- **External binaries** — compiled artefacts baked into the Gitai Docker image (e.g. `ruriko-gw-imap`)

**Integration**:
- Supervised by Gitai (same process model as MCP servers: start, monitor, restart)
- POST normalised event envelopes (`common/spec/envelope`) to the agent's ACP: `POST /events/{source}`
- Defined under `gateways:` in Gosuto configuration
- Credentials managed by Ruriko's secrets pipeline; injected as environment variables
- External binary artefacts listed in `deploy/docker/gateway-manifest.yaml`

**Event lifecycle**:
- Gateway process fires → POSTs envelope to ACP `/events/{source}` → Gitai turn engine handles it identically to a Matrix message → LLM proposes tools → policy engine enforces → response sent to Matrix room

---

## Architecture Diagrams

### High-Level System Architecture

```mermaid
flowchart TB
  Matrix["Matrix Homeserver\nIdentity and Messaging Bus"]

  subgraph ControlPlane["Ruriko Control Plane"]
    CR["Command Router"]
    RM["Runtime Manager\nDocker, Kubernetes, systemd"]
    SS["Secret Store"]
    AM["Approval Manager"]
    AL["Audit Logger"]
    DB[("SQLite\nState Store")]
  end

  subgraph RuntimeInfra["Runtime Infrastructure"]
    RI["Docker / Kubernetes"]
  end

  subgraph Agent1["Gitai Agent 1 Runtime"]
    PE["Policy Engine"]
    LLM["LLM Interface"]
    MCP["MCP Supervisor"]
    EP["Envelope Parser"]
    ADB[("SQLite\nLocal")]
    subgraph MCPTools1["MCP Tool Processes"]
      M1["MCP Process 1\nBrowser"]
      M2["MCP Process 2\nWeather API"]
    end
  end

  subgraph Agent2["Gitai Agent 2 Runtime"]
    A2["..."]
  end

  Matrix --- ControlPlane
  Matrix --- Agent1
  Matrix --- Agent2

  ControlPlane <--> |"Agent Control Protocol\nHTTP"| Agent1

  ControlPlane --- DB
  Agent1 --- ADB

  RI --- Agent1
  RI --- Agent2

  MCP --> M1
  MCP --> M2

  CR --- RM
  RM --- SS
  RM --- AM
  RM --- AL
```

---

### Lifecycle: Agent Creation

```mermaid
sequenceDiagram
  autonumber
  actor Admin as Admin User
  participant R as Ruriko
  participant D as Docker Engine
  participant A as Gitai Agent (ACP Server)

  Admin->>R: /ruriko agents create<br/>--template cron<br/>--name weatherbot

  Note over R: 1. Load template<br/>2. Generate agent_id<br/>3. Store in DB<br/>4. Provision Matrix account

  R->>D: 5. Create container
  D-->>R: container_id

  R->>A: 6. Apply Gosuto (POST /config/apply)
  A-->>R: Gosuto applied (hash/version)

  Note over R: 7. Audit log

  R-->>Admin: ✅ Agent created<br/>trace_id: abc123

```

---

### Lifecycle: Message Processing (Agent)

```mermaid
sequenceDiagram
  autonumber
  participant M as Matrix Server
  participant G as Gitai Agent
  participant P as Policy Engine
  participant L as LLM
  participant T as MCP Server

  M->>G: User message

  Note over G: 1. Parse envelope

  G->>P: 2. Check trust context
  P-->>G: 3. Allowed? YES

  G->>L: 4. Call LLM
  L-->>G: 5. Tool proposal

  G->>P: 6. Check capability
  P-->>G: 7. Allowed? YES

  G->>T: 8. Execute tool
  T-->>G: 9. Result

  Note over G: 10. Redact & format<br/>11. Audit log

  G-->>M: Response
```

---

### Lifecycle: Approval Workflow

```mermaid
sequenceDiagram
  autonumber
  participant G as Gitai Agent
  participant R as Ruriko
  participant AR as Approval Room
  actor H as Approver User

  Note over G: 1. Needs approval

  G->>R: 2. Request approval

  Note over R: 3. Generate approval object<br/>Store in DB

  R->>AR: 4. Post request to room
  AR->>H: 5. Review

  H->>AR: 6. Decision command<br/>(approve ID)
  AR-->>R: Decision event delivered

  Note over R: 7. Validate decision<br/>Update DB

  R-->>G: 8. Notify agent: APPROVED

  Note over G: 9. Execute operation
```

---

### Gateway Architecture

```mermaid
flowchart LR
  subgraph External["External Event Sources"]
    IMAP["IMAP Server\n(email)"]
    WebSrc["Webhook Sender\n(GitHub, Stripe, etc.)"]
    Clock["System Clock\n(cron)"]
  end

  subgraph RurikoProxy["Ruriko Control Plane"]
    WHProxy["/webhook/{agent}/{source}\nRate-limit · HMAC auth · Proxy"]
  end

  subgraph GitaiAgent["Gitai Agent Runtime"]
    GWS["Gateway Supervisor"]

    subgraph GWProcs["Gateway Processes"]
      CronGW["Built-in Cron Gateway"]
      WHBuiltin["Built-in Webhook Gateway"]
      IMAPGW["ruriko-gw-imap\n(external binary)"]
    end

    ACP["ACP\nPOST /events/{source}"]
    TE["Turn Engine\n(Policy → LLM → Tools)"]
  end

  Clock -->|"tick"| CronGW
  WebSrc -->|"HTTP POST"| WebSrc
  WebSrc --> WHProxy
  WHProxy -->|"authenticated,\nrate-limited"| WHBuiltin
  IMAP -->|"IMAP IDLE / poll"| IMAPGW

  GWS --> CronGW
  GWS --> WHBuiltin
  GWS --> IMAPGW

  CronGW -->|"POST envelope"| ACP
  WHBuiltin -->|"POST envelope"| ACP
  IMAPGW -->|"POST envelope"| ACP

  ACP --> TE
```

**Key invariants**:
- Every inbound event — regardless of source — passes through the same turn engine as Matrix messages.
- Built-in gateways run in-process inside Gitai; external binaries are supervised child processes.
- Ruriko's webhook proxy is the only internet-facing ingress; agents are never directly reachable.
- Rate limiting and HMAC verification are enforced at the Ruriko proxy before delivery.

---

## Component Details

### Ruriko Subsystems

#### Command Router (`internal/ruriko/commands/`)
- Parses Matrix messages for `/ruriko` commands
- Validates command syntax and arguments
- Checks sender permissions
- Dispatches to appropriate handlers
- Generates trace IDs
- Returns structured responses

#### Runtime Manager (`internal/ruriko/runtime/`)
- Abstract interface for multiple runtimes (Docker, K8s, systemd)
- Handles agent container lifecycle
- Reconciliation loop (desired vs actual state)
- Health checking
- Resource management

#### Secret Store (`internal/ruriko/secrets/`)
- Encrypts secrets at rest (AES-GCM)
- Manages secret versioning and rotation
- Tracks agent-secret bindings
- Pushes updates to agents
- Audit logging for secret operations

#### Approval Manager (`internal/ruriko/approvals/`)
- Creates approval objects with TTL
- Stores in database
- Parses approval decisions
- Enforces approver lists
- Handles expiration

#### Agent Control Protocol Client (`internal/ruriko/runtime/acp/`)
- HTTP client for agent management
- Endpoints: /health, /status, /config/apply, /secrets/apply, /process/restart
- Bearer-token authentication (MVP); mTLS planned post-MVP
- Request/response logging

---

### Gitai Subsystems

#### Policy Engine (`internal/gitai/policy/`)
- Evaluates trust contexts (room/sender allowlists)
- Evaluates capability rules
- Enforces constraints (rate limits, URL filters, payload sizes)
- Returns ALLOW / APPROVAL_REQUIRED / DENY decisions
- Pure, deterministic logic (no LLM)

#### LLM Interface (`internal/gitai/llm/`)
- Provider abstraction (OpenAI, Anthropic, local models)
- Handles tool proposal format
- Manages context windows
- Tracks token usage
- Retries and error handling

#### MCP Supervisor (`internal/gitai/supervisor/`)
- Starts/stops MCP processes based on Gosuto
- Monitors process health
- Restarts on crash with backoff
- Exposes status via /status endpoint
- Logs MCP stdout/stderr

#### Gateway Supervisor (`internal/gitai/gateway/`)
- Manages built-in cron and webhook gateway goroutines
- Manages external gateway binary processes (same lifecycle as MCPs)
- Reconciles running gateways against active Gosuto on hot-reload
- Restarts external gateway processes on crash (when `autoRestart: true`)
- Exposes gateway status alongside MCP status via `/status` endpoint

#### Matrix Messaging Tool (`internal/gitai/matrix/`)
- Built-in tool available to all LLM-powered agents: `matrix.send_message`
- Allows agents to send messages to other agents' rooms or to users
- Policy-gated: room must be in Gosuto `trust.allowedRooms` or a dedicated messaging allowlist
- Rate-limited: subject to Gosuto `limits` configuration
- Enables peer-to-peer agent collaboration without Ruriko relaying messages
- Audit logged: target room, message hash, and trace ID recorded (never content at INFO level)

#### Envelope Parser (`internal/gitai/envelope/`)
- Extracts JSON envelope from Matrix message
- Validates schema (version, structure)
- Verifies from/to/trace_id
- Handles request/response pairing
- Error handling for malformed envelopes

---

## Data Flow

### 1. Command Flow (Admin → Ruriko)

```
Admin types command in Matrix
    ↓
Matrix homeserver routes to Ruriko
    ↓
Ruriko's Matrix client receives event
    ↓
Command router parses and validates
    ↓
Permission check (sender allowlist)
    ↓
Handler executes operation
    ↓
Audit log written
    ↓
Response sent to Matrix
```

### 2. Message Flow (User → Agent)

```
User sends message in agent's room
    ↓
Matrix homeserver routes to Gitai
    ↓
Gitai's Matrix client receives event
    ↓
Policy engine checks trust context
    ↓
Envelope parser extracts request (if present)
    ↓
LLM processes message
    ↓
LLM proposes tool calls
    ↓
Policy engine validates each tool call
    ↓
MCP tools execute (if allowed)
    ↓
Results aggregated and redacted
    ↓
Response envelope generated
    ↓
Friendly message + envelope sent to Matrix
```

### 3. Gosuto Update Flow

```
Admin uploads new Gosuto to Ruriko
    ↓
Ruriko validates YAML schema
    ↓
Computes hash, stores as new version
    ↓
Sends to agent via ACP: POST /config/apply
    ↓
Gitai validates received Gosuto
    ↓
Atomic swap to new config
    ↓
Gateway supervisor reconciles (starts/stops gateway processes)
    ↓
Reports applied hash back to Ruriko
    ↓
Audit logged
```

---

### 4. Gateway Event Flow

```
External event source fires (cron tick, email arrives, webhook delivery)
    ↓
Gateway process (built-in or external binary) receives event
    ↓
Gateway normalises event into envelope {source, type, ts, payload}
    ↓
Gateway POSTs envelope to ACP: POST /events/{source}
    ↓                              (with Bearer token if GITAI_ACP_TOKEN set)
Gitai turn engine receives event
    ↓
Policy engine checks trust context (sender allowlist, rate limits)
    ↓
LLM processes payload.message as user turn
    ↓
LLM proposes tool calls → policy engine validates each
    ↓
MCP tools execute (if allowed)
    ↓
Response sent to Matrix room
    ↓
Audit logged (source, type, status — never payload content)
```

### 5. Inter-Agent Collaboration Flow (Peer-to-Peer)

```
Agent A decides it needs Agent B's help (LLM reasoning)
    ↓
LLM proposes tool call: matrix.send_message(room=B's room, message="...")
    ↓
Policy engine checks: is B's room in A's allowed messaging targets?
    ↓
Rate limit check: has A exceeded its messaging rate?
    ↓
If ALLOWED: Matrix client sends message to B's room
    ↓
Agent B receives the message (normal Matrix message flow)
    ↓
B processes, reasons, and may reply to A or send results to the user
    ↓
Audit logged on both sides (sender, target room, trace ID)
```

This is the **peer-to-peer model**: Ruriko configured both agents and their
policies (which rooms they can message, what tools they have), but the actual
collaboration happens directly between agents over Matrix.

---

## Communication Protocols

### 1. Matrix (Human ↔ Ruriko, Human ↔ Agent, Agent ↔ Agent)

**Protocol**: Matrix Client-Server API  
**Transport**: HTTPS (WebSocket for sync)  
**Authentication**: Access token  
**Message Format**: `m.room.message` events  

**Usage**:
- Admin commands to Ruriko
- User messages to agents
- Agent responses
- Agent-to-agent structured requests (envelope-based)
- Approval workflows

---

### 2. Agent Control Protocol (Ruriko ↔ Gitai)

**Protocol**: HTTP REST  
**Transport**: HTTP/1.1 on private Docker network (MVP)  
**Authentication**: Per-agent Bearer token (MVP)  
**Format**: JSON  

**Note**: In the single-host MVP, ACP is intentionally private-network HTTP
with bearer authentication. mTLS is planned as post-MVP hardening for
multi-host or otherwise untrusted network topologies.

**Endpoints**:
- `GET /health` - Health check
- `GET /status` - Runtime status (version, gosuto hash, MCPs)
- `POST /config/apply` - Apply new Gosuto
- `POST /secrets/apply` - Push secrets update
- `POST /process/restart` - Graceful restart

---

### 3. MCP (Gitai ↔ MCP Servers)

**Protocol**: Model Context Protocol (stdio or TCP)  
**Transport**: stdin/stdout or TCP socket  
**Format**: JSON-RPC 2.0  

**Operations**:
- Tool discovery
- Tool invocation
- Result return

---

### 4. Gateway Events (Gateway Process → Gitai ACP)

**Protocol**: HTTP REST  
**Transport**: HTTP/1.1 (loopback — gateway and agent share a container)  
**Authentication**: Bearer token (`ACP_TOKEN` env var, same value as `GITAI_ACP_TOKEN`)  
**Format**: JSON (`common/spec/envelope.Event`)  

**Endpoint**: `POST /events/{source}`

**Event envelope fields**:

| Field | Type | Description |
|---|---|---|
| `source` | string | Gateway name from the Gosuto `gateways[].name` field |
| `type` | string | Event classifier, e.g. `cron.tick`, `webhook.delivery`, `imap.email` |
| `ts` | RFC 3339 | UTC timestamp at which the event was generated |
| `payload.message` | string | Human-readable message passed to the LLM as the user turn |
| `payload.data` | object | Optional structured metadata (not forwarded to LLM directly) |

---

## Deployment Models

### 1. Docker Compose MVP (Tuwunel — Recommended)

```
services:
  tuwunel:         # lightweight, single-binary Matrix homeserver
    image: tuwunel:latest
    # Federation OFF, registration OFF

  ruriko:
    image: ruriko:latest
    volumes:
      - ./data:/data
    environment:
      - MATRIX_HOMESERVER=http://tuwunel:8008
      - RURIKO_MASTER_KEY=...

  agent-saito:
    image: gitai:latest
    environment:
      - GITAI_AGENT_ID=saito

  agent-kairo:
    image: gitai:latest
    environment:
      - GITAI_AGENT_ID=kairo
```

**Pros**: Single command (`docker compose up -d`), Tuwunel is lightweight and
private, ACP stays inside the Docker network  
**Cons**: Single host, no HA

> Kuze is embedded in Ruriko in this model. Secret entry is done via one-time
> browser links that Ruriko issues. Secrets never pass through Matrix.

---

### 2. Kubernetes (Production)

```
Deployment: ruriko (replicas: 1, persistent volume)
Deployment: gitai-agent-1 (replicas: 1)
Deployment: gitai-agent-2 (replicas: 1)
Service: ruriko-control (ClusterIP)
Service: agent-control (ClusterIP, headless)
ConfigMap: gosuto-configs
Secret: agent-secrets
```

**Pros**: HA, scaling, orchestration  
**Cons**: Complexity

---

### 3. Raspberry Pi / Homelab (Self-Hosted)

```
systemd services:
  - ruriko.service
  - gitai@weatherbot.service
  - gitai@browserbot.service
```

**Pros**: Low cost, full control  
**Cons**: Limited resources, manual management

---

## Security Boundaries

### Trust Boundaries

1. **Matrix Protocol Boundary**
   - All external input crosses here
   - Authentication and authorization required

2. **Control Plane Boundary**
   - Ruriko has elevated privileges
   - Only admins can send commands

3. **Agent Runtime Boundary**
   - Agents are sandboxed (container or process isolation)
   - Limited capabilities defined by Gosuto

4. **Tool Execution Boundary**
   - MCP tools run in separate processes
   - Results filtered and redacted

### Defense Layers

1. **Identity**: Matrix authentication
2. **Authorization**: Gosuto policy rules
3. **Validation**: Schema validation, constraint checking
4. **Isolation**: Container/process boundaries
5. **Audit**: All actions logged
6. **Approval**: Human checkpoints for risky operations

---

## Scalability Considerations

### Current Design (MVP)

- Ruriko: Single instance (SQLite)
- Agents: Multiple instances, independent
- Matrix: Tuwunel homeserver (local, single-host)

**Bottlenecks**:
- SQLite write concurrency
- Ruriko single point of failure

### Future Scaling

- **Ruriko HA**: Leader election (etcd, Consul)
- **Database**: PostgreSQL or distributed SQLite (LiteFS)
- **Agent Mesh**: Agents already communicate peer-to-peer over Matrix; scaling is inherent
- **Sharding**: Group agents by function/team

---

## References

- [preamble.md](./preamble.md) - Product story, UX contract, and canonical glossary
- [invariants.md](./invariants.md) - System invariants
- [threat-model.md](./threat-model.md) - Security analysis
- [gosuto-spec.md](./gosuto-spec.md) - Policy specification
- [agent-envelope-spec.md](./agent-envelope-spec.md) - Agent communication protocol

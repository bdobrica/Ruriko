# Ruriko

> A conversational control plane for secure, capability-scoped AI agents over Matrix.

Ruriko is a self-hosted system where a human talks to **Ruriko** over Matrix, and Ruriko plans and configures specialized LLM-powered agents (**Gitai**) that collaborate **peer-to-peer** — messaging each other directly over Matrix, with secrets handled securely and control operations kept off the conversation layer.

> 📖 **Full product story, UX contract, and canonical glossary:** [docs/preamble.md](docs/preamble.md)

Instead of treating agents like chatbots, Ruriko treats them like **operated system components** — with lifecycle control, secret management, deterministic policies, and auditable tool access.

---

## ✨ Why Ruriko?

Most agent frameworks are:

* Python-first
* Developer-centric
* Monolithic
* Loosely guarded
* Hard to operate safely in production

Ruriko takes a different approach:

* 🔐 **Policy-first** – deterministic capability enforcement
* 🧱 **Immutable runtime** – agents cannot self-modify
* 🗝️ **Centralized secret management**
* 🔄 **Lifecycle control** – spawn, stop, respawn, update
* 🛰️ **Matrix-native communication** – humans and agents coexist naturally
* 🧩 **MCP-based tool ecosystem**
* 🐳 **Single binary agents** – easy to deploy anywhere

Ruriko is inspired by Kubernetes, MLOps control planes, and capability-based security models.

---

# 🏗 Architecture

Ruriko consists of two main components:

## 1️⃣ Ruriko (Control Plane)

* Plans workflows and drafts agent configurations
* Manages agent lifecycle
* Provisions Matrix accounts
* Stores and rotates secrets
* Applies and versions agent configuration (*Gosuto*)
* Defines the agent mesh topology (which agents can message which)
* Enforces administrative approvals
* Maintains audit logs
* Integrates with Codex to generate new agent templates

Ruriko is the **planner and policy authority**. It does **not** sit in the hot path of agent-to-agent collaboration — it plans the topology and agents execute peer-to-peer.

---

## 2️⃣ Gitai (Agent Runtime)

Each agent runs as a separate single binary:

* Connects to Matrix via `mautrix-go`
* **Sends messages to other agents and users via built-in Matrix messaging tool** (policy-gated)
* Communicates via structured message envelopes
* Calls LLM providers
* Manages and supervises MCP tool processes
* **Manages and supervises event gateway processes** (built-in cron/webhook, external binaries)
* Accepts inbound event triggers via ACP `POST /events/{source}` and routes them through the same policy → LLM → tool pipeline as Matrix messages
* Enforces policy locally
* Handles approvals
* Executes tool calls within strict constraints

Runtime is immutable. Behavior is controlled by structured configuration. Agents collaborate **peer-to-peer** over Matrix — Ruriko is not in the message path.

---

## 👻 Gosuto (Agent Configuration — Policy, Instructions, Persona)

Each agent is configured using a versioned YAML file called **Gosuto**.

Gosuto defines:

* Allowed rooms and senders
* Capability rules
* MCP server wiring
* **Event gateway wiring** (built-in cron/webhook, and external gateway binaries baked into the image)
* Tool allowlists and constraints
* Approval requirements
* Limits (rate, cost, concurrency, events-per-minute)
* Secret bindings
* **Messaging targets** — rooms the agent is allowed to send messages to (policy-gated, rate-limited)
* **Instructions** — operational workflow: role, workflow steps, peer and user awareness (auditable, versioned)
* **Persona** — cosmetic tone and style (non-authoritative)

The Gosuto authority model is three-tier: **Policy** (code-enforced) > **Instructions** (operational workflow) > **Persona** (cosmetic). Instructions cannot grant capabilities outside policy.

---

# 🔐 Security Model

Ruriko uses capability-based enforcement:

* All actions are evaluated against structured policy rules.
* First-match-wins rule evaluation.
* Default deny.
* Sensitive tool calls require explicit human approval.
* Secrets are **never** stored in Gosuto or sent through Matrix.
* Secrets are entered by humans via **Kuze** one-time secure links.
* Secrets are encrypted at rest (AES-GCM, master key from environment).
* Agents fetch secrets via one-time redemption tokens (never raw credentials).
* All actions are auditable and traceable.

Agents cannot:

* Modify their runtime
* Access secrets outside their scope
* Call tools not explicitly allowed
* Execute privileged operations without approval

---

# 🛡️ CI Security Automation

Security and supply-chain checks run automatically in GitHub Actions via `.github/workflows/security-supply-chain.yml`.

It runs on pull requests, pushes to `main`, and weekly on schedule, and includes:

* `govulncheck ./...` for Go dependency/runtime vulnerability detection
* CycloneDX SBOM generation (`ruriko-go-mod.cdx.json`)
* Trivy image scanning for `ruriko` and `gitai` (fails on `HIGH`/`CRITICAL`)

To retrieve SBOM output, open a workflow run in **Actions** and download the artifact named **`sbom-cyclonedx`**.

---

# 📡 Communication Model

Ruriko uses **three distinct channels**:

| Channel | Used for |
|---------|----------|
| **Matrix** (conversation) | Human ↔ Ruriko dialogue, **agent ↔ agent peer-to-peer collaboration**, audit breadcrumbs |
| **ACP** (Agent Control Protocol) | Lifecycle control, config apply, health checks, restarts, **inbound event delivery** (`POST /events/{source}`) — private to the Docker network |
| **Kuze** (secret plane) | One-time secret entry (human) and one-time secret redemption (agents) — never through Matrix |
| **Event Gateways** (inbound triggers) | Cron ticks, email arrivals, webhook deliveries — translated to event envelopes, posted to ACP |

This separation keeps the transcript meaningful and safe.

---

# 🧠 Tooling via MCP

Agents integrate with tools via the Model Context Protocol (MCP).

Examples:

* Browser automation (Playwright MCP)
* Weather APIs
* Scheduling
* File systems
* Custom enterprise connectors

MCP processes are supervised and reconciled by Gitai.

---

# 🔔 Inbound Event Gateways

Agents can be *woken* by external events rather than waiting for Matrix messages.

Gateway types:

* **Built-in Cron** — fires `cron.tick` events on any 5-field cron schedule (no external process needed)
* **Built-in Webhook** — receives HTTP POSTs proxied through Ruriko's rate-limited, HMAC-authenticated `/webhooks/{agent}/{source}` endpoint
* **External binaries** — compiled gateway processes baked into the Gitai Docker image (e.g. `ruriko-gw-imap` for email-reactive agents)

Gateways are wired in Gosuto under `gateways:` and are supervised identically to MCP processes — same credential management, same restart semantics, same audit trail. Events enter the same policy → LLM → tool pipeline as Matrix messages; prompt injection from external sources is mitigated by code-enforced policy.

---

# 🛠 Agent Templates

### Canonical agents

* **Saito Agent** – deterministic cron/trigger agent; fires periodic triggers and **sends Matrix messages to other agents** to initiate workflows (e.g., tells Kairo to check the portfolio). Singleton identity.
* **Kairo Agent** – finance and portfolio analysis via the Finnhub MCP; retrieves market data, analyses tickers, **delegates news lookups to Kumo** via Matrix, and delivers final reports to the user. Singleton identity.
* **Kumo Agent** – news and web search via the Brave Search MCP; **receives requests from Kairo via Matrix** and summarises news for tickers and topics. Singleton identity.

Canonical agents are **named singleton identities** with distinct personalities and roles — not interchangeable worker instances.

### Generic templates

* **Cron Agent** – scheduled checks and recurring tasks, woken by a built-in cron gateway
* **Email Agent** – email-reactive agent; monitors an IMAP mailbox via `ruriko-gw-imap` and acts on new messages
* **Browser Agent** – headless browsing with approval-gated navigation
* **Research Agent** – structured envelope-based task delegation

---

# 🚀 Deployment Philosophy

* Single command: `docker compose up -d`
* Bundled **Tuwunel** Matrix homeserver (federation OFF, registration OFF)
* Container-friendly
* Runs on:

  * Small VPS (1 CPU, 512MB RAM is sufficient)
  * Raspberry Pi
  * Homelab
  * Kubernetes (runtime adapter available)
* SQLite for state (WAL mode)
* No heavy external dependencies required

---

# 🧭 Project Goals

* Make agentic AI safe for non-programmers
* Provide operational guardrails by default
* Separate policy from prompt
* Enable distributed, small-footprint agents
* Avoid probabilistic control logic

---

# 🧪 Current Status

Core infrastructure and all agent primitives are complete. Working on the canonical end-to-end workflow and conversation memory.

Completed:

* [x] Ruriko core control plane (Matrix command router, agent inventory)
* [x] Gitai runtime (Matrix client, ACP server, policy engine)
* [x] Secret store + rotation (AES-GCM, per-agent scoping)
* [x] Gosuto versioned configuration
* [x] Approval workflow
* [x] MCP supervisor
* [x] Docker lifecycle control
* [x] Observability (structured logging, audit trail, trace IDs)
* [x] Tuwunel homeserver integration (Docker Compose + provisioning)
* [x] ACP authentication (bearer token, idempotency keys, per-op timeouts)
* [x] Kuze one-time secret entry and agent token redemption
* [x] Natural language interface — LLM-powered Matrix command translation (R9)
* [x] Event gateways — built-in cron/webhook, external binary gateways (R11–R13)
* [x] Automated provisioning pipeline — container → ACP health → Gosuto apply (R5)
* [x] Saito, Kairo, and Kumo canonical Gosuto templates
* [x] Gosuto persona / instructions separation — three-layer authority model (R14)
* [x] Built-in `matrix.send_message` tool — policy-gated peer-to-peer messaging (R15)
* [x] Mesh topology provisioning — Ruriko injects allowed messaging targets at provision time
* [x] Conversation memory — STM tracker, LTM interface, seal pipeline, context assembly, pluggable SQLite/OpenAI/LLM backends (R10)
* [x] NLP planning layer — canonical agent role knowledge, multi-step plan intent, cron mapping, ID sanitisation, conversation history, re-query retries (R16)

In progress / up next:

* [ ] Canonical Saito → Kairo → Kumo end-to-end workflow (R6)
* [ ] Gosuto template variable customization at provision time (R17)

---

# 💡 Inspiration

Ruriko is influenced by:

* Kubernetes control plane patterns
* MLOps lifecycle management
* Capability-based security systems
* Service meshes
* Matrix federation architecture

---

# 📜 License

[Apache 2.0](./LICENSE)
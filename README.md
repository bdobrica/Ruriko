# Ruriko

> A conversational control plane for secure, capability-scoped AI agents over Matrix.

Ruriko is a self-hosted system where a human talks to **Ruriko** over Matrix, and Ruriko coordinates specialized LLM-powered agents (**Gitai**) that collaborate like a small team — with secrets handled securely and control operations kept off the conversation layer.

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

* Manages agent lifecycle
* Provisions Matrix accounts
* Stores and rotates secrets
* Applies and versions agent configuration (*Gosuto*)
* Enforces administrative approvals
* Maintains audit logs
* Integrates with Codex to generate new agent templates

Ruriko is deterministic. It does **not** rely on LLM output for control decisions.

---

## 2️⃣ Gitai (Agent Runtime)

Each agent runs as a separate single binary:

* Connects to Matrix via `mautrix-go`
* Communicates via structured message envelopes
* Calls LLM providers
* Manages and supervises MCP tool processes
* Enforces policy locally
* Handles approvals
* Executes tool calls within strict constraints

Runtime is immutable. Behavior is controlled by structured configuration.

---

## 👻 Gosuto (Agent Persona & Policy)

Each agent is configured using a versioned YAML file called **Gosuto**.

Gosuto defines:

* Allowed rooms and senders
* Capability rules
* MCP server wiring
* Tool allowlists and constraints
* Approval requirements
* Limits (rate, cost, concurrency)
* Secret bindings
* Persona and style (non-authoritative)

Policy is deterministic. Persona is cosmetic.

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

# 📡 Communication Model

Ruriko uses **three distinct channels**:

| Channel | Used for |
|---------|----------|
| **Matrix** (conversation) | Human ↔ Ruriko dialogue, agent ↔ agent discussion, audit breadcrumbs |
| **ACP** (Agent Control Protocol) | Lifecycle control, config apply, health checks, restarts — private to the Docker network |
| **Kuze** (secret plane) | One-time secret entry (human) and one-time secret redemption (agents) — never through Matrix |

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

# 🛠 Example Agent Templates

* **Cron Agent** – scheduled checks and recurring tasks
* **Browser Agent** – headless browsing with approval-gated navigation
* **Research Agent** – structured envelope-based task delegation
* **Notification Agent** – policy-controlled outbound messaging

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

Core infrastructure is complete. Working on MVP canonical workflow.

Completed:

* [x] Ruriko core control plane (Matrix command router, agent inventory)
* [x] Gitai runtime (Matrix client, ACP server, policy engine)
* [x] Secret store + rotation (AES-GCM, per-agent scoping)
* [x] Gosuto versioned configuration
* [x] Approval workflow
* [x] MCP supervisor
* [x] Docker lifecycle control
* [x] Observability (structured logging, audit trail, trace IDs)

In progress:

* [ ] Tuwunel homeserver integration
* [ ] ACP authentication (mTLS)
* [ ] Kuze one-time secret entry (browser link)
* [ ] Saito / Kairo / Kumo canonical agents

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
# Ruriko

> A distributed control plane for secure, capability-scoped AI agents running over Matrix.

Ruriko is a lightweight, self-hosted infrastructure for running and managing AI agents as first-class services.

Instead of treating agents like chatbots, Ruriko treats them like **operated systems components** — with lifecycle control, secret management, deterministic policies, and auditable tool access.

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
* Secrets are never stored in Gosuto.
* Secrets are encrypted at rest.
* All actions are auditable and traceable.

Agents cannot:

* Modify their runtime
* Access secrets outside their scope
* Call tools not explicitly allowed
* Execute privileged operations without approval

---

# 📡 Communication Model

Ruriko uses Matrix as:

* Identity layer
* Message bus
* Human + agent collaboration channel
* Approval workflow surface

Agents communicate using structured JSON envelopes embedded in Matrix messages.
Human-friendly chat remains readable, but machine decisions are deterministic.

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

* Single binary
* Container-friendly
* Runs on:

  * Raspberry Pi
  * Small VMs
  * Kubernetes
  * Homelabs
* SQLite for state
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

Early-stage infrastructure project.

Planned milestones:

* [ ] Ruriko core control plane
* [ ] Gitai runtime v1
* [ ] Secret store + rotation
* [ ] Policy engine
* [ ] Approval workflow
* [ ] MCP supervisor
* [ ] Codex template generation

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
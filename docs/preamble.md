
# Ruriko: Product Story, UX Contract, and Architecture Alignment (MVP)

## Why this project exists

Ruriko is a self-hosted, conversational control-plane for secure agentic automation. A human user talks to Ruriko over Matrix, and Ruriko plans and configures a set of specialized LLM-powered agents ("Gitai") that collaborate **peer-to-peer** like a small team — messaging each other directly over Matrix to accomplish tasks.

The goal is to make agentic AI usable by regular non-technical people:

* setup happens through dialogue, not YAML and CLI spelunking
* the user can observe what agents are doing and intervene when needed
* the system is safe by default (secrets and permissions are handled correctly)

This project is explicitly designed to avoid the two common failures of agent systems:

1. insecure “just give the LLM your credentials” workflows
2. overly complex “distributed orchestration frameworks” that are hard to run and debug

---

## The core UX contract

The system behaves like a small team of virtual humans:

* The user speaks to **Ruriko** as the main entry point.
* Ruriko plans the workflow, spawns/configures specialist agents ("Gitai"), and drafts their personalities, instructions, and mesh topology.
* Agents execute **peer-to-peer**: they message each other directly over Matrix using a built-in messaging tool, without Ruriko relaying every interaction.
* Ruriko remains the **policy authority** — it defines what agents are allowed to do, which agents exist, and which rooms they can message — but it does not sit in the hot path of agent-to-agent collaboration.
* The user can read the collaboration, correct assumptions, and stop tasks.
* Smart (LLM-powered) agents can also be interacted with directly by the user — they are not exclusively controlled through Ruriko.

However, the system is **not** a chat-based DevOps tool. We keep a strict separation between:

* human-facing conversation (Matrix)
* operational control (ACP)
* secret handling (Kuze)

This separation is what makes the system both usable *and* safe.

---

## Canonical user story (the reference workflow)

Bogdan (the user) wants a team of agents to manage and explain his stock portfolio.

**Bogdan → Ruriko (Matrix DM):**
"Hi Ruriko. Can you set up Saito so that every 15 minutes he asks Kairo to check my stock portfolio and report to you? I want long-term optimization, but also event speculation. If Kairo finds something interesting, ask Kumo to pull news for those companies, then ask Kairo again to adjust his findings and report back."

Ruriko responds by creating and configuring the relevant agents:

* **Saito** (cron / trigger agent): emits periodic reminders
* **Kairo** (finance agent): portfolio analysis, uses finnhub + DB MCP
* **Kumo** (news agent): searches news for relevant tickers/topics

A typical cycle looks like:

1. **Saito** triggers a scheduled check (cron tick).
2. Saito sends a Matrix message to **Kairo**: "Time for a portfolio check."
3. Kairo retrieves portfolio data and market state, writes analysis to the DB.
4. Kairo sends a Matrix message to **Kumo**: "Pull news for these tickers: AAPL, MSFT, TSLA."
5. Kumo searches for news and sends results back to **Kairo** via Matrix.
6. Kairo revises analysis based on news and sends a final report to **Bogdan** via Matrix DM.

Note the **peer-to-peer** nature: Ruriko is not in the hot loop. Ruriko planned this topology (which agents exist, what they are allowed to do, which rooms they can message), but the agents execute collaboratively by messaging each other directly.

Agents can ask Bogdan clarifying questions directly when needed (e.g., missing portfolio). Ruriko remains the **planner and policy authority** — it drafted the agent configurations, knows the mesh topology, and can intervene or reconfigure at any time.

Canonical agents (**Saito**, **Kairo**, **Kumo**) are **singleton identities** — each has a distinct personality, role, and set of capabilities. They are not interchangeable worker instances; they are named team members.

This scenario is the **canonical reference**: architecture decisions and implementation must support this flow without requiring the user to become a sysadmin.

---

## Deployment model and threat model (MVP)

The MVP is designed for a single-host deployment:

* One VPS (e.g., Hetzner)
* A Matrix homeserver exposed to the internet so the human can connect
* Matrix federation disabled
* Matrix registration disabled (accounts provisioned explicitly)
* Ruriko, Gitai agents, and supporting services run on the same host via Docker Compose
* Agent control endpoints are reachable only inside the Docker network

This model optimizes for:

* 1-command installation (`docker compose up -d`)
* minimal operational complexity
* strong security defaults

---

## Architectural decisions (non-negotiable)

### 1) Matrix is the conversation layer (not the control plane)

Matrix is used for:

* user ↔ Ruriko conversation
* agent ↔ agent **peer-to-peer collaboration** (task delegation, result sharing, clarifications)
* agent → user direct messages (reports, questions)
* audit breadcrumbs (non-sensitive state changes)

**Built-in Matrix messaging** is a first-class tool available to all LLM-powered Gitai agents. Agents can send messages to other agents' rooms or to the user, subject to Gosuto policy (room allowlists, rate limits). This is what enables the peer-to-peer collaboration model.

Matrix is not used for:

* secrets exchange
* agent lifecycle control
* synchronous health/status checks
* configuration push

This keeps the transcript meaningful and safe.

---

### 2) ACP is the control plane transport (agent lifecycle + config + health)

Each Gitai agent exposes a private ACP endpoint inside the Docker network.

ACP is used for:

* health checks
* status queries
* config apply (Gosuto updates)
* process restart/shutdown
* task cancellation / suspension

ACP is intentionally synchronous and operationally boring, so Ruriko can manage agents reliably without inventing a complex protocol inside Matrix.

ACP must be:

* authenticated and encrypted (mTLS preferred)
* not exposed publicly
* idempotent for all control operations

---

### 3) Kuze is the secret plane (one-time entry + controlled redemption)

Secrets must never appear in Matrix history.

Kuze is embedded in Ruriko and is used for:

* human → Ruriko secret entry via one-time secure link
* agent secret redemption via one-time tokens (end-state)

Secrets are:

* encrypted at rest
* never logged
* never sent to LLMs
* never placed into chat transcripts

This preserves a conversational setup flow for non-technical users while keeping the system secure.

---

### 4) Observability without leakage

The system remains observable because:

* agents discuss tasks in Matrix like humans
* Ruriko posts audit breadcrumbs to Matrix (hashes, IDs, summaries)
* control actions happen via ACP but are mirrored to Matrix as non-sensitive breadcrumbs
* secrets are represented only as references/tokens, never as values

Users can see what happened without being exposed to secret material.

---

## Design north stars

* **Conversation-first:** everything important should be explainable in chat.
* **No secrets in chat:** never, under any circumstance.
* **Boring control plane:** ACP must be reliable, authenticated, idempotent.
* **Non-technical friendly:** setup must not require engineering expertise.
* **Agents are constrained:** tools, permissions, and secrets are least-privilege by design.
* **Future-proofing:** ACP is an interface; it can later be rewired to reverse RPC without changing orchestration logic.

---

## Glossary (canonical terms)

**Ruriko (Control Plane)**
The main system agent and point of entry. Ruriko talks to the user over Matrix, manages agent lifecycle, applies configuration, enforces permissions, coordinates multi-agent workflows, and owns the system state. Ruriko is the “manager” and policy authority.

**Gitai (Agent Runtime)**
An individual LLM-powered worker agent. Each Gitai runs as its own process/container and can perform tasks, use tools, talk over Matrix, and report results. Gitai are “hollow bodies” that become useful once configured.

**Gosuto (Persona + Tool Profile)**
The agent’s personality + role + tool permissions packaged as configuration. A Gosuto defines what a Gitai is allowed to do (MCPs, tools, DB access, prompts, constraints). In practice: the “job description” and guardrails for an agent.

**Saito (Cron / Trigger Agent)**
A canonical singleton Gitai identity responsible for scheduling and emitting periodic triggers. Saito fires cron events and **sends Matrix messages to other agents** to initiate workflows. Saito is intentionally deterministic and low-intelligence: it should not reason deeply, only schedule and coordinate.

**Kairo (Finance Agent)**
A canonical singleton Gitai identity responsible for portfolio analysis, market data retrieval (e.g., finnhub MCP), and writing structured findings into the database. Kairo can ask the user for missing inputs, **delegate sub-tasks to other agents via Matrix messages** (e.g., asking Kumo for news), and produces reports that are sent directly to the user.

**Kumo (News/Search Agent)**
A canonical singleton Gitai identity responsible for retrieving news and public information (e.g., Brave Search API/MCP) related to tickers, companies, or topics. Kumo **receives requests from other agents via Matrix** and sends results back to the requesting agent or to the user.

**Matrix (Conversation Layer)**
The communication fabric used for human interaction and **agent-to-agent peer-to-peer collaboration**. All LLM-powered agents have a built-in Matrix messaging tool that lets them send messages to other agents' rooms or to the user — subject to Gosuto policy constraints (room allowlists, rate limits). Matrix carries task delegation, result sharing, and non-sensitive audit breadcrumbs. It must never carry secret values.

**Tuwunel (Bundled Matrix Homeserver)**
The default Matrix homeserver used for the MVP deployment. It is lightweight, single-binary, and intended to run on the same host as Ruriko and the agents. Federation and registration are disabled by default.

**ACP — Agent Control Protocol (Control Plane Transport)**
A private HTTP-based control interface exposed by each Gitai agent inside the Docker network. Ruriko uses ACP for synchronous operations: health checks, status, config apply, restart/shutdown, and task cancellation. ACP is authenticated and not exposed publicly.

**Kuze (Secret Plane / Secret UX)**
A service embedded into Ruriko that handles secure secret entry and secret distribution without exposing secrets in Matrix history. Kuze provides one-time links for humans to submit secrets and one-time redemption tokens for agents to fetch secrets securely.

**Secret Store / Keystore**
The encrypted storage used by Ruriko to persist secrets at rest. It is protected by a master key provided at runtime (environment/config). Secrets are referenced by name (secret refs) and are never logged or sent to LLMs.

**MCP (Model Context Protocol) Connector**
A tool integration mechanism that lets agents access external services (e.g., finnhub, database, brave search) through a controlled interface. MCP access is governed by the agent’s Gosuto.

**Audit Breadcrumbs**
Short, non-sensitive status messages posted to Matrix to preserve observability. Examples: “Kairo config applied (hash=…)”, “Task started”, “Task completed”, “Issued secret token (ttl=60s)”. Breadcrumbs must never contain secret values.

**Provisioning**
The process by which Ruriko creates/configures agents and (if applicable) their Matrix accounts, applies Gosuto configuration, and ensures they are online and healthy.

**Single-host MVP Topology**
The initial deployment model: Ruriko, Kuze, Gitai agents, and the Matrix homeserver run on the same VPS under Docker Compose. Matrix is exposed to the internet for the human user; ACP is private to the Docker network.

**Reverse RPC Broker (Future)**
A future replacement for ACP’s direct HTTP control, where agents establish outbound persistent connections to a broker/gateway. This enables agents to run behind NAT or on remote devices without inbound connectivity. Not part of the MVP.

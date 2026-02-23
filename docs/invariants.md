# Ruriko System Invariants

> **The immutable principles that govern Ruriko's architecture and behavior.**

These invariants are the foundation of Ruriko's design. They must never be violated. When in doubt about implementation decisions, refer back to these principles.

**Last Updated**: 2026-02-23  
**Status**: Living Document (changes require architecture review)

---

## Core Invariants

### 1. Deterministic Control Boundary

**Principle**: Control decisions are NEVER made by LLM output.

**What this means**:
- Ruriko's command parser is deterministic (no LLM interpretation)
- Agent lifecycle operations (spawn/stop/restart) are triggered by explicit commands
- Secret management operations are never inferred from natural language
- Policy evaluation is rule-based, not probabilistic

**Rationale**: LLMs can be unpredictable. Critical infrastructure operations must be deterministic and auditable.

**Enforcement**:
- ✅ Use structured command syntax: `/ruriko <command> <args>`
- ✅ Parse commands with regex/parser, not LLM
- ✅ Validate all parameters before execution
- ❌ Never pass user text directly to lifecycle operations
- ❌ Never let LLM output determine if an action should execute

---

### 2. Policy > Instructions > Persona

**Principle**: Gosuto enforces a strict three-layer authority model. Policy is
authoritative and enforced by code. Instructions are operational and auditable.
Persona is cosmetic and non-authoritative.

```
┌─────────────────────────────────────────────────────────────┐
│  POLICY (authoritative)                                      │
│  What the agent is *allowed* to do — enforced by code.       │
│  Defined by: capabilities[], approvals, limits               │
│  Changeable by: Ruriko operators only (via ACP)              │
├─────────────────────────────────────────────────────────────┤
│  INSTRUCTIONS (operational)                                  │
│  What the agent *should* do — auditable workflow logic.      │
│  Defined by: instructions.role, workflow, context            │
│  Changeable by: Ruriko (versioned + diffable in Gosuto)      │
├─────────────────────────────────────────────────────────────┤
│  PERSONA (cosmetic)                                          │
│  How the agent *sounds* — tone, style, name.                 │
│  Defined by: persona.systemPrompt, model, temperature        │
│  Changeable by: Ruriko operators (no security impact)        │
└─────────────────────────────────────────────────────────────┘
```

**What this means**:
- An agent's capabilities are defined by Gosuto policy, not by prompt engineering
- No amount of prompt injection — including via instructions — can grant
  capabilities outside the Gosuto capability rules
- Instructions define operational workflow (trigger → action) and peer awareness;
  they are injected into the LLM system prompt but cannot supersede policy
- Instructions cannot reference tools the agent is not permitted to use:
  if workflow steps mention an MCP server excluded from capabilities, the agent
  will be denied at runtime — this is flagged as a warning at validation time
- Instructions are versioned and diffable as part of the Gosuto, making all
  workflow logic changes auditable alongside capability changes
- Persona text (tone, style, name) is for UX only and has no security impact

**Rationale**: Trust must be based on enforced rules, not on instructing an LLM
to "be careful." Instructions improve agent coherence and auditability but must
not be confused with a security boundary — only Policy is authoritative.

**Enforcement**:
- ✅ Policy evaluation happens BEFORE the LLM sees requests
- ✅ Tool calls are gated by explicit Gosuto capability rules regardless of instructions
- ✅ Default deny: if not in allowlist, operation is blocked
- ✅ Instructions are versioned with the Gosuto (same hash + diff pipeline as capabilities)
- ✅ `gosuto.Warnings()` flags workflow steps that reference MCP servers not covered by an allow rule
- ❌ Never rely on system prompts or instructions for security enforcement
- ❌ Never bypass policy checks based on LLM reasoning or instruction content
- ❌ Instructions cannot expand capabilities: they are advisory, not authoritative

---

### 3. Immutable Runtime

**Principle**: Gitai agent runtime is immutable and cannot self-modify.

**What this means**:
- Agents cannot edit their own binary
- Agents cannot modify their policy (Gosuto)
- Agents cannot escalate their own privileges
- Behavior changes require external control (Ruriko)

**Rationale**: Self-modifying agents can escape their security boundaries.

**Enforcement**:
- ✅ Gitai binary is read-only in container
- ✅ Gosuto updates come only from Ruriko via Agent Control Protocol
- ✅ Runtime restarts to apply new Gosuto (hot-reload with validation)
- ❌ Agents never write to their own config files
- ❌ Agents never execute arbitrary code from untrusted sources

---

### 4. No Root Secrets

**Principle**: Agents receive only scoped, leased secrets—never root credentials.

**What this means**:
- Agents don't have access to Ruriko's master encryption key
- Agents receive only the specific secrets bound to them
- Secrets can be rotated and invalidated by Ruriko
- Agent compromise doesn't compromise the entire system

**Rationale**: Principle of least privilege. Limit blast radius of compromise.

**Enforcement**:
- ✅ Secrets are bound per-agent in `agent_secret_bindings`
- ✅ Ruriko pushes only authorized secrets to each agent
- ✅ Secrets are encrypted with agent-specific keys (or scope-limited)
- ❌ Agents never have access to other agents' secrets
- ❌ Agents never have access to Ruriko's database encryption key

---

### 5. Explicit Approval for Destructive Operations

**Principle**: All destructive or high-risk operations require explicit human approval.

**What this means**:
- Deleting agents requires approval
- Deleting or rotating critical secrets requires approval
- Enabling risky tool capabilities (browser, shell, filesystem write) requires approval
- Approval is a structured workflow, not a confirmation prompt

**Rationale**: Prevent accidents and provide a human checkpoint for safety.

**Enforcement**:
- ✅ Approvals are stored as immutable objects with TTL
- ✅ Approval decisions are parsed deterministically
- ✅ Only authorized approvers can approve/deny
- ✅ Expired approvals are automatically denied
- ❌ Never bypass approvals based on trust or convenience
- ❌ Never infer approval from conversational "yes"

---

### 6. Audit Everything

**Principle**: All actions are logged with trace correlation.

**What this means**:
- Every command executed by Ruriko is logged
- Every tool call by agents is logged
- Every approval request/decision is logged
- Logs include: actor, action, target, timestamp, trace_id, result

**Rationale**: Accountability, debugging, security incident response.

**Enforcement**:
- ✅ Write to `audit_log` table before and after critical operations
- ✅ Generate and propagate `trace_id` for request correlation
- ✅ Structured logging with consistent fields
- ✅ Optional: emit audit events to Matrix audit room
- ❌ Never skip audit logging for "quick" operations
- ❌ Never log raw secrets (redact aggressively)

---

### 7. Fail Safely

**Principle**: When in doubt, refuse the operation.

**What this means**:
- Errors result in denial, not silent failures
- Ambiguous requests are rejected, not guessed
- Validation failures block execution
- Missing permissions block execution

**Rationale**: Better to refuse an action than to execute it incorrectly.

**Enforcement**:
- ✅ Default deny for all policy rules
- ✅ Strict schema validation for Gosuto and envelopes
- ✅ Fail-fast on parse errors
- ✅ Clear error messages to users
- ❌ Never fall back to permissive behavior on error
- ❌ Never silently ignore validation failures

---

### 8. Secrets Never Leak

**Principle**: Secrets are never exposed in logs, Matrix messages, or tool traces.

**What this means**:
- Raw secrets never appear in audit logs
- Raw secrets never get sent to Matrix rooms
- Raw secrets never appear in LLM prompts (unless explicitly allowed)
- Tool results containing secrets are redacted

**Rationale**: Prevent accidental exposure of credentials.

**Enforcement**:
- ✅ Implement redaction middleware for logs
- ✅ Redact secrets in tool call traces
- ✅ Secrets are referenced by name, not value
- ✅ Use special handling for secret injection into tools
- ❌ Never print secrets for debugging
- ❌ Never include secrets in error messages

---

### 9. Trust Contexts Are Explicit

**Principle**: Room/sender allowlists are enforced before processing.

**What this means**:
- Agents only process messages from allowed rooms/senders
- Ruriko only accepts commands from authorized Matrix users
- Trust decisions are based on identity, not content
- E2EE requirements are enforced (if configured)

**Rationale**: Defense in depth. Prevent unauthorized access.

**Enforcement**:
- ✅ Check sender MXID against allowlist before processing
- ✅ Check room ID against allowlist before processing
- ✅ Verify E2EE status if required by Gosuto
- ✅ Log unauthorized access attempts
- ❌ Never process commands from unknown senders
- ❌ Never bypass trust checks based on message content

---

### 10. Configuration is Versioned and Auditable

**Principle**: Gosuto changes are versioned, diffable, and auditable.

**What this means**:
- Every Gosuto change gets a version number and hash
- Previous versions are retained for rollback
- Changes are attributed to a specific actor
- Diffs are available for review

**Rationale**: Transparency, accountability, rollback capability.

**Enforcement**:
- ✅ Store all Gosuto versions in `gosuto_versions` table
- ✅ Compute SHA-256 hash for integrity
- ✅ Track `created_by_mxid` for accountability
- ✅ Implement rollback command
- ❌ Never overwrite previous versions
- ❌ Never apply Gosuto without versioning

---

### 11. Secrets Are Never Entered via Matrix

**Principle**: Matrix is a conversation channel. It is not a secure channel for
secret material. No secret value must ever travel through Matrix in any direction.

**What this means**:
- Users **never paste secret values** (API keys, tokens, passwords) into Matrix chat
- The `/ruriko secrets set <name>` command issues a one-time Kuze link and never accepts an inline value
- Agents never receive raw secret values over Matrix
- Ruriko must refuse to process messages that look like secret material (pattern matching on known formats)

**Rationale**: Matrix messages are stored in the homeserver database and may be
readable by homeserver admins, leaked in backups, or cached by clients.
Even in a single-host deployment the principle must hold: Matrix history
is not a secrets vault.

**Enforcement**:
- ✅ `/ruriko secrets set <name>` replies with a Kuze one-time link, not a prompt for inline input
- ✅ Kuze tokens are single-use with a short TTL (5–10 minutes)
- ✅ Incoming messages matching common secret patterns are rejected with a guidance reply
- ✅ Secrets are confirmed by reference only ("Secret 'openai_api_key' stored") 
- ❌ Never prompt the user to type a secret into Matrix
- ❌ Never echo, confirm, or repeat a secret value in Matrix
- ❌ Never store a Matrix message body that contains a secret reference value

---

### 12. Inter-Agent Communication Is Policy-Gated

**Principle**: Agents can only message rooms explicitly allowed by their Gosuto policy.
Ruriko defines the mesh topology; agents cannot expand it.

**What this means**:
- The built-in `matrix.send_message` tool is subject to Gosuto capability rules
- Agents can only send messages to rooms listed in their outbound messaging allowlist
- The agent mesh topology (which agents can talk to which) is defined by Ruriko at provision time
- Rate limits on outbound messages prevent amplification spirals
- Inter-agent messages are treated as untrusted input by the receiving agent (same threat model as user messages)

**Rationale**: Unrestricted inter-agent communication creates prompt injection and
amplification attack surfaces. The mesh must be explicitly defined and policy-enforced.

**Enforcement**:
- ✅ `matrix.send_message` tool gated by Gosuto `capabilities` rules
- ✅ Target room validated against an allowlist before sending
- ✅ Per-agent rate limits on outbound messages
- ✅ Receiving agent applies its own full policy evaluation on incoming messages
- ✅ All inter-agent messages audit logged (source, target room, trace ID)
- ❌ Agents never discover or message rooms outside their Gosuto allowlist
- ❌ Agents never relay messages to bypass another agent's policy constraints

---

## Enforcement Checklist

When implementing a new feature, verify:

- [ ] Does this feature respect the deterministic control boundary?
- [ ] Is policy enforcement separate from LLM logic? (Policy > Instructions > Persona)
- [ ] Can agents self-modify in any way? (Should be NO)
- [ ] Are secrets scoped appropriately?
- [ ] Does this require approval? (If destructive/risky: YES)
- [ ] Is it audited? (Should be YES)
- [ ] What happens on error? (Should fail safely)
- [ ] Can secrets leak? (Should be NO)
- [ ] Could this feature cause a secret value to appear in a Matrix room or message? (Should be NO)
- [ ] Are trust contexts checked? (Should be YES)
- [ ] Is configuration versioned? (Should be YES)
- [ ] Does this feature allow inter-agent messaging? If so, is it policy-gated with room allowlists and rate limits? (Should be YES)

---

## Consequences of Violations

Violating these invariants can lead to:

- Security vulnerabilities
- Unpredictable system behavior
- Loss of auditability
- Privilege escalation
- Secret exposure
- Loss of control over agents

**When faced with a tradeoff between convenience and invariants, choose invariants.**

---

## Amendment Process

These invariants can only be changed through:

1. Architecture review
2. Threat model re-evaluation
3. Explicit documentation of the change and rationale
4. Update of this document with clear justification

Invariants should be stable. Frequent changes indicate architectural problems.

---

## References

- [preamble.md](./preamble.md) - Product story, glossary, and architectural decisions
- [RURIKO_COMPONENTS.md](../RURIKO_COMPONENTS.md) - Implementation details
- [architecture.md](./architecture.md) - System architecture
- [threat-model.md](./threat-model.md) - Security analysis
- [gosuto-spec.md](./gosuto-spec.md) - Policy specification

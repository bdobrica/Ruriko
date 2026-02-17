# Ruriko System Invariants

> **The immutable principles that govern Ruriko's architecture and behavior.**

These invariants are the foundation of Ruriko's design. They must never be violated. When in doubt about implementation decisions, refer back to these principles.

**Last Updated**: 2026-02-17  
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

### 2. Policy > Persona

**Principle**: Gosuto's structured policy is authoritative; persona is cosmetic.

**What this means**:
- An agent's capabilities are defined by Gosuto YAML, not by prompt engineering
- No amount of prompt injection can grant capabilities outside Gosuto rules
- Persona text is for UX only; it doesn't affect security boundaries

**Rationale**: Trust must be based on enforced rules, not on instructing an LLM to "be careful."

**Enforcement**:
- ✅ Policy evaluation happens BEFORE LLM sees requests
- ✅ Tool calls are gated by explicit Gosuto capability rules
- ✅ Default deny: if not in allowlist, operation is blocked
- ❌ Never rely on system prompts for security
- ❌ Never bypass policy checks based on LLM reasoning

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

## Enforcement Checklist

When implementing a new feature, verify:

- [ ] Does this feature respect the deterministic control boundary?
- [ ] Is policy enforcement separate from LLM logic?
- [ ] Can agents self-modify in any way? (Should be NO)
- [ ] Are secrets scoped appropriately?
- [ ] Does this require approval? (If destructive/risky: YES)
- [ ] Is it audited? (Should be YES)
- [ ] What happens on error? (Should fail safely)
- [ ] Can secrets leak? (Should be NO)
- [ ] Are trust contexts checked? (Should be YES)
- [ ] Is configuration versioned? (Should be YES)

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

- [RURIKO_COMPONENTS.md](../RURIKO_COMPONENTS.md) - Implementation details
- [architecture.md](./architecture.md) - System architecture
- [threat-model.md](./threat-model.md) - Security analysis
- [gosuto-spec.md](./gosuto-spec.md) - Policy specification

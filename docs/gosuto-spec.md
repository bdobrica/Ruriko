# Gosuto v1 Specification

> **Gosuto** (後藤, "after the path") — the versioned configuration document that governs an agent's trust boundaries, capabilities, persona and operational limits.

---

## Overview

Each Gitai agent is configured by a single YAML file called its **Gosuto config**. Gosuto is:

- **Versioned** — every change is hashed and stored by Ruriko; rollbacks are first-class.
- **Policy-first** — capability rules are deterministic and enforced by Gitai at runtime.
- **Secret-free** — secrets are never stored in Gosuto. They are distributed separately via the ACP.
- **Mutable** — operators push new versions via `/ruriko gosuto set`; the runtime receives them over ACP.

---

## File Format

```yaml
apiVersion: gosuto/v1
metadata:
  name: <agent-id>
  template: <template-name>        # informational only
  description: <free text>

trust: { ... }
limits: { ... }                    # optional
capabilities: [ ... ]              # optional; default-deny if omitted
approvals: { ... }                 # optional
mcps: [ ... ]                      # optional
secrets: [ ... ]                   # optional
persona: { ... }                   # optional
```

---

## Fields

### `apiVersion` *(required)*

Must be exactly `"gosuto/v1"`.

---

### `metadata` *(required)*

| Field         | Type   | Required | Description                            |
|---------------|--------|----------|----------------------------------------|
| `name`        | string | ✅       | Agent identifier (matches Ruriko agent ID) |
| `template`    | string | ❌       | Template the config was derived from   |
| `description` | string | ❌       | Human-readable purpose                 |

---

### `trust` *(required)*

Defines the Matrix rooms and senders the agent pays attention to.

| Field            | Type     | Required | Description                                          |
|------------------|----------|----------|------------------------------------------------------|
| `allowedRooms`   | []string | ✅       | Room IDs (`!id:server`) or `"*"` for all rooms       |
| `allowedSenders` | []string | ✅       | User MXIDs (`@user:server`) or `"*"` for all senders |
| `requireE2EE`    | bool     | ❌       | Refuse to operate in non-encrypted rooms             |
| `adminRoom`      | string   | ❌       | Matrix room used for operator control messages       |

**Validation rules:**
- `allowedRooms` entries must start with `!` or be `"*"`.
- `allowedSenders` entries must start with `@` or be `"*"`.
- Both lists must contain at least one entry.

---

### `limits` *(optional)*

Resource and cost guardrails.

| Field                   | Type    | Default | Description                              |
|-------------------------|---------|---------|------------------------------------------|
| `maxRequestsPerMinute`  | int     | 0 (∞)   | Max LLM calls per minute                 |
| `maxTokensPerRequest`   | int     | 0 (∞)   | Max tokens per single LLM call           |
| `maxConcurrentRequests` | int     | 0 (∞)   | Max simultaneous in-flight requests      |
| `maxMonthlyCostUSD`     | float64 | 0 (∞)   | Monthly LLM spend cap in USD             |

---

### `capabilities` *(optional)*

Ordered list of capability rules. Evaluated **first-match-wins**. If no rule matches, the default policy is **DENY**.

Each rule:

| Field             | Type              | Required | Description                                            |
|-------------------|-------------------|----------|--------------------------------------------------------|
| `name`            | string            | ✅       | Human-readable rule label                              |
| `mcp`             | string            | ❌       | MCP server name to match, or `"*"` for all             |
| `tool`            | string            | ❌       | Tool name within the MCP, or `"*"` for all             |
| `allow`           | bool              | ✅       | `true` = allow; `false` = deny                         |
| `requireApproval` | bool              | ❌       | Gate the invocation behind human approval even if allowed |
| `constraints`     | map[string]string | ❌       | Key-value restrictions on tool arguments               |

**Example:**

```yaml
capabilities:
  - name: allow-web-search
    mcp: brave-search
    tool: "*"
    allow: true

  - name: deny-browser-write
    mcp: playwright
    tool: page_fill
    allow: false

  - name: approve-navigation
    mcp: playwright
    tool: navigate
    allow: true
    requireApproval: true

  - name: default-deny
    mcp: "*"
    tool: "*"
    allow: false
```

---

### `approvals` *(optional)*

Configuration for the human approval workflow.

| Field        | Type     | Default | Description                                  |
|--------------|----------|---------|----------------------------------------------|
| `enabled`    | bool     | false   | Activate the approval workflow               |
| `room`       | string   | —       | Matrix room where approval requests are sent |
| `approvers`  | []string | —       | List of approver MXIDs                       |
| `ttlSeconds` | int      | 3600    | Approval TTL in seconds; 0 → 1 hour          |

---

### `mcps` *(optional)*

List of MCP server processes the Gitai runtime will supervise.

| Field         | Type              | Required | Description                              |
|---------------|-------------------|----------|------------------------------------------|
| `name`        | string            | ✅       | Unique name for this MCP within the agent |
| `command`     | string            | ✅       | Binary path or name to execute            |
| `args`        | []string          | ❌       | Command-line arguments                   |
| `env`         | map[string]string | ❌       | Additional environment variables         |
| `autoRestart` | bool              | ❌       | Restart the MCP if it exits unexpectedly |

---

### `secrets` *(optional)*

References to Ruriko secrets the agent expects to be injected at runtime. Secret *values* are never stored in Gosuto. Ruriko distributes them via ACP `/secrets/apply`.

| Field     | Type   | Required | Description                                       |
|-----------|--------|----------|---------------------------------------------------|
| `name`    | string | ✅       | Secret name in the Ruriko store                   |
| `envVar`  | string | ❌       | Environment variable to inject the value into      |
| `required`| bool   | ❌       | Refuse to start if this secret is missing          |

---

### `persona` *(optional)*

LLM persona configuration. **Non-authoritative** — all access control is enforced via capability rules, not the system prompt.

| Field          | Type    | Default | Description                                         |
|----------------|---------|---------|-----------------------------------------------------|
| `systemPrompt` | string  | —       | LLM system prompt prepended to every context window |
| `llmProvider`  | string  | —       | Backend identifier: `"openai"`, `"anthropic"`, etc. |
| `model`        | string  | —       | Model name: `"gpt-4o"`, `"claude-3-5-sonnet"`, etc. |
| `temperature`  | float64 | 0.0     | Sampling temperature; must be in `[0.0, 2.0]`       |

---

## Versioning

Ruriko tracks every Gosuto change:

- The SHA-256 hash of the raw YAML is computed and stored.
- Each version is immutable after storage.
- Versions are numbered sequentially per agent (1, 2, 3, …).
- Up to **N** versions are retained (configurable; default 20).
- Rollback restores a previous version as a new version entry (audit trail preserved).

### Commands

```
/ruriko gosuto show <agent>                       — current config
/ruriko gosuto show <agent> --version <n>         — specific version
/ruriko gosuto diff <agent> --from <v1> --to <v2> — line diff between versions
/ruriko gosuto set <agent> --content <base64>     — store new version
/ruriko gosuto rollback <agent> --to <version>    — revert to version (creates new entry)
/ruriko gosuto push <agent>                       — push current version to running agent
```

---

## Security Considerations

1. **Secrets are never in Gosuto.** All credentials are managed separately.
2. **Persona does not override policy.** A system prompt cannot grant additional capabilities.
3. **Default deny.** If no capability rule matches, the tool call is rejected.
4. **Approval gating.** Sensitive operations must be explicitly approved, regardless of capability rules.
5. **Version history.** All changes are auditable with trace correlation.

---

## Minimal Valid Example

```yaml
apiVersion: gosuto/v1
metadata:
  name: my-agent
  description: Minimal agent with no tools

trust:
  allowedRooms:
    - "!roomid:example.com"
  allowedSenders:
    - "@alice:example.com"
```

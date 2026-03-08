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
  canonicalName: <well-known-name> # optional singleton role name
  description: <free text>

trust: { ... }
limits: { ... }                    # optional
capabilities: [ ... ]              # optional; default-deny if omitted
approvals: { ... }                 # optional
mcps: [ ... ]                      # optional
gateways: [ ... ]                  # optional; event source gateways
secrets: [ ... ]                   # optional
persona: { ... }                   # optional
workflow: { ... }                  # optional; deterministic protocol workflows
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
| `canonicalName` | string | ❌     | Optional singleton role name (`saito`, `kairo`, `kumo`) |
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
| `trustedPeers`   | []object | ❌       | Exact peer trust tuples for protocol workflows       |

**Validation rules:**
- `allowedRooms` entries must start with `!` or be `"*"`.
- `allowedSenders` entries must start with `@` or be `"*"`.
- Both lists must contain at least one entry.

`trustedPeers` object shape:

| Field       | Type     | Required | Description |
|-------------|----------|----------|-------------|
| `mxid`      | string   | ✅       | Exact trusted peer MXID |
| `roomId`    | string   | ✅       | Exact room ID where the peer is trusted |
| `alias`     | string   | ❌       | Human-readable peer alias |
| `protocols` | []string | ✅       | Protocol IDs this peer may send |

Additional trusted-peer validation:
- `mxid` must start with `@`.
- `roomId` must start with `!`.
- `protocols` must be non-empty.
- Duplicate `(mxid, roomId, protocol)` tuples are rejected.

Operator recipe for provision-time peer overrides (without manual YAML edits):
- `docs/architecture.md#6-operator-recipe-peer-override-topology-provision-time`
- `docs/ops/agent-mesh-topology.md#provision-time-peer-overrides-operator-commands`

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

### `gateways` *(optional)*

Event source gateways that feed inbound events into the agent's turn engine. Gateways are the inbound complement to MCPs: where MCPs provide outbound tool access, gateways provide inbound event ingress. Each gateway translates domain-specific events (email arrival, social media notification, timer tick) into a normalised event envelope and delivers it to the agent via its local webhook endpoint (`POST /events/{source}`).

There are two categories:

1. **Built-in gateways** — compiled into the Gitai binary, zero external dependencies. Specified by `type` only (no `command` field).
2. **External gateways** — supervised child processes, identical lifecycle model to MCPs. Specified by `command` + optional `args`/`env`. They POST events to `localhost:<acp_port>/events/{name}`.

| Field         | Type              | Required | Description                                                     |
|---------------|-------------------|----------|-----------------------------------------------------------------|
| `name`        | string            | ✅       | Unique name for this gateway within the agent. Used as the `{source}` path segment in `/events/{source}`. |
| `type`        | string            | ❌       | Built-in gateway type: `"cron"` or `"webhook"`. Mutually exclusive with `command`. |
| `command`     | string            | ❌       | Binary path for an external gateway process. Mutually exclusive with `type`. |
| `args`        | []string          | ❌       | Command-line arguments (external gateways only).                |
| `env`         | map[string]string | ❌       | Additional environment variables (external gateways only).      |
| `config`      | map[string]string | ❌       | Type-specific or gateway-specific configuration (see below).    |
| `autoRestart` | bool              | ❌       | Restart the gateway process if it exits unexpectedly (external only). |

**Exactly one** of `type` or `command` must be set.

#### Built-in type: `cron`

Supports two cron sources:

- `source: static` (default): emits `cron.tick` events based on `config.expression` and forwards them through ACP `POST /events/{name}`.
- `source: db`: executes due schedules from the agent's internal SQLite `cron_schedules` table. These rows are typically managed by built-in tools (`schedule.upsert`, `schedule.disable`, `schedule.list`) and dispatched deterministically as tool calls.

No external process is spawned — both modes run as goroutines inside the Gitai binary.

| Config key    | Type   | Required | Description                                                   |
|---------------|--------|----------|---------------------------------------------------------------|
| `source`      | string | ❌       | `static` (default) or `db`.                                   |
| `expression`  | string | ✅*      | Standard 5-field cron expression (minute hour dom month dow). Required for `source: static`. Optional bootstrap expression for `source: db`. |
| `payload`     | string | ❌       | For `source: static`: event `payload.message`. For `source: db`: optional bootstrap message (requires `target`). |
| `target`      | string | ❌       | Only used by `source: db` bootstrap; target alias for bootstrap `matrix.send_message` row. |
| `poll_interval` | string | ❌     | Only used by `source: db`; Go duration (default `15s`) controlling due-row polling. |

**Example:**

```yaml
gateways:
  - name: market-check
    type: cron
    config:
      source: db
      expression: "*/15 9-16 * * 1-5"
      target: kairo
      payload: "Check portfolio performance and market state"
```

#### Built-in type: `webhook`

Exposes an additional authenticated HTTP endpoint on the ACP listener for receiving external webhook deliveries (e.g. from GitHub, Stripe, or Ruriko acting as a reverse proxy for internet-facing webhooks).

| Config key       | Type   | Required | Description                                                      |
|------------------|--------|----------|------------------------------------------------------------------|
| `path`           | string | ❌       | Custom sub-path (default: `/events/{name}`).                     |
| `authType`       | string | ❌       | Authentication method: `"bearer"` (default, uses ACP token), `"hmac-sha256"`. |
| `hmacSecretRef`  | string | ❌       | Secret ref for HMAC verification (required when `authType` is `"hmac-sha256"`). |

**Example:**

```yaml
gateways:
  - name: github-push
    type: webhook
    config:
      authType: hmac-sha256
      hmacSecretRef: "my-agent.github-webhook-secret"
```

#### External gateways

External gateways are supervised processes that watch a domain-specific source and POST normalised event envelopes to the agent's local webhook endpoint. They follow the same lifecycle model as MCP server processes: started by the supervisor, restarted on crash (when `autoRestart` is true), and stopped on agent shutdown.

**Example:**

```yaml
gateways:
  - name: inbox-watch
    command: /usr/local/bin/ruriko-gw-imap
    args: ["--idle"]
    env: {}
    config:
      host: imap.gmail.com
      port: "993"
      tls: "true"
      folder: INBOX
    autoRestart: true
```

The set of `config` keys is gateway-specific and documented by each gateway binary. The Gitai runtime passes `config` entries as environment variables prefixed with `GATEWAY_` (e.g. `GATEWAY_HOST=imap.gmail.com`) and injects `GATEWAY_TARGET_URL=http://localhost:8765/events/{name}` so the gateway knows where to POST events.

#### Event envelope format

All gateways — built-in and external — produce the same normalised JSON envelope:

```json
{
  "source":   "inbox-watch",
  "type":     "email.received",
  "ts":       "2026-02-22T14:30:00Z",
  "payload": {
    "message": "New email from alice@example.com: Q4 earnings report",
    "data": { "from": "alice@example.com", "subject": "Q4 earnings report" }
  }
}
```

The `payload.message` field is what reaches the LLM as the equivalent of a "user message" for this event. The `payload.data` field carries structured metadata for downstream tool calls.

#### Rate limiting

Gateway events share the agent's existing rate limits (`limits.maxRequestsPerMinute`, etc.). An additional per-gateway rate limit can be enforced via the `eventRateLimit` field in `limits`:

| Field                      | Type | Default | Description                                    |
|----------------------------|------|---------|------------------------------------------------|
| `maxEventsPerMinute`       | int  | 0 (∞)   | Maximum inbound events per minute across all gateways. |

Events that exceed the rate limit are dropped with a warning log.

---

### `secrets` *(optional)*

References to Ruriko secrets the agent expects to be injected at runtime. Secret *values* are never stored in Gosuto. Ruriko distributes token leases via ACP `/secrets/token`, and the agent redeems each lease through Kuze.

| Field     | Type   | Required | Description                                       |
|-----------|--------|----------|---------------------------------------------------|
| `name`    | string | ✅       | Secret name in the Ruriko store                   |
| `envVar`  | string | ❌       | Environment variable to inject the value into      |
| `required`| bool   | ❌       | Refuse to start if this secret is missing          |

---

### `persona` *(optional)*

LLM persona configuration. **Non-authoritative** — all access control is enforced via capability rules, not the system prompt.

| Field              | Type    | Default | Description                                                      |
|--------------------|---------|---------|------------------------------------------------------------------|
| `systemPrompt`     | string  | —       | LLM system prompt prepended to every context window              |
| `llmProvider`      | string  | —       | Backend identifier: `"openai"`, `"anthropic"`, etc.              |
| `model`            | string  | —       | Model name: `"gpt-4o"`, `"claude-3-5-sonnet"`, etc.             |
| `temperature`      | float64 | 0.0     | Sampling temperature; must be in `[0.0, 2.0]`                   |
| `apiKeySecretRef`  | string  | —       | Secret ref for the LLM API key (resolved via Kuze/secrets store) |

---

### `workflow` *(optional)*

Deterministic, protocol-driven workflows executed by Gitai.

```yaml
workflow:
  schemas: {}
  protocols: []
```

#### `workflow.schemas`

- Local schema map keyed by name.
- Used by `inputSchemaRef`, `outputSchemaRef`, `forEachResultSchemaRef`, and `forEachIterationSchemaRef`.
- External references (URL/path/`#...`) are rejected.
- Duplicate schema keys are rejected.

#### `workflow.protocols[]`

| Field            | Type   | Required | Description |
|------------------|--------|----------|-------------|
| `id`             | string | ✅       | Protocol identifier |
| `trigger`        | object | ✅       | Protocol trigger matcher |
| `inputSchemaRef` | string | ❌       | Schema key for inbound payload validation |
| `retries`        | int    | ❌       | Protocol-level retry budget (must be `>= 0`) |
| `steps`          | []step | ❌       | Ordered deterministic step list |

`trigger` object:

| Field    | Type   | Required | Description |
|----------|--------|----------|-------------|
| `type`   | string | ✅       | Trigger type (`matrix.protocol_message`, `gateway.event`) |
| `prefix` | string | ❌       | Trigger matcher prefix (`matrix.protocol_message`: required protocol prefix, `gateway.event`: optional exact event-type matcher) |

Trigger validation rules:
- `trigger.type` must be exactly `matrix.protocol_message` or `gateway.event`.
- `trigger.prefix` must not contain whitespace.
- `matrix.protocol_message` requires a non-empty `trigger.prefix`.
- `gateway.event` allows empty `trigger.prefix` (matches any event type).

#### `workflow.protocols[].steps[]`

Common step fields:

| Field            | Type   | Required | Description |
|------------------|--------|----------|-------------|
| `type`           | string | ✅       | Step type |
| `retries`        | int    | ❌       | Step retry override (`>= 0`) |
| `maxOutputItems` | int    | ❌       | Generic array output guard (`>= 0`; `0` disables the guard) |
| `inputSchemaRef` | string | ❌       | Optional schema key |
| `outputSchemaRef`| string | ❌       | Optional schema key |

Supported `type` values:
- `parse_input`
- `tool`
- `branch` (reserved placeholder, currently not implemented)
- `summarize`
- `plan`
- `send_message`
- `persist`
- `for_each`
- `collect`

##### `tool`

| Field          | Type   | Required | Description |
|----------------|--------|----------|-------------|
| `tool`         | string | ✅       | Tool name routed via dispatcher |
| `argsTemplate` | object | ❌       | Interpolated args object |

##### `summarize`

| Field    | Type   | Required | Description |
|----------|--------|----------|-------------|
| `prompt` | string | ✅       | Interpolated summarize prompt |

##### `plan`

| Field            | Type   | Required | Description |
|------------------|--------|----------|-------------|
| `prompt`         | string | ✅       | Interpolated plan prompt |
| `outputSchemaRef`| string | ✅       | Schema key for structured plan output |

Runtime behavior:
- Plan output must be JSON (plain JSON or fenced JSON block).
- Parsed JSON is validated via `outputSchemaRef`.

##### `send_message`

| Field             | Type   | Required | Description |
|-------------------|--------|----------|-------------|
| `targetAlias`     | string | ✅       | Allowed messaging target alias |
| `payloadTemplate` | string | ✅       | Interpolated message payload |

##### `persist`

| Field          | Type   | Required | Description |
|----------------|--------|----------|-------------|
| `persistKey`   | string | ✅       | State key to write |
| `persistValue` | string | ✅       | Interpolated value |

##### `for_each`

| Field                       | Type   | Required | Description |
|-----------------------------|--------|----------|-------------|
| `itemsExpr`                 | string | ✅       | Interpolated array expression |
| `itemVar`                   | string | ❌       | Item variable name (default `item`) |
| `maxIterations`             | int    | ❌       | Upper bound for loop items (`>= 0`) |
| `steps`                     | []step | ✅       | Nested steps executed per item |
| `forEachResultSchemaRef`    | string | ❌       | Schema key to validate each iteration `result` |
| `forEachIterationSchemaRef` | string | ❌       | Schema key to validate each iteration contract object |

Validation rules:
- `itemsExpr` is required.
- `steps` is required and non-empty.
- `maxIterations` must be `>= 0`.
- `forEachResultSchemaRef` and `forEachIterationSchemaRef` are valid only on `for_each`.

Runtime iteration contract shape:

```yaml
index: <int>
item: <original-item>
outputs: <map of nested step outputs keyed by step_N>
result: <last non-empty nested step output>
```

##### `collect`

| Field            | Type   | Required | Description |
|------------------|--------|----------|-------------|
| `collectFrom`    | string | ✅       | Interpolated source array expression |
| `collectMode`    | string | ❌       | `result` (default), `entry`, `outputs`, `item` |
| `collectFlatten` | bool   | ❌       | Flatten array-shaped selected values |

Validation rules:
- `collectFrom` is required.
- `collectMode` (if set) must be one of `result`, `entry`, `outputs`, `item`.

#### Workflow example: strict iteration contracts

```yaml
workflow:
  schemas:
    kairoNewsRequest:
      type: object
      required: [run_id, topic]
      properties:
        run_id: { type: integer }
        topic: { type: string }
    searchPlan:
      type: object
      required: [items]
      properties:
        items:
          type: array
          items:
            type: object
            required: [query]
            properties:
              query: { type: string }
    searchResult:
      type: string
    searchIterationContract:
      type: object
      required: [index, item, outputs, result]
      properties:
        index: { type: integer }
        item:
          type: object
          required: [query]
          properties:
            query: { type: string }
        outputs: { type: object }
        result: { type: string }
    kumoNewsResponse:
      type: object
      required: [run_id, summary, headlines, material]
      properties:
        run_id: { type: integer }
        summary: { type: string }
        headlines: { type: array }
        material: { type: boolean }
  protocols:
    - id: kairo.news.request.v1
      trigger:
        type: matrix.protocol_message
        prefix: KAIRO_NEWS_REQUEST
      inputSchemaRef: kairoNewsRequest
      steps:
        - type: plan
          prompt: "Build a concise search plan for {{input.topic}}"
          outputSchemaRef: searchPlan
        - type: for_each
          itemsExpr: "{{steps.step_0.items}}"
          itemVar: plan_item
          maxIterations: 5
          forEachResultSchemaRef: searchResult
          forEachIterationSchemaRef: searchIterationContract
          steps:
            - type: tool
              tool: brave-search__web_search
              argsTemplate:
                query: "{{state.plan_item.query}}"
              outputSchemaRef: searchResult
        - type: collect
          collectFrom: "{{steps.step_1}}"
          collectMode: result
        - type: summarize
          prompt: "Summarize collected results for run {{input.run_id}}"
          outputSchemaRef: kumoNewsResponse
```

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
6. **Gateway events are untrusted input.** Event payloads are treated identically to user messages — they pass through the same policy engine. A crafted event cannot bypass capability rules.
7. **External gateways follow the MCP threat model.** They are vetted binaries baked into the container image, supervised by the same process manager, and receive credentials only via Kuze-managed environment variables.
8. **Webhook authentication.** Built-in webhook gateways require ACP bearer token or HMAC signature verification. Unauthenticated webhook deliveries are rejected.
9. **Internet webhooks are proxied through Ruriko.** Agents never receive raw internet traffic. Ruriko validates and forwards webhook payloads via ACP.

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

---

## Event-Driven Agent Example

```yaml
apiVersion: gosuto/v1
metadata:
  name: portfolio-watcher
  template: finance-agent
  description: Watches market on a schedule and reacts to email alerts

trust:
  allowedRooms:
    - "!room:example.com"
  allowedSenders:
    - "@bogdan:example.com"

limits:
  maxRequestsPerMinute: 10
  maxEventsPerMinute: 30

gateways:
  - name: market-hours-tick
    type: cron
    config:
      expression: "*/15 9-16 * * 1-5"
      payload: "Check portfolio performance and market state"

  - name: inbox-watch
    command: /usr/local/bin/ruriko-gw-imap
    args: ["--idle"]
    config:
      host: imap.gmail.com
      port: "993"
      tls: "true"
      folder: INBOX
    autoRestart: true

mcps:
  - name: finnhub
    command: /usr/local/bin/mcp-finnhub
    autoRestart: true

capabilities:
  - name: allow-finnhub
    mcp: finnhub
    tool: "*"
    allow: true
  - name: default-deny
    mcp: "*"
    tool: "*"
    allow: false

secrets:
  - name: portfolio-watcher.openai-api-key
    envVar: OPENAI_API_KEY
    required: true
  - name: portfolio-watcher.finnhub-api-key
    envVar: FINNHUB_API_KEY
    required: true
  - name: portfolio-watcher.imap-credentials
    envVar: IMAP_CREDENTIALS
    required: true

persona:
  systemPrompt: |
    You are a portfolio analyst. When triggered by a scheduled check, analyse
    current market conditions using your tools. When triggered by an email,
    assess whether the email content is relevant to the portfolio and act
    accordingly. Always provide concise, actionable reports.
  llmProvider: openai
  model: gpt-4o
  temperature: 0.2
  apiKeySecretRef: portfolio-watcher.openai-api-key
```

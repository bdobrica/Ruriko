# Workflow Step Specification (Draft)

> Normative draft for Gosuto-driven workflow execution in Gitai.
>
> This document defines the step model for `workflow.protocols[].steps[]` and is
> the anchor for implementation and future extensions.

**Status**: Draft (implementation target)  
**Last Updated**: 2026-02-27

---

## 1) Purpose

This spec formalizes a declarative workflow model so agent behavior is encoded in Gosuto instead of hard-coded runtime branches.

Core design goals:

- Deterministic control path
- Policy-first tool execution
- Schema-enforced step input/output
- Auditable, versioned workflow behavior
- No embedded DSL parser in v1

---

## 2) Scope

In scope (v1):

- `workflow.schemas`
- `workflow.protocols`
- `workflow.protocols[].steps[]`
- Step input/output contracts
- Allowed step types and execution semantics
- Error and retry behavior

Out of scope (v1):

- Full scripting language (`LET/CASE/WHILE` parser)
- Arbitrary `goto`
- Unbounded loops

---

## 3) Data model

## 3.1 Workflow container

```yaml
workflow:
  schemas: {}
  protocols: []
```

- `schemas`: map of JSON Schema objects keyed by schema name.
- `protocols`: list of protocol workflows keyed by protocol `id`.

## 3.2 Protocol definition

```yaml
workflow:
  protocols:
    - id: "kairo.news.request.v1"
      trigger:
        type: "matrix.protocol_message"
        prefix: "KAIRO_NEWS_REQUEST"
      inputSchemaRef: "kairoNewsRequest"
      retries: 2
      steps: []
```

Fields:

- `id` (required): stable protocol identifier.
- `trigger` (required): trigger matcher.
- `inputSchemaRef` (required): key in `workflow.schemas`.
- `retries` (optional): protocol-level parse retry default.
- `steps` (required): ordered execution steps.

## 3.3 Step definition

Every step is an object with:

- `id` (optional, recommended): stable step identifier
- `type` (required): step kind
- `input` (optional): input binding expression/config
- `output` (optional): output binding target
- `retries` (optional): per-step override
- Type-specific fields (required per type)

---

## 4) Workflow state and I/O contract

Workflow execution operates over a deterministic state object:

```yaml
state:
  protocol:
    id: string
    trigger: object
  input: object          # validated protocol input
  step:
    current: object|null # current step result
    byId: map            # step-id -> result
  tools:
    last: object|null
  output: object|null    # final workflow output payload
  meta:
    traceId: string
    senderMxid: string
    roomId: string
    startedAt: string
```

Rules:

- A step reads from `state` and writes a structured result back to `state.step.current` and optionally `state.step.byId[step.id]`.
- A step may declare `output.bind` to map result fields into `state.output`.
- Any declared `inputSchemaRef` / `outputSchemaRef` must validate before side-effect continuation.

---

## 5) Schema model

## 5.1 Location

Schemas are defined inline under `workflow.schemas`.

```yaml
workflow:
  schemas:
    kairoNewsRequest:
      type: object
      required: [run_id, tickers]
      properties:
        run_id: { type: integer, minimum: 1 }
        tickers:
          type: array
          minItems: 1
          items: { type: string }
```

## 5.2 Resolution

- `inputSchemaRef` and `outputSchemaRef` resolve only against `workflow.schemas`.
- External refs (URI/path/remote) are not supported in v1.

## 5.3 Validation behavior

- Missing/invalid schema references are config-apply errors.
- Runtime schema validation failure triggers retry policy.
- On retry exhaustion: fail-safe refuse and stop workflow.

---

## 6) Step types (v1)

## 6.1 `parse_input`

Purpose: parse/normalize raw protocol payload to schema.

Required fields:

- `outputSchemaRef`
- `prompt` (if LLM-assisted parsing is used)

Semantics:

- Produces structured object in `state.step.current`.
- Must pass `outputSchemaRef` validation.

## 6.2 `tool`

Purpose: call a tool (MCP or built-in) through unified dispatcher.

Required fields:

- `toolRef` (e.g. `mcp.brave-search.search`, `builtin.matrix.send_message`)
- `argsTemplate`

Semantics:

- Dispatcher enforces policy + approvals before execution.
- Direct MCP client access is forbidden.

## 6.3 `branch`

Purpose: evaluate deterministic predicates and choose branch path.

Required fields:

- `when`: ordered predicate list
- `default`: fallback branch

Semantics:

- First-match-wins.
- Predicate language is minimal and deterministic (field compare, existence, bool).

## 6.4 `summarize`

Purpose: generate structured summary output from current state.

Required fields:

- `prompt`
- `outputSchemaRef`

Semantics:

- LLM output must validate against `outputSchemaRef`.
- Retry then refuse on invalid structured output.

## 6.5 `send_message`

Purpose: send Matrix message via built-in tool.

Required fields:

- `targetAlias`
- `payloadTemplate`

Semantics:

- Executed through dispatcher using `builtin.matrix.send_message`.
- Subject to messaging policy, limits, and audit.

## 6.6 `persist`

Purpose: write bounded structured state to storage.

Required fields:

- `storeRef`
- `payloadTemplate`

Semantics:

- Uses predefined store operation surface (no arbitrary SQL in step config).
- Must be deterministic and auditable.

---

## 7) Retry semantics

Retry precedence:

1. step-level `retries`
2. protocol-level `retries`
3. engine default (0)

Retry applies only to schema-producing/validating steps (`parse_input`, `summarize`, optional `tool` output validation).

On exhaustion:

- Stop workflow.
- Mark status as refused/failed safely.
- Emit audit event with reason and step id.
- Do not execute remaining side-effect steps.

---

## 8) Trust and trigger gating

Before protocol step execution:

1. Validate room/sender against `trust.allowedRooms` and `trust.allowedSenders`.
2. Match trigger shape (e.g. protocol prefix).
3. Validate peer protocol trust against `trust.trustedPeers`:
   - sender MXID exact match
   - room ID exact match
   - protocol id allowed for peer

Any mismatch => refuse + audit warning.

---

## 9) Approval and policy boundary

For any step that invokes a tool:

- policy evaluation is mandatory
- `requireApproval` routes via Ruriko approval flow
- only approved operations execute
- deny/timeout => refuse

This applies equally to workflow-triggered and LLM-triggered tool calls.

---

## 10) Control flow in v1

Supported control flow:

- Linear ordered steps
- Deterministic branch (`branch`)

Not supported in v1:

- Arbitrary backward jumps
- Unbounded loops

### Reserved extension: bounded `jump`

A future `jump` step MAY be introduced with strict bounds.

Potential shape:

```yaml
- type: jump
  targetStepId: "fetch-news"
  when:
    field: "state.step.current.retryNeeded"
    op: "=="
    value: true
  maxJumps: 2
```

Constraints for future adoption:

- target must be a valid existing `step.id`
- only backward jump allowed when explicitly enabled
- per-step and per-workflow jump counters required
- hard cap required (`maxJumps`), default deny without cap

---

## 11) Determinism and observability requirements

Must be logged/audited with trace correlation:

- protocol trigger match result
- trusted peer check result
- schema validation pass/fail
- policy decision per tool call
- approval request/decision reference
- step start/end + duration
- workflow terminal status

No secret values may be logged.

---

## 12) Minimal example

```yaml
workflow:
  schemas:
    kairoNewsRequest:
      type: object
      required: [run_id, tickers]
      properties:
        run_id: { type: integer, minimum: 1 }
        tickers:
          type: array
          minItems: 1
          items: { type: string }
    kumoNewsResponse:
      type: object
      required: [run_id, summary, headlines, material]
      properties:
        run_id: { type: integer, minimum: 1 }
        summary: { type: string }
        headlines:
          type: array
          items: { type: string }
        material: { type: boolean }
  protocols:
    - id: "kairo.news.request.v1"
      trigger:
        type: "matrix.protocol_message"
        prefix: "KAIRO_NEWS_REQUEST"
      inputSchemaRef: "kairoNewsRequest"
      retries: 2
      steps:
        - id: "news-fetch"
          type: tool
          toolRef: "mcp.brave-search.search"
          argsTemplate:
            query: "{{state.input.tickers}} stock news latest"
        - id: "news-summarize"
          type: summarize
          prompt: "Summarize top headlines and mark materiality"
          outputSchemaRef: "kumoNewsResponse"
          retries: 2
        - id: "reply-kairo"
          type: send_message
          targetAlias: "kairo"
          payloadTemplate: "KUMO_NEWS_RESPONSE {{state.step.current.json}}"
```

---

## 13) References

- [REFACTOR.md](../REFACTOR.md)
- [gosuto-spec.md](./gosuto-spec.md)
- [invariants.md](./invariants.md)
- [architecture.md](./architecture.md)

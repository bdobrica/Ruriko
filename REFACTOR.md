# R6 Refactor Plan — Gosuto-Driven Workflow Engine

**Status**: Proposed (approved direction)  
**Date**: 2026-02-26  
**Owner**: Ruriko/Gitai core

---

## 1) Why this refactor

Phase R6.1–R6.3 proved the canonical story works, but the implementation is currently coupled to hard-coded canonical agent paths (Saito/Kairo/Kumo) inside Gitai runtime logic.

That coupling is useful for validating ideas quickly, but it does not scale to many specialized agents and weakens the intended contract:

- workflow behavior should be versioned/auditable in Gosuto,
- policy enforcement should remain centralized in deterministic code,
- agent specialization should come from configuration, not new runtime branches.

This refactor replaces canonical hard-coded pipelines with a **generic Gosuto-driven workflow engine** while preserving policy-first security and deterministic approvals.

Step model details are specified in [docs/workflow-step-spec.md](docs/workflow-step-spec.md).

---

## 2) Confirmed decisions (from design review)

1. **No compatibility mode**: this is an in-progress system; we can break current R6 canonical code paths in one cut.
2. **Trusted protocol senders**: add `trustedPeers` under `trust`.
3. **Enforcement model**: enforce on **MXID + room**, alias optional (UX only).
4. **Policy boundary**: deterministic workflow engine must **never** call MCP directly; all tool calls must go through existing policy + approval gates.
5. **Approval transport**: use **via_ruriko** first as the only path in this refactor.
6. **Workflow language**: start simple with declarative primitives; no full DSL parser yet.

---

## 3) Goals

- Remove Saito/Kairo/Kumo special branches from Gitai turn handling.
- Express machine-triggered workflow behavior in Gosuto (schema-validated, versioned, diffable).
- Support structured NL parsing and structured output with retry-then-refuse semantics.
- Enforce peer-protocol trust separately from general message allowlists.
- Preserve all existing security invariants (policy-first, default deny, explicit approvals, full audit).

---

## 4) Non-goals (this refactor)

- No full scripting DSL (`LET/CASE/WHILE`) parser yet.
- No direct-to-user approval transport.
- No backwards-compat shim for old canonical deterministic code.
- No broad redesign of Ruriko command/NLP stack.

---

## 5) Architecture target

### 5.1 Turn processing split

For each incoming Matrix/gateway event, Gitai runs:

1. Trust checks (`allowedRooms`, `allowedSenders`)  
2. **Protocol trust checks** (if message/event matches workflow protocol trigger) against `trust.trustedPeers`  
3. Workflow trigger match against Gosuto workflow spec  
4. Workflow step execution (parse -> branch -> tool -> summarize)  
5. Any tool invocation is dispatched through existing policy engine + approval flow  
6. Audit records written with trace correlation

### 5.2 Tool execution invariant

All tool execution must route through one deterministic path:

- policy evaluate
- if `requireApproval` => request approval via Ruriko
- execute tool (MCP/builtin) only after allow/approval
- audit result

No workflow step may obtain MCP clients and call `CallTool` directly.

---

## 6) Gosuto schema changes (v1 additive)

## 6.1 `trust.trustedPeers`

Add a protocol-trust list under `trust`:

```yaml
trust:
  allowedRooms:
    - "!room:server"
  allowedSenders:
    - "*"
  trustedPeers:
    - mxid: "@kairo:server"
      roomId: "!kairo-room:server"
      alias: "kairo" # optional, UX only
      protocols:
        - "kairo.news.request.v1"
```

### Semantics

- `allowedSenders`/`allowedRooms` => who can chat generally.
- `trustedPeers` => who can send **machine-protocol messages** that trigger workflow protocol steps.
- Matching requires:
  - sender MXID exact match,
  - room ID exact match,
  - protocol ID allowed for that peer.
- If a message matches protocol shape but sender/room/protocol is not trusted: refuse + audit warn.

### Validation rules

- `mxid` must start with `@`.
- `roomId` must start with `!`.
- `protocols` cannot be empty for trusted peer entries.
- Duplicate `(mxid, roomId, protocol)` tuples forbidden.

---

## 6.2 `workflow` section (declarative primitives)

Add a new top-level section (name can be `workflow` or `workflows`; use singular for now):

```yaml
workflow:
  schemas:
    kairoNewsRequest:
      type: object
      required: [run_id, tickers]
      properties:
        run_id:
          type: integer
          minimum: 1
        tickers:
          type: array
          minItems: 1
          items:
            type: string
    kumoNewsResponse:
      type: object
      required: [run_id, summary, headlines, material]
      properties:
        run_id:
          type: integer
          minimum: 1
        summary:
          type: string
        headlines:
          type: array
          items:
            type: string
        material:
          type: boolean
  protocols:
    - id: "kairo.news.request.v1"
      trigger:
        type: "matrix.protocol_message"
        prefix: "KAIRO_NEWS_REQUEST"
      inputSchemaRef: "kairoNewsRequest"
      retries: 2
      steps:
        - type: "tool"
          tool: "mcp.brave-search.search"
          argsTemplate:
            query: "{{input.tickers}} stock news latest"
        - type: "summarize"
          prompt: "Summarize top material headlines"
          outputSchemaRef: "kumoNewsResponse"
          retries: 2
        - type: "send_message"
          targetAlias: "kairo"
          payloadTemplate: "KUMO_NEWS_RESPONSE {{output.json}}"
```

### Schema location and resolution (explicit)

- Workflow schemas are embedded in Gosuto under `workflow.schemas`.
- `inputSchemaRef` and `outputSchemaRef` must resolve to keys inside `workflow.schemas`.
- No external schema URI/file lookup is allowed in v1 workflow execution.
- Missing schema refs are validation errors and block config apply.

### Validation contract (workflow schemas)

Validator behavior should be deterministic and fail-fast on config apply.

- `workflow.schemas` missing while a protocol/step declares `*SchemaRef`:
  - error: `workflow schema ref requires workflow.schemas to be defined`
- `inputSchemaRef` not found in `workflow.schemas`:
  - error: `workflow protocol <id>: input schema ref <ref> not found`
- `outputSchemaRef` not found in `workflow.schemas`:
  - error: `workflow protocol <id>, step <index>: output schema ref <ref> not found`
- Empty schema ref value (`""` after trim):
  - error: `workflow protocol <id>: schema ref cannot be empty`
- Duplicate schema keys in `workflow.schemas`:
  - error: `workflow schemas contains duplicate key <name>`
- Invalid schema object (not a JSON object / invalid JSON Schema structure):
  - error: `workflow schema <name>: invalid JSON Schema`
- Disallowed external schema reference syntax (e.g. URI/path/pointer outside `workflow.schemas`):
  - error: `external schema references are not supported; use workflow.schemas`

Recommended validator checks for v1 schema objects:

- schema root must be an object.
- if `type` is present it must be a valid JSON Schema type keyword value.
- if `required` is present it must be an array of unique strings.
- if `properties` is present it must be an object.

### Primitive step types for v1

- `parse_input` (LLM parse to schema, retries)
- `tool` (named tool call, policy-gated)
- `branch` (simple predicates on structured state)
- `summarize` (LLM output to schema, retries)
- `send_message` (builtin matrix send)
- `persist` (store state through bounded store API)

### Retry semantics

- Schema parse/output retries are bounded (`retries` integer).
- On exhaustion: fail-safe refuse, do not continue to side-effect steps.

---

## 7) Approval model (via Ruriko)

Workflow/tool approvals use one deterministic transport:

1. Gitai computes normalized approval request (tool, args hash, trace_id, context)
2. Gitai sends approval request to Ruriko control channel
3. Ruriko persists request + emits approver prompt
4. Approver decision returns to Ruriko (`approve/deny` deterministic parser)
5. Ruriko returns signed/validated decision to Gitai
6. Gitai executes tool only on approve; deny/timeout => refuse

Audit responsibilities:

- Ruriko: canonical approval ledger (request, actor, decision, timestamps)
- Gitai: local execution audit + received decision reference

---

## 8) Single-cut implementation plan

## 8.1 Remove canonical branches

Delete canonical-specific runtime hooks and files:

- hard-coded Saito event branch in app turn loop
- hard-coded Kairo/Kumo deterministic message branches
- canonical-specific protocol constants in runtime core

Replace with generic workflow-trigger dispatch.

## 8.2 Build workflow engine package

Add `internal/gitai/workflow/` (or equivalent) with:

- trigger matcher
- schema validation helpers
- step executor
- execution context/state bag
- deterministic error typing

## 8.3 Unified tool dispatcher

Expose a single internal API used by both LLM turns and workflow engine:

- `DispatchToolCall(ctx, caller, toolRef, args)`

This API must own policy evaluation + approvals + audit + execution for builtin/MCP tools.

## 8.4 Gosuto spec + validation updates

Update:

- `common/spec/gosuto/types.go`
- `common/spec/gosuto/validate.go`
- JSON schema files under `schemas/gosuto/`
- docs (`docs/gosuto-spec.md`, `docs/architecture.md`, `docs/invariants.md` if needed)

## 8.5 Template updates

Refactor `templates/saito-agent/gosuto.yaml`, `templates/kairo-agent/gosuto.yaml`, `templates/kumo-agent/gosuto.yaml` to encode canonical workflow using generic workflow config + `trust.trustedPeers`.

---

## 9) Security checklist mapping (must pass)

- Deterministic control boundary preserved (no control decision by LLM).
- Policy > Instructions > Persona preserved (workflow in config, policy in code).
- No direct MCP invocation paths outside dispatcher.
- Explicit approval for `requireApproval` tool calls (via Ruriko).
- Fail-safe deny on schema/trigger trust mismatch.
- Protocol messages gated by `trustedPeers` (MXID + room + protocol).
- Full audit trail: trigger, validation result, policy decision, approval, execution outcome.
- No secret material in Matrix payloads/logs.

---

## 10) Testing strategy

## 10.1 Unit

- `trustedPeers` matching and rejection behavior.
- Workflow parser/validator for malformed schemas/steps.
- Retry-then-refuse behavior for parse/summarize steps.
- Dispatcher ensures policy + approval called for every tool step.

## 10.2 Integration (deterministic)

- Saito->Kairo->Kumo canonical loop using workflow config only.
- Protocol message from untrusted peer rejected even with `allowedSenders: "*"`.
- Approval-required tool step blocked until Ruriko approval arrives.

## 10.3 Live/compose

- End-to-end canonical cycle for at least 3 consecutive runs.
- Validate no secret leakage in Matrix/audit logs.
- Validate Ruriko approval ledger completeness for approved/denied calls.

---

## 11) Acceptance criteria

Refactor is complete when all are true:

1. No Saito/Kairo/Kumo hard-coded turn branches remain in Gitai runtime.
2. Canonical workflow behavior is encoded in Gosuto templates + workflow config.
3. `trust.trustedPeers` exists and is enforced for protocol-triggered workflows.
4. Workflow-triggered tool calls go through policy + via-Ruriko approvals.
5. Canonical deterministic + live integration tests pass.
6. Docs/specs updated and consistent.

---

## 12) Risks and mitigations

### Risk: schema complexity grows quickly
Mitigation: keep primitive set minimal; postpone DSL parser.

### Risk: workflow misconfiguration becomes new failure mode
Mitigation: strict Gosuto validation + clear startup errors + dry-run validation command.

### Risk: approval latency slows workflow
Mitigation: reserve approvals for high-risk tools only; maintain deterministic timeout/deny behavior.

---

## 13) Immediate execution order

1. Add Gosuto schema/types (`trustedPeers`, `workflow`) + validators.
2. Implement generic workflow engine skeleton + trigger matching.
3. Implement unified dispatcher API and route workflow tool steps through it.
4. Remove canonical hard-coded runtime branches.
5. Port canonical templates to workflow config.
6. Update tests (unit + deterministic integration + live checklist).
7. Update architecture/spec docs.

---

## 14) Deferred follow-ups (post-refactor)

- Optional compact DSL as syntax sugar over declarative workflow model.
- Optional direct-to-user approvals (only after audit parity model is specified).
- Workflow visualizer/introspection tooling for operator UX.

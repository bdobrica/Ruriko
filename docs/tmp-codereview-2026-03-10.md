# Code Review — Ruriko & Gitai

**Date**: 2026-03-10  
**Scope**: `common/`, `internal/ruriko/`, `internal/gitai/`  
**Methodology**: Full file-by-file read of all Go source and test files, cross-referenced against invariants and architecture docs. All critical/high findings verified against actual source.

---

## Executive Summary

The codebase is **well-engineered** with clear package boundaries, strong security fundamentals, and good test coverage. The architecture faithfully implements the invariants: deterministic control plane, policy-before-execution, secret isolation, and audit trails.

**Overall Grade: B+** — production-ready with a handful of issues that should be addressed.

| Category | Rating | Notes |
|----------|--------|-------|
| Architecture | A- | Clean separation, correct layering, minimal coupling |
| Security | B+ | Strong at boundaries; bearer token timing + missing CSRF |
| Error handling | B+ | Generally excellent; a few silent swallows |
| Concurrency | A- | Proper mutex usage; RWMutex for LLM provider hot-swap |
| Test coverage | B | Good unit tests; some edge-case gaps |
| Code quality | A- | Idiomatic Go; well-documented; consistent style |

---

## CRITICAL — Fix Before Production

### C1. OpenAI client ignores HTTP status codes

**File**: [common/llm/openai/client.go](common/llm/openai/client.go#L147-L170)  
**Impact**: Silent failures on 401 (auth), 429 (rate limit), 500 (server error)

The `CreateChatCompletion` method reads the response body and decodes JSON regardless of the HTTP status code. A 429 from OpenAI still produces a `ChatCompletionResult` with empty choices, and the caller has no way to distinguish "no content generated" from "rate limited":

```go
httpResp, err := c.http.Do(httpReq)
// ...
respBody, err := io.ReadAll(httpResp.Body)
// ❌ No status code check here
var parsed ChatCompletionResponse
json.Unmarshal(respBody, &parsed)
return &ChatCompletionResult{StatusCode: httpResp.StatusCode, ...}, nil
```

The `StatusCode` field is set but never checked by callers. The `Response.Error` field may be populated by OpenAI on errors but is also never inspected.

**Fix**: Check status code before parsing; check the error envelope even on 200:
```go
if httpResp.StatusCode >= 400 {
    return nil, fmt.Errorf("openai: HTTP %d: %s", httpResp.StatusCode, string(respBody))
}
if parsed.Error != nil {
    return nil, fmt.Errorf("openai: API error: %s", parsed.Error.Message)
}
```

**Severity**: Critical — a 429 or 401 will silently produce empty LLM responses, leading to confusing agent behavior.

---

### C2. Bearer token comparison is not constant-time

**File**: [internal/gitai/control/server.go](internal/gitai/control/server.go#L291) (and two more locations at lines ~805 and ~892)  
**Impact**: Timing side-channel on ACP bearer token

The auth middleware compares bearer tokens with `!=`:
```go
if auth[len("Bearer "):] != s.handlers.Token {
    writeError(w, http.StatusUnauthorized, "invalid bearer token")
}
```

This appears in three places (general auth middleware, non-webhook event path, webhook bearer path). While the ACP is on a private Docker network, a co-located attacker could exploit timing differences to brute-force the token byte-by-byte.

Note: The HMAC-SHA256 webhook path correctly delegates to `webhookauth.VerifyHMACSHA256` which uses `hmac.Equal` (constant-time). Only the bearer code paths are vulnerable.

**Fix**: Use `subtle.ConstantTimeCompare()` for all three bearer checks.

---

### C3. Idempotency cache never evicts expired entries

**File**: [internal/gitai/control/server.go](internal/gitai/control/server.go#L70-L130)  
**Impact**: Slow memory leak over deployment lifetime

The `idempotencyCache.get()` method checks `expiresAt` and rejects stale entries, but never deletes them from the underlying map. The `set()` method only adds entries. Over a long-running deployment with frequent ACP calls, the map grows unbounded.

**Fix**: Add a periodic cleanup goroutine (e.g., every 5 minutes, sweep entries where `time.Now().After(e.expiresAt)`), or prune expired entries inside `set()`.

---

## HIGH — Fix Soon

### H1. Rate limiter bucket map grows unbounded

**File**: [common/ratelimit/fixed_window.go](common/ratelimit/fixed_window.go#L60-L84)  
**Impact**: Memory leak when many unique keys are used

The `KeyedFixedWindow` creates a new `bucket` for every unique key and never removes them, even after the window expires. For use cases with high key cardinality (per-user rate limiting, per-source event limiting), the bucket map grows without bound.

**Fix**: Add a cleanup sweep after window resets, or delete bucket entries when their `resetAt` has passed and count is 0.

---

### H2. Retry has no jitter — retry storms possible

**File**: [common/retry/retry.go](common/retry/retry.go#L43-L95)  
**Impact**: Synchronized retries under load

The backoff is purely deterministic (500ms → 1s → 2s → ...). If multiple agents hit the same transient failure simultaneously, they all retry at the same times, potentially amplifying the overload.

**Fix**: Add randomized jitter (e.g., ±25% of delay):
```go
jitter := time.Duration(rand.Int63n(int64(delay / 4)))
actualDelay := delay + jitter
```

---

### H3. Kuze form has no CSRF protection

**File**: [internal/ruriko/kuze/server.go](internal/ruriko/kuze/server.go#L425-L500)  
**Impact**: Cross-site request forgery on secret entry

The one-time link form at `POST /s/<token>` accepts form submissions without a CSRF token. An attacker who knows the Kuze URL (e.g., from observing Matrix traffic on an unencrypted homeserver) could craft a malicious page that auto-submits an arbitrary value.

**Mitigating factors**: Tokens are one-time, cryptographically random, and short-lived (10 min default). The attacker needs the exact token URL. Risk is low but the fix is simple.

**Fix**: Generate a random CSRF token on `GET`, store it in a short-lived cookie, embed it in a hidden form field, and validate it on `POST`. Or use `SameSite=Strict` cookie + `Origin` header check.

---

### H4. Redact pattern list is incomplete

**File**: [common/redact/redact.go](common/redact/redact.go#L54-L63)  
**Impact**: Secrets in fields named `jwt`, `signing_key`, `bearer`, `private_key`, `connection_string` are not redacted

Current sensitive-key patterns: `password`, `passwd`, `token`, `secret`, `key`, `credential`, `auth`, `apikey`.

Missing common patterns that should be added:
- `jwt`, `bearer`, `signing`, `private`, `certificate`
- `connection` (catches `connection_string`, `connectionUrl`)
- `access` (catches `access_key`, `access_token`)

Also, `Map()` only processes the top level — nested maps/slices pass through unredacted. A field like `{"database": {"password": "secret123"}}` would not be caught.

**Fix**: Expand the pattern list; consider adding recursive redaction for nested structures.

---

### H5. Constraint validation in policy engine is limited

**File**: [internal/gitai/policy/engine.go](internal/gitai/policy/engine.go#L176-L205)  
**Impact**: Only `url_prefix` is a typed constraint; all others use fmt.Sprintf string comparison

The `checkConstraints` function has a `switch` on constraint key with only one named case (`url_prefix`). The `default` branch compares using `fmt.Sprintf("%v", actual)`, which is fragile for non-string types (booleans, numbers, nested objects).

Gosuto validation (`common/spec/gosuto/validate.go`) accepts arbitrary constraint keys without documenting which ones are actually enforced at runtime.

**Fix**: Either enumerate supported constraint types in both Gosuto validation and the policy engine, or document clearly that only `url_prefix` is typed and all others are string-equality checks.

---

## MODERATE — Address in Current Cycle

### M1. Secrets distributor partial failure handling

**File**: [internal/ruriko/secrets/distributor.go](internal/ruriko/secrets/distributor.go#L90-L170)  
**Impact**: If a Kuze token issuance fails for one binding, only the successful ones are pushed and marked

`distributeViaTokens` issues tokens for each binding in a loop. Failed bindings are logged, and their errors collected. Successfully-issued leases are sent to the agent via ACP, and only those get `MarkPushed`. Function returns `(pushed_count, aggregate_error)`.

This is acceptable behavior if `PushToAgent` is called periodically — failed bindings remain stale and will be retried next cycle. However, the caller should be aware of partial success semantics. Currently, a non-nil error is returned alongside a non-zero pushed count, which is unusual.

**Recommendation**: Document the partial-success contract explicitly. Consider logging at ERROR level (not WARN) when token issuance fails, since the binding becomes stale until the next push cycle.

---

### M2. Token estimation in memory context assembly is crude

**File**: [common/memory/context.go](common/memory/context.go)  
**Impact**: May over- or under-allocate context window budget

The `EstimateTokens` function assumes 1 token ≈ 4 characters, which is a rough approximation. For Unicode-heavy content, CJK text, or code with many special characters, this can be significantly off.

**Recommendation**: Document the approximation explicitly. Consider using a more conservative ratio (3 chars/token) to avoid context truncation. No need for a full tokenizer dependency at this stage.

---

### M3. MCP JSON-RPC scanner buffer is hardcoded at 1 MB

**File**: [internal/gitai/mcp/client.go](internal/gitai/mcp/client.go#L165-L166)  
**Impact**: A malicious or buggy MCP server can force 1 MB memory allocation per response line

The scanner buffer is set to `1<<20` (1 MB). This is reasonable for most MCP tools, but the limit is not configurable and there's no warning logged when a response is close to the limit.

**Recommendation**: Make the limit configurable via Gosuto or environment variable. Log a warning when a response exceeds 512 KB. Low priority.

---

### M4. Gosuto validation: missing cross-reference checks for workflow steps

**File**: [common/spec/gosuto/validate.go](common/spec/gosuto/validate.go)  
**Impact**: Invalid workflow step references are caught at runtime, not at config load time

Several workflow step types reference other entities that could be validated at config load time:
- `step.Type = "send_message"`: `TargetAlias` not validated against `messaging.allowed_targets[].alias`
- `step.Type = "tool"`: `Tool` not validated against available MCP tools
- `step.Type = "branch"`: `BranchExpr` not checked for emptiness

These will fail at runtime with less helpful error messages.

**Recommendation**: Add cross-reference validation in `Validate()` for step fields that reference other config sections.

---

### M5. Docker network init failure is swallowed as warning

**File**: internal/ruriko/runtime/docker/adapter.go + internal/ruriko/app/app.go  
**Impact**: If `EnsureNetwork()` fails during startup, agent spawning later produces cryptic Docker API errors

The app logs a warning and continues. When an agent is subsequently spawned, it targets a network that doesn't exist, resulting in a confusing Docker API error.

**Recommendation**: Either fail startup if the network can't be ensured, or lazily create the network on first `Spawn()` with a clear error message.

---

### M6. Envelope message field not validated

**File**: [common/spec/envelope/event.go](common/spec/envelope/event.go)  
**Impact**: Gateway events with empty `payload.message` pass validation but may produce empty LLM prompts

The doc comment says `Message` is "required for LLM-driven agents" but `Validate()` does not enforce it. An empty message silently becomes an empty context for the agent LLM.

**Recommendation**: Add `if e.Payload.Message == ""` check in `Validate()`, or at least in `Warnings()`.

---

## LOW — Nice to Have

### L1. `app.go` in Gitai is large (~1200+ lines)

The Gitai `app.go` handles initialization, message handling, turn processing, tool dispatch, LLM calls, secret resolution, workflow integration, and event handling. Consider splitting into focused files:
- `turn.go` — `runTurn`, LLM dispatch loop
- `tools.go` — `DispatchToolCall`, `executeBuiltinTool`, `resolveSecretArgs`
- `events.go` — `handleEvent`, `runEventTurn`
- `llm.go` — `rebuildLLMProvider`, provider management

---

### L2. ACP response types lack `Validate()` methods

**File**: [common/spec/acp/types.go](common/spec/acp/types.go)  
DTOs like `ToolCallRequest`, `ApprovalDecisionRequest` etc. define fields but no validation methods. Callers must validate after unmarshalling, which is error-prone.

---

### L3. `DefaultConfig` in retry package is a mutable global

**File**: [common/retry/retry.go](common/retry/retry.go#L36)  
`var DefaultConfig = Config{...}` can be mutated by any caller, affecting all future uses. Very unlikely in practice, but exporting a function `DefaultConfig()` that returns a copy would be cleaner.

---

### L4. Gosuto `messaging.allowed_targets` alias uniqueness not checked

**File**: [common/spec/gosuto/validate.go](common/spec/gosuto/validate.go)  
Duplicate aliases in `AllowedTargets` would cause ambiguity in LLM prompts but are not rejected during validation.

---

### L5. Missing tests for `common/trace` package

**File**: [common/trace/trace.go](common/trace/trace.go)  
No unit tests for `GenerateID()`, `WithTraceID()`, or `FromContext()`. Simple smoke tests would catch regressions.

---

### L6. OpenAI client has no test for error responses

**File**: [common/llm/openai/client_test.go](common/llm/openai/client_test.go)  
No tests for non-200 status codes (400, 429, 500), malformed responses with missing `choices`, or timeout behavior. This is especially important given C1 above.

---

## Security Observations

### Strengths

| Area | Implementation | Invariant |
|------|---------------|-----------|
| Secret isolation | Kuze one-time tokens; base64 over HTTPS; TTL eviction | §4, §8, §11 |
| Secret-in-chat guardrail | Vendor-specific + generic patterns; isCommand flag avoids false positives | §8 |
| HMAC webhook auth | Constant-time via `hmac.Equal` in `webhookauth` package | §9 |
| Policy engine | First-match-wins, default deny, constraint evaluation before execution | §2, §7 |
| Approval gating | Interactive Matrix approvals with TTL, required for destructive ops | §5 |
| Immutable runtime | Agent cannot modify its own config/binary | §3 |
| Audit trail | Trace IDs propagated through all execution paths | §6 |
| Template auto-escaping | Kuze HTML forms use `html/template` — no XSS | — |
| Secret store atomicity | `Store.Apply()` decodes all values first, then atomically replaces the map | §8 |
| Gosuto strict parsing | `yaml.Decoder.KnownFields(true)` rejects unknown fields | §10 |

### Weaknesses

| Issue | Severity | Reference |
|-------|----------|-----------|
| Bearer token timing side-channel | Medium | C2 |
| Kuze form missing CSRF | Low-Medium | H3 |
| Redact pattern gaps | Medium | H4 |
| Idempotency cache memory leak | Low | C3 |
| Error response leaks endpoint availability (ACP) | Very Low | — |

---

## Architecture Notes

### What works well

1. **Separation of Ruriko and Gitai** — the control plane and agent runtime share only `common/` types with no circular dependencies.
2. **`DispatchToolCall` as single execution boundary** — all tool execution (LLM, workflow, gateway, control) flows through one function with policy evaluation, approval checks, and audit logging.
3. **Gosuto loader + policy engine** — config is immutable once loaded; the policy engine reads it via a getter function, so hot-swaps are clean.
4. **Secret flow** — the Kuze → Manager → Store chain ensures secrets are never logged, have TTL expiry, and are evicted by a background goroutine.
5. **Supervisor pattern** — MCP servers, cron gateways, and external gateways all use the supervised-process pattern with reconciliation on config changes.

### What could improve

1. **Interface usage for stores** — the Ruriko and Gitai stores are concrete types. Introducing interfaces would make testing easier and reduce the need for SQLite in unit tests.
2. **Error type standardization** — error messages mix styles (`"policy denied"`, `"Policy denied"`, `"policy: denied"`). Consider error type constants or a shared error package.
3. **Observability gaps** — rate limiter has no stats/metrics API; idempotency cache has no size indicator; MCP client doesn't expose connection state.

---

## Test Coverage Assessment

### Well-tested areas
- Gosuto validation (comprehensive table-driven tests)
- Policy engine (allow/deny/requireApproval paths, constraint checking)
- Webhook HMAC (valid, invalid, tampered, empty cases)
- Crypto (roundtrip, key validation, tampering detection)
- Built-in tools (matrix.send_message, schedule.*)
- Guardrail (named patterns, generic patterns, isCommand differentiation)
- Router (command parsing, flag extraction, NL dispatch)
- Secrets store/manager (apply, get, TTL, eviction)

### Gaps to address
- OpenAI client: no tests for error status codes, API error responses, timeouts
- Concurrency: no stress tests for concurrent `DispatchToolCall`, config hot-swap + tool dispatch race
- Edge cases: secrets expiring mid-turn, MCP process crash mid-call, approval timeout during tool execution
- Rate limiter: no tests for high cardinality (many unique keys), memory growth
- Trace package: no unit tests at all

---

## Summary of Findings

| ID | Severity | Summary | File |
|----|----------|---------|------|
| C1 | Critical | OpenAI client ignores HTTP error codes | common/llm/openai/client.go |
| C2 | Critical | Bearer token uses `!=` not constant-time | internal/gitai/control/server.go |
| C3 | Critical | Idempotency cache never evicts | internal/gitai/control/server.go |
| H1 | High | Rate limiter bucket map unbounded | common/ratelimit/fixed_window.go |
| H2 | High | Retry backoff has no jitter | common/retry/retry.go |
| H3 | High | Kuze form missing CSRF token | internal/ruriko/kuze/server.go |
| H4 | High | Redact pattern list incomplete | common/redact/redact.go |
| H5 | High | Policy constraints poorly documented/validated | internal/gitai/policy/engine.go |
| M1 | Medium | Distributor partial failure semantics unclear | internal/ruriko/secrets/distributor.go |
| M2 | Medium | Token estimation crude (4 chars/token) | common/memory/context.go |
| M3 | Medium | MCP scanner buffer hardcoded at 1 MB | internal/gitai/mcp/client.go |
| M4 | Medium | Workflow step cross-references not validated in Gosuto | common/spec/gosuto/validate.go |
| M5 | Medium | Docker network failure swallowed | internal/ruriko/app/app.go |
| M6 | Medium | Envelope message field not validated | common/spec/envelope/event.go |
| L1 | Low | Gitai app.go too large | internal/gitai/app/app.go |
| L2 | Low | ACP DTOs lack Validate() methods | common/spec/acp/types.go |
| L3 | Low | Mutable global DefaultConfig | common/retry/retry.go |
| L4 | Low | Messaging target alias uniqueness unchecked | common/spec/gosuto/validate.go |
| L5 | Low | No tests for trace package | common/trace/trace.go |
| L6 | Low | No error-path tests for OpenAI client | common/llm/openai/client_test.go |

---

## Package-Level Grades

### common/

| Package | Grade | Key Observation |
|---------|-------|-----------------|
| crypto | A | Solid AES-GCM, proper nonce handling |
| environment | A- | Clean helpers; silent parse failures by design |
| llm/openai | C+ | **Ignores HTTP errors** — needs fix |
| matrixcore | B+ | Good backoff logic, no tests (wrapper) |
| memory | B | Crude token estimates, LTM errors silenced |
| ratelimit | B | Correct atomicity; **unbounded bucket map** |
| redact | B- | **Incomplete patterns**, no nested redaction |
| retry | B+ | Good design; **no jitter** |
| spec/acp | B | No validation methods on DTOs |
| spec/envelope | A- | Good validation; message emptiness unchecked |
| spec/gosuto | A- | Comprehensive; minor cross-ref gaps |
| sqliteutil | A- | Safe migrations, good test coverage |
| trace | A- | Clean; missing tests |
| version | A | Simple, correct |
| webhookauth | A+ | Constant-time HMAC, excellent tests |

### internal/gitai/

| Package | Grade | Key Observation |
|---------|-------|-----------------|
| app | B+ | Well-structured turn loop; large file; correct policy→secrets ordering |
| approvals | A- | Clean gate pattern with Matrix integration |
| builtin | A- | Good tool registry; per-sender rate limiting would improve matrix.send |
| control | B | **Bearer timing + cache leak**; otherwise solid ACP server |
| gateway | A | HMAC delegated correctly; clean event wrapping |
| gosuto | A | Simple, correct loader |
| llm | A- | Clean provider abstraction |
| matrix | B+ | Good wrapper; no tests |
| mcp | B+ | JSON-RPC over stdio works; buffer size hardcoded |
| observability | A | Clean slog setup |
| policy | B+ | Correct first-match-wins; constraint handling weak |
| secrets | A- | TTL cache + eviction sweep; well-documented |
| store | A- | Clean SQLite with migrations |
| supervisor | A- | Good reconcile pattern for MCP + gateways |
| workflow | B+ | Engine works; still maturing |

### internal/ruriko/

| Package | Grade | Key Observation |
|---------|-------|-----------------|
| app | B+ | Complex wiring; Docker network error swallowed |
| approvals | A- | Good gate + parser + store design |
| audit | A | Clean notifier with trace ID propagation |
| commands | A- | Deterministic router; good guardrail; flag sanitization duplicated |
| config | A | Simple key-value config store |
| kuze | B+ | One-time links work; **missing CSRF** |
| matrix | A- | Clean mautrix wrapper |
| secrets | B+ | Distributor partial-success semantics need docs |
| store | A- | Well-organized domain-split SQLite store |
| runtime | A- | Docker adapter + ACP client; network init concern |
| webhook | B+ | Proxy works; gateway config race window exists |

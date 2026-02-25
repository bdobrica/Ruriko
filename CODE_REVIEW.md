# Ruriko Code Review — Operations & Security

Date: 2026-02-25  
Reviewer: GitHub Copilot (GPT-5.3-Codex)

## Scope

This review focused on:

- Architecture intent and UX contract in `docs/preamble.md`
- Security model and controls in `README.md`, `docs/invariants.md`, `docs/threat-model.md`
- Operational guidance in `OPERATIONS.md`, `docs/ops/*.md`
- Security- and ops-critical implementation paths in:
  - `internal/ruriko/commands/*`
  - `internal/ruriko/webhook/*`
  - `internal/ruriko/runtime/acp/*`
  - `internal/gitai/control/*`
  - `examples/docker-compose/docker-compose.yaml`

## Executive Summary

Ruriko’s architecture is strong and coherent: deterministic control plane, policy-first execution, explicit trust contexts, scoped secret distribution, and good runtime separation between Matrix/ACP/Kuze.

The main risk is **policy drift** between stated invariants and shipped behavior. The highest-priority issue is that Matrix-based inline secret entry is still accepted in command handlers and operational docs, which conflicts with your explicit invariant that secrets must never enter via Matrix.

## Priority Findings

### P0 — Invariant breach: Matrix inline secret values still accepted

**Severity**: Critical  
**Why it matters**: Violates your strongest safety invariant and increases secret leakage risk through homeserver/client history.

**Spec / docs say**

- `docs/invariants.md` §11: `/ruriko secrets set <name>` must issue one-time Kuze link and never accept inline value.
- `README.md`: secrets are entered via Kuze one-time secure links; never via Matrix.

**Implementation observed**

- `internal/ruriko/commands/secrets_handlers.go` supports:
  - `/ruriko secrets set <name> --type <type> --value <base64>`
  - `/ruriko secrets rotate <name> --value <base64>`
- Responses include warnings but still process secret material over Matrix.

**Implementation guidance**

1. Remove all inline `--value` command paths from Matrix handlers.
2. Require Kuze for `secrets set` and `secrets rotate` entry flow.
3. If Kuze is unavailable, fail safely with explicit remediation message.
4. Keep non-chat ingestion paths (provisioning internals) unchanged.

**Acceptance criteria**

- Matrix command handlers do not accept secret values in arguments.
- Help/ops docs no longer recommend inline secret value usage.
- Existing behavior remains deterministic and auditable.

---

### P0 — Secret guardrail active only when Kuze is configured

**Severity**: Critical  
**Why it matters**: In non-Kuze deployments, secret-looking content can pass through chat processing.

**Implementation observed**

- In `internal/ruriko/app/app.go`, secret pattern guardrail is conditional on `a.kuzeServer != nil`.

**Implementation guidance**

1. Enforce secret-in-chat detection regardless of Kuze availability.
2. Keep behavior deterministic: reject with guidance response.
3. Update comments to remove “dev mode allows inline secrets” framing.

**Acceptance criteria**

- Any inbound message matching secret patterns is rejected in all modes.
- Guidance points to Kuze-based `/ruriko secrets set <name>` workflow.

---

### P1 — Control-plane transport hardening drift (documentation vs current runtime)

**Severity**: High  
**Why it matters**: Threat model suggests HTTPS/mTLS for ACP, while implementation is internal HTTP bearer-token model.

**Observed**

- Threat model C2 includes HTTPS/TLS and bearer or mTLS.
- Docker runtime control URLs are `http://<container-ip>:<port>`.

**Implementation options**

- Option A (recommended short-term): explicitly document current trust boundary as private docker network + bearer tokens, and mark mTLS as roadmap.
- Option B (hardening): introduce mTLS between Ruriko and Gitai ACP endpoints.

---

### P1 — Container hardening controls overstate current deployment defaults

**Severity**: High  
**Why it matters**: Threat model says no Docker socket access; Compose mounts socket to Ruriko by design for lifecycle management.

**Observed**

- `examples/docker-compose/docker-compose.yaml` mounts `/var/run/docker.sock:ro` and adds docker group.
- `docs/ops/deployment-docker.md` correctly notes root-equivalent risk.

**Implementation guidance**

1. Align threat-model language with real operating modes.
2. Provide explicit “hardened mode” compose profile using Docker socket proxy.
3. Add capability drops/read-only FS/resource limits where compatible.

---

### P2 — Endpoint naming/documentation drift

**Severity**: Medium  
**Why it matters**: Causes operator confusion and integration errors.

**Observed**

- README refers to `/webhook/{agent}/{source}` while implementation registers `/webhooks/{agent}/{source}`.

**Implementation guidance**

1. Standardize on one route path and update all docs/examples.
2. Optionally keep compatibility alias for one release cycle.

---

### P2 — Security operations controls partially documented but not fully operationalized

**Severity**: Medium  
**Why it matters**: Ongoing exposure to dependency/supply-chain risk.

**Observed**

- Threat model references `govulncheck` and SBOM generation as controls.
- No clear CI enforcement found in this review scope.

**Implementation guidance**

1. Add CI jobs: `govulncheck ./...`, image scanning, SBOM artifact generation.
2. Define fail threshold policy (e.g., fail on critical/high in runtime paths).

## Positive Security Posture Observed

- Deterministic command routing and approval checks for destructive operations.
- Per-agent ACP bearer token generation and propagation.
- Token-based secret distribution (`/secrets/token`) and direct push disabled by default.
- Event ingress validation, source checks, request body caps, and rate limiting.
- Policy-gated inter-agent messaging with target allowlists and message rate limits.

## Recommended Implementation Sequence

1. **Immediate (P0)**
   - Remove inline Matrix secret entry/rotation pathways.
   - Make secret guardrail unconditional.
   - Update help + operations docs to match behavior.

2. **Next (P1)**
   - Reconcile ACP/TLS documentation with implemented trust boundary.
   - Publish hardened deployment profile with socket proxy + tighter container settings.

3. **Then (P2)**
   - Resolve route naming drift (`/webhooks` vs `/webhook`).
   - Operationalize dependency and SBOM controls in CI.

## Validation Checklist (for P0 changes)

- `/ruriko secrets set ... --value ...` returns refusal/guidance, never stores value.
- `/ruriko secrets rotate ... --value ...` returns refusal/guidance, never consumes inline value.
- `/ruriko secrets set <name> --type <type>` issues Kuze link when Kuze is enabled.
- Secret-pattern messages are blocked whether Kuze is enabled or not.
- Command help and operations docs no longer advertise Matrix inline secret value workflows.

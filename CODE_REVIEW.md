# Ruriko Code Review — Operations & Security

Date: 2026-02-25  
Last Updated: 2026-02-26  
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

The original high-priority policy-drift findings in this review were remediated during the follow-up implementation pass (P0 and P1, plus P2 docs/path consistency). Remaining work is primarily operationalization (CI vulnerability/SBOM enforcement).

## Remediation Status (as of 2026-02-26)

- ✅ **P0 closed** — Matrix inline secret entry/rotation removed from chat command path; guardrail made unconditional; docs/help updated.
   - `internal/ruriko/commands/secrets_handlers.go`
   - `internal/ruriko/app/app.go`
   - `internal/ruriko/commands/handlers.go`
   - `internal/ruriko/commands/provisioning_handlers.go`
   - `OPERATIONS.md`, `.env.example`, `examples/docker-compose/docker-compose.yaml`
- ✅ **P1 (ACP trust-boundary docs) closed** — MVP now explicitly documented as private Docker network + bearer auth; mTLS moved to roadmap/TODO.
   - `docs/preamble.md`
   - `docs/threat-model.md` (ACP controls section)
   - `docs/architecture.md` (ACP transport/auth sections)
   - `TODO.md` (explicit mTLS roadmap item)
- ✅ **P1 (container hardening wording + hardened profile) closed** — threat model aligned with real operating modes; hardened deployment checklist and compose profile added.
   - `docs/threat-model.md` (container controls section)
   - `docs/ops/deployment-docker.md` (hardened checklist)
   - `examples/docker-compose/docker-compose.yaml` (commented hardened profile)
   - `.env.example` (`DOCKER_HOST` / `DOCKER_GID` guidance)
   - `OPERATIONS.md` (Hardened Mode Quick-Start)
- ✅ **P2 docs/path drift closed** — webhook path standardized to `/webhooks/{agent}/{source}`; gosuto spec updated to token-based secret distribution wording.
   - `README.md`, `docs/threat-model.md`, `docs/architecture.md`
   - `docs/gosuto-spec.md`

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

**Status**: ✅ Closed via Option A

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

**Status**: ✅ Closed

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

**Status**: ✅ Closed

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

1. **Immediate next**
   - Operationalize dependency and SBOM controls in CI.
2. **Then**
   - Add hardened deployment verification examples/tests for socket-proxy mode.
3. **Roadmap**
   - Implement ACP mTLS for multi-host / untrusted-network topologies.

## Validation Checklist (for P0 changes)

- `/ruriko secrets set ... --value ...` returns refusal/guidance, never stores value.
- `/ruriko secrets rotate ... --value ...` returns refusal/guidance, never consumes inline value.
- `/ruriko secrets set <name> --type <type>` issues Kuze link when Kuze is enabled.
- Secret-pattern messages are blocked whether Kuze is enabled or not.
- Command help and operations docs no longer advertise Matrix inline secret value workflows.

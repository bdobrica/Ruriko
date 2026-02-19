# Ruriko Threat Model

> **Security analysis and threat mitigation strategies**

**Version**: 0.1.0  
**Last Updated**: 2026-02-19  
**Status**: Living Document

---

## Table of Contents

1. [Overview](#overview)
2. [Assets](#assets)
3. [Threat Actors](#threat-actors)
4. [Attack Surface](#attack-surface)
5. [Threat Scenarios](#threat-scenarios)
6. [Mitigations](#mitigations)
7. [Security Controls](#security-controls)
8. [Incident Response](#incident-response)

---

## Overview

This document analyzes security threats to the Ruriko system and documents mitigation strategies. It follows the STRIDE methodology (Spoofing, Tampering, Repudiation, Information Disclosure, Denial of Service, Elevation of Privilege).

**Threat Modeling Approach**:
- Asset-based: What needs protection?
- Attacker-centric: Who might attack and why?
- Defense-in-depth: Multiple layers of protection

> For full product context and the canonical UX contract see
> [preamble.md](./preamble.md).

---

## Deployment Topology (MVP)

The MVP targets a **single-host deployment** with the following security properties:

- **Tuwunel** is the bundled Matrix homeserver (lightweight, single-binary)
- **Matrix federation: OFF** — no inbound traffic from the public Matrix network
- **Matrix registration: OFF** — accounts are provisioned explicitly by Ruriko
- **ACP endpoints** (Gitai agent control) are reachable only inside the Docker bridge network and are never exposed publicly
- **Kuze** (embedded in Ruriko) provides one-time HTTPS links for secure secret entry — secrets never travel through Matrix
- All services (Ruriko, Tuwunel, Gitai agents) run on the same VPS via Docker Compose
- The Matrix homeserver port is exposed to the internet so the operator's Matrix client can connect; all other ports are internal

**Threat surface reduction from this topology**:
- No federated traffic eliminates a large class of spoofing and injection attacks
- No open registration eliminates account-based DoS and infiltration
- Private ACP network eliminates unauthenticated ACP access from the internet
- Single host means secrets on disk require host-level compromise to access

---

## Assets

### Critical Assets (Highest Value)

1. **Master Encryption Key**
   - Encrypts all secrets at rest
   - Compromise = full system compromise
   - **Protection level**: CRITICAL

2. **Agent Secrets**
   - API keys, tokens, credentials
   - Compromise = agent impersonation, external service access
   - **Protection level**: HIGH

3. **Ruriko Database**
   - Agent inventory, audit logs, approvals
   - Tampering = loss of trust, policy bypass
   - **Protection level**: HIGH

4. **Gosuto Configurations**
   - Define agent capabilities and constraints
   - Tampering = privilege escalation
   - **Protection level**: HIGH

5. **Matrix Access Tokens**
   - Ruriko admin token
   - Agent tokens
   - Compromise = impersonation, unauthorized commands
   - **Protection level**: HIGH

### Important Assets

6. **Audit Logs**
   - Accountability and forensics
   - Tampering = loss of accountability
   - **Protection level**: MEDIUM

7. **Approval Records**
   - Decision history
   - Tampering = unauthorized approvals
   - **Protection level**: MEDIUM

8. **Agent Runtime Binaries**
   - Gitai executables
   - Tampering = backdoors, malicious behavior
   - **Protection level**: MEDIUM

---

## Threat Actors

### External Attackers

**Motivation**: Financial gain, espionage, disruption  
**Capabilities**: Network access, social engineering  
**Entry points**: Matrix homeserver, exposed APIs, compromised dependencies

**Typical attacks**:
- Exploit vulnerabilities in Ruriko/Gitai
- Phishing for Matrix credentials
- Supply chain attacks (malicious MCP servers)
- Denial of service

---

### Malicious Insiders

**Motivation**: Sabotage, data theft, revenge  
**Capabilities**: Some level of authorized access  
**Entry points**: Admin Matrix account, host access

**Typical attacks**:
- Abuse admin commands
- Exfiltrate secrets
- Delete agents or data
- Modify Gosuto to grant excessive permissions

---

### Compromised Agents

**Motivation**: Attacker control after successful exploitation  
**Capabilities**: Agent's authorized capabilities + any vulnerabilities  
**Entry points**: Exploited Gitai vulnerability, malicious tool

**Typical attacks**:
- Lateral movement to other agents
- Exfiltrate data via tools
- Amplify privileges via policy bugs
- Denial of service

---

### Curious Users

**Motivation**: Experimentation, curiosity  
**Capabilities**: Normal user Matrix account  
**Entry points**: Matrix messages to agents

**Typical attacks**:
- Prompt injection
- Social engineering agents
- Denial of service (spam)
- Information disclosure via crafted queries

---

## Attack Surface

### 1. Matrix Protocol Surface

**Entry Points**:
- Ruriko admin room messages
- Agent room messages (human users, other agents)
- Direct messages
- Room invites

**Risks**:
- Command injection
- Spoofing (impersonating authorized users)
- Prompt injection
- Message flooding (DoS)

**Mitigations**:
→ See [Matrix Security Controls](#1-matrix-security-controls)

---

### 2. Agent Control Protocol Surface

**Entry Points**:
- HTTP endpoints (`/health`, `/status`, `/config/apply`, `/secrets/apply`, `/process/restart`)

**Risks**:
- Unauthorized access (no auth)
- Man-in-the-middle
- Replay attacks
- Malicious Gosuto injection
- Secret interception

**Mitigations**:
→ See [ACP Security Controls](#2-agent-control-protocol-acp-security-controls)

---

### 3. MCP Tool Surface

**Entry Points**:
- Tool calls from Gitai to MCP servers
- MCP server responses

**Risks**:
- Malicious tool execution
- Unsafe tool arguments (command injection, path traversal)
- Tool result tampering
- Secret leakage via tool results
- Denial of service (resource exhaustion)

**Mitigations**:
→ See [MCP Security Controls](#3-mcp-security-controls)

---

### 4. Dependency Surface

**Entry Points**:
- Go dependencies
- MCP servers (third-party)
- LLM provider APIs
- Container images

**Risks**:
- Vulnerable dependencies
- Supply chain attacks
- Backdoors in third-party tools
- Malicious updates

**Mitigations**:
→ See [Dependency Security Controls](#4-dependency-security-controls)

---

### 5. Host/Container Surface

**Entry Points**:
- Docker socket
- Kubernetes API
- Host filesystem
- Environment variables

**Risks**:
- Container escape
- Host compromise
- Access token leakage
- Privilege escalation

**Mitigations**:
→ See [Container Security Controls](#5-container-security-controls)

---

## Threat Scenarios

### Scenario 1: Compromised Agent Attempts Privilege Escalation

**Attack Flow**:
1. Attacker exploits vulnerability in Gitai agent
2. Gains code execution within agent container
3. Attempts to modify Gosuto to grant broader capabilities
4. Attempts to access secrets outside agent's scope
5. Attempts to compromise other agents or Ruriko

**Mitigations**:
- ✅ **Immutable runtime**: Gitai binary and Gosuto are read-only in container
- ✅ **ACP authentication**: Agent cannot call Ruriko's admin endpoints
- ✅ **Secret scoping**: Agent can only access bound secrets
- ✅ **Container isolation**: Agent cannot access other containers
- ✅ **Audit logging**: Compromise attempts are logged

**Residual Risk**: LOW (multiple layers of defense)

---

### Scenario 2: Admin Account Compromise

**Attack Flow**:
1. Attacker phishes admin's Matrix password
2. Logs into Matrix as admin
3. Sends commands to Ruriko: delete agents, exfiltrate secrets, modify policies

**Mitigations**:
- ✅ **Approval workflow**: Destructive operations require approval from multiple parties
- ✅ **Audit logging**: All commands logged with actor MXID
- ✅ **Command restrictions**: Some operations (e.g., "dump all secrets") are not implemented
- ⚠️ **MFA**: Should be enforced by Matrix homeserver (admin responsibility)
- ⚠️ **Session management**: Revoke old sessions regularly

**Residual Risk**: MEDIUM (depends on Matrix security + approval enforcement)

---

### Scenario 3: Prompt Injection to Bypass Policy

**Attack Flow**:
1. User sends crafted message to agent
2. Message contains instructions to ignore Gosuto rules
3. Agent's LLM interprets instructions and attempts disallowed action

**Mitigations**:
- ✅ **Policy-first architecture**: LLM proposes actions, policy engine approves/denies
- ✅ **Deterministic enforcement**: Policy is code, not prompts
- ✅ **Default deny**: Actions not explicitly allowed are blocked
- ✅ **Capability checking**: Every tool call validated against Gosuto

**Residual Risk**: LOW (prompt injection cannot bypass code-enforced policy)

---

### Scenario 4: Malicious MCP Server

**Attack Flow**:
1. Admin installs third-party MCP server
2. MCP server contains backdoor or vulnerability
3. Agent calls tool, MCP server exploits agent or exfiltrates data

**Mitigations**:
- ⚠️ **MCP server vetting**: Admins should only install trusted MCPs
- ✅ **Process isolation**: MCP runs in separate process, not agent process
- ✅ **Constraint enforcement**: URL filters, payload size limits
- ✅ **Result redaction**: Secrets removed from tool results
- ⚠️ **Sandboxing**: Future: run MCPs in restricted containers/VMs

**Residual Risk**: MEDIUM (depends on MCP trustworthiness)

---

### Scenario 5: Secret Exfiltration via Tool Call

**Attack Flow**:
1. Agent has access to secret (e.g., API key)
2. Attacker tricks agent into calling tool that logs/sends data externally
3. Tool result contains secret, sent to attacker-controlled endpoint

**Mitigations**:
- ✅ **Redaction middleware**: Secrets removed from logs and tool traces
- ✅ **URL allowlists**: Constrain which URLs tools can access
- ✅ **Approval gates**: Risky tools require approval
- ⚠️ **DLP**: Future: content inspection on tool results

**Residual Risk**: MEDIUM (depends on constraint configuration)

---

### Scenario 6: Database Tampering

**Attack Flow**:
1. Attacker gains filesystem access to Ruriko host
2. Modifies SQLite database directly (bypass Ruriko logic)
3. Escalates agent privileges, approves own requests, erases audit logs

**Mitigations**:
- ✅ **Filesystem permissions**: Database file only readable by Ruriko process
- ✅ **Integrity checks**: Gosuto hashes verified on load
- ⚠️ **Encryption at rest**: Future: encrypt entire database
- ⚠️ **Append-only logs**: Future: write audit logs to immutable storage

**Residual Risk**: MEDIUM (depends on host security)

---

### Scenario 7: Denial of Service

**Attack Flow**:
1. Attacker floods agent with messages
2. Agent exhausts LLM token budget or MCP resources
3. Legitimate users cannot use agent

**Mitigations**:
- ✅ **Rate limits**: Gosuto defines max operations per time period
- ✅ **Sender allowlists**: Only known users can message agents
- ✅ **Token budgets**: Max tokens per day enforced
- ⚠️ **Backpressure**: Future: queue management, prioritization

**Residual Risk**: LOW (rate limits mitigate most DoS scenarios)

---

### Scenario 8: Supply Chain Attack

**Attack Flow**:
1. Attacker compromises a Go dependency or base container image
2. Malicious code injected into Ruriko or Gitai build
3. Backdoor grants attacker access

**Mitigations**:
- ✅ **Dependency pinning**: `go.mod` locks versions
- ✅ **Checksum verification**: `go.sum` verifies integrity
- ⚠️ **Dependency scanning**: Use tools like `govulncheck`
- ⚠️ **Minimal base images**: Use distroless or scratch
- ⚠️ **SBOM**: Generate Software Bill of Materials for auditing

**Residual Risk**: MEDIUM (supply chain is hard to fully secure)

---

## Mitigations

### Summary of Mitigations by Threat Type

| Threat Type                  | Primary Mitigations                              | Residual Risk |
|------------------------------|--------------------------------------------------|---------------|
| Spoofing                     | Matrix auth, sender allowlists, ACP auth         | LOW           |
| Tampering                    | Gosuto hashing, read-only config, audit logs     | MEDIUM        |
| Repudiation                  | Audit logs, trace IDs, immutable records         | LOW           |
| Information Disclosure       | Secret redaction, encryption at rest, scoping    | MEDIUM        |
| Denial of Service            | Rate limits, resource quotas, token budgets      | LOW           |
| Elevation of Privilege       | Policy enforcement, approval gates, isolation    | LOW           |

---

## Security Controls

### 1. Matrix Security Controls

**C1.1: Sender Allowlists**
- Gosuto defines allowed MXIDs or room members
- Messages from unknown senders are ignored
- Logged but not processed

**C1.2: Room Allowlists**
- Agents only join and process messages from allowed rooms
- Prevents cross-contamination

**C1.3: E2EE Enforcement (Optional)**
- Gosuto can require E2EE for sensitive rooms
- Verified devices only

**C1.4: Power Level Checks**
- Ruriko checks Matrix power levels for admin ops
- Only users with sufficient power can execute destructive commands

---

### 2. Agent Control Protocol (ACP) Security Controls

**C2.1: Authentication**
- Bearer token or mTLS required for ACP endpoints
- Token stored securely, rotated periodically

**C2.2: Network Isolation**
- ACP endpoints listen on localhost or private network only
- No public internet exposure

**C2.3: Request Validation**
- Schema validation on all ACP payloads
- Reject malformed requests

**C2.4: HTTPS/TLS**
- All ACP traffic encrypted in transit
- Certificate pinning (optional, for mTLS)

---

### 3. MCP Security Controls

**C3.1: Process Isolation**
- MCP servers run as separate processes
- Limited resource quotas (CPU, memory)

**C3.2: Argument Validation**
- Policy engine validates tool arguments against constraints
- URL filters (allowlist/denylist)
- Path traversal prevention
- Command injection prevention

**C3.3: Result Sanitization**
- Redact secrets from tool results
- Limit result size
- Content filtering (optional)

**C3.4: Approval Gates**
- High-risk tools (browser, shell, fs.write) require approval
- Cannot be bypassed by LLM

---

### 4. Dependency Security Controls

**C4.1: Dependency Scanning**
- Run `govulncheck` in CI/CD
- Fail builds on critical vulnerabilities

**C4.2: Pinned Versions**
- Lock versions in `go.mod`
- Review updates before merging

**C4.3: Minimal Dependencies**
- Avoid unnecessary libraries
- Prefer standard library when possible

**C4.4: SBOM Generation**
- Generate SBOM (Software Bill of Materials) for each release
- Track all transitive dependencies

---

### 5. Container Security Controls

**C5.1: Non-Root User**
- Run containers as non-root UID
- Drop all capabilities

**C5.2: Read-Only Filesystem**
- Mount `/` as read-only
- Only writable: `/tmp`, `/data` (bind mounts)

**C5.3: No Privileged Mode**
- Never use `--privileged` flag
- No access to Docker socket

**C5.4: Resource Limits**
- Set CPU and memory limits
- Prevent resource exhaustion

**C5.5: Network Policies**
- Restrict egress (optional)
- Only allow necessary endpoints

---

### 6. Secrets Management Controls

**C6.1: Encryption at Rest**
- All secrets encrypted with AES-GCM
- Master key loaded from env, not stored in DB

**C6.2: Secret Scoping**
- Agents only receive bound secrets
- Scopes enforced in database

**C6.3: Rotation**
- Secrets can be rotated independently
- Old versions invalidated

**C6.4: No Plaintext Logging**
- Redaction middleware removes secrets from logs
- Secrets never included in audit logs or error messages

---

### 7. Audit and Monitoring Controls

**C7.1: Comprehensive Logging**
- All commands, tool calls, approvals logged
- Includes trace IDs for correlation

**C7.2: Tamper-Evident Logs**
- Audit logs append-only (in future: immutable storage)
- Hashing for integrity

**C7.3: Alerting**
- Failed auth attempts
- Approval denials
- Policy violations
- Unusual activity patterns

**C7.4: Retention**
- Audit logs retained for compliance period
- Backed up regularly

---

## Incident Response

### Detection

**Indicators of Compromise**:
- Multiple failed authentication attempts
- Policy violations by trusted agents
- Unauthorized approval attempts
- Unexpected process crashes
- Audit log gaps or anomalies
- Resource exhaustion

**Monitoring**:
- Watch audit logs for suspicious patterns
- Set up alerts in Matrix audit room
- Monitor agent health via `/status` endpoints

---

### Response Plan

#### Phase 1: Containment

1. **Identify affected agents**
   - Check audit logs, trace IDs
   - Correlate timeline

2. **Isolate compromised agents**
   - Stop affected containers: `/ruriko agents stop <name>`
   - Remove from rooms
   - Revoke Matrix tokens

3. **Preserve evidence**
   - Snapshot database
   - Save container logs
   - Export audit logs

#### Phase 2: Investigation

1. **Analyze logs**
   - Review audit trail
   - Identify attack vector
   - Scope of compromise

2. **Check integrity**
   - Verify Gosuto hashes
   - Check binary checksums
   - Review recent approvals

3. **Identify root cause**
   - Vulnerability, misconfiguration, social engineering?

#### Phase 3: Remediation

1. **Patch vulnerabilities**
   - Update dependencies
   - Fix bugs
   - Rebuild containers

2. **Rotate secrets**
   - `/ruriko secrets rotate <name>` for all exposed secrets

3. **Update policies**
   - Tighten Gosuto constraints if needed
   - Review approval workflows

4. **Restore from backup**
   - If database tampered, restore clean version

#### Phase 4: Recovery

1. **Respawn agents**
   - `/ruriko agents respawn <name>` with patched runtime

2. **Verify functionality**
   - Test basic operations
   - Confirm policy enforcement

3. **Resume normal operations**

#### Phase 5: Post-Incident Review

1. **Document incident**
   - Timeline, impact, root cause
   - Lessons learned

2. **Update threat model**
   - Add new scenarios
   - Refine mitigations

3. **Improve controls**
   - Implement additional safeguards
   - Update monitoring

---

## Security Checklist

Use this checklist when deploying Ruriko:

### Pre-Deployment

- [ ] Review and understand system invariants
- [ ] Configure Matrix homeserver securely (HTTPS, rate limits)
- [ ] Enable Matrix MFA for admin accounts
- [ ] Generate strong master encryption key
- [ ] Store master key in secure location (not in repo)
- [ ] Review default Gosuto templates (principle of least privilege)
- [ ] Set up admin and approval rooms with proper power levels
- [ ] Harden host/container environment

### Post-Deployment

- [ ] Verify Ruriko connects to Matrix successfully
- [ ] Test approval workflow end-to-end
- [ ] Verify audit logs are being written
- [ ] Test agent lifecycle (create/stop/respawn)
- [ ] Verify secrets encryption and scoping
- [ ] Configure alerting for suspicious activity
- [ ] Set up backup schedule for database
- [ ] Document recovery procedures

### Ongoing

- [ ] Regularly review audit logs
- [ ] Rotate secrets periodically
- [ ] Update dependencies monthly
- [ ] Run vulnerability scans
- [ ] Review and update Gosuto policies
- [ ] Test disaster recovery procedures
- [ ] Conduct periodic security reviews

---

## Future Enhancements

### Planned Security Improvements

1. **Secret Management**
   - Integration with HashiCorp Vault or similar
   - Short-lived secret leases
   - Automatic rotation

2. **Audit Logging**
   - Write-once storage (S3, append-only filesystem)
   - Cryptographic log signing
   - Real-time streaming to SIEM

3. **MCP Sandboxing**
   - Run MCP servers in lightweight VMs (Firecracker)
   - Network policies per MCP
   - Resource quotas enforced by kernel

4. **Anomaly Detection**
   - ML-based behavioral analysis
   - Automatic alerts on unusual patterns
   - Integration with SOC workflows

5. **Zero-Trust Networking**
   - mTLS for all agent-to-agent communication
   - Service mesh integration (Istio, Linkerd)
   - Certificate rotation

6. **Formal Verification**
   - Policy engine correctness proofs
   - Constraint solver verification
   - Model checking critical paths

---

## References

- [preamble.md](./preamble.md) - Product story, UX contract, and canonical glossary
- [invariants.md](./invariants.md) - System invariants
- [architecture.md](./architecture.md) - System architecture
- [OWASP Top 10](https://owasp.org/www-project-top-ten/)
- [STRIDE Methodology](https://en.wikipedia.org/wiki/STRIDE_(security))
- [CWE Top 25](https://cwe.mitre.org/top25/archive/2023/2023_top25_list.html)

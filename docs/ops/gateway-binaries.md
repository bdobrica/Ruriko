# Gateway Binaries in Container Images

This document explains how inbound event gateway binaries are included in the
Gitai container image, how to configure them in a Gosuto agent config, and
how to add new vetted gateway binaries to the build.

---

## Overview

Event gateways are the inbound complement to MCPs. Where MCPs let an agent call
outbound tools, gateways let external events (email, MQTT messages, RSS feed
entries, etc.) trigger agent turns. Each gateway process:

1. Connects to an external source (e.g. an IMAP server).
2. Translates incoming data into a normalised event envelope.
3. POSTs the envelope to the agent's ACP endpoint: `POST /events/{source}`.

Gitai supervises gateway processes the same way it supervises MCP processes —
they run as children of the agent container, are restarted on failure when
`autoRestart: true`, and have secrets injected via environment variables.

---

## Build model: binaries baked in at image build time

Gateway binaries follow the **same model as MCP binaries**: they are compiled
from source inside the Docker builder stage and copied into the runtime image
at a well-known path. No binaries are downloaded at runtime.

```
┌─────────────────────────────────────────────────────────────────┐
│  Docker builder stage (golang:1.25-alpine)                      │
│                                                                  │
│  go build ./cmd/gateway/ruriko-gw-imap  →  /build/gateways/    │
│  go build ./cmd/gateway/ruriko-gw-...   →  /build/gateways/    │
└──────────────────────────────┬──────────────────────────────────┘
                               │ COPY --from=builder
┌──────────────────────────────▼──────────────────────────────────┐
│  Runtime image (alpine:3.21)                                    │
│                                                                  │
│  /usr/local/bin/gitai                     ← Gitai agent runtime │
│  /usr/local/lib/gitai/gateways/           ← Gateway binaries    │
│    ruriko-gw-imap                                               │
└─────────────────────────────────────────────────────────────────┘
```

The full manifest of vetted gateways, their install paths, and their
configuration contracts is in [`deploy/docker/gateway-manifest.yaml`](../../deploy/docker/gateway-manifest.yaml).

---

## Vetted gateway manifest

[`deploy/docker/gateway-manifest.yaml`](../../deploy/docker/gateway-manifest.yaml)
is the single source of truth for gateway binaries that ship with the image. Each
entry records:

| Field | Purpose |
|---|---|
| `name` | Binary name and identifier used in Gosuto `gateways[].name` |
| `source` | Source path within the repository (`cmd/gateway/<name>`) |
| `installPath` | Absolute path inside the container where the binary is installed |
| `placeholder` | `true` if this is a development stub, `false` for production-ready |
| `env` | Environment variables the binary accepts |
| `gosutoSnippet` | Ready-to-use Gosuto YAML snippet |

---

## Configuring a gateway in Gosuto

Reference a built-in gateway binary from your agent's `gosuto.yaml` using the
`command` field:

```yaml
gateways:
  - name: imap
    command: /usr/local/lib/gitai/gateways/ruriko-gw-imap
    env:
      ACP_URL: http://localhost:8765
      GW_SOURCE: imap
      GW_IMAP_HOST: "imap.example.com"
      GW_IMAP_USER: "agent@example.com"
      GW_IMAP_PASSWORD: "${IMAP_PASSWORD}"
    autoRestart: true
```

Secrets (e.g. `IMAP_PASSWORD`) should be pushed to the agent via Ruriko's
secret provisioning pipeline, where they appear as environment variables inside
the gateway process.

---

## Available gateway binaries

### `ruriko-gw-imap` — IMAP email gateway (placeholder)

**Install path:** `/usr/local/lib/gitai/gateways/ruriko-gw-imap`

Polls an IMAP mailbox for new messages and forwards each message as an
`imap.email` event envelope to the agent's ACP endpoint. The IMAP polling loop
is a placeholder in the current release; the binary compiles and runs cleanly
but does not connect to a mail server.

| Variable | Required | Default | Description |
|---|---|---|---|
| `ACP_URL` | yes | — | Agent ACP base URL, e.g. `http://localhost:8765` |
| `ACP_TOKEN` | no | — | ACP bearer token (set to `GITAI_ACP_TOKEN` value) |
| `GW_SOURCE` | yes | — | Gateway source name matching the Gosuto `name` field |
| `GW_IMAP_HOST` | yes | — | IMAP server hostname |
| `GW_IMAP_PORT` | no | `993` | IMAP server port (TLS) |
| `GW_IMAP_USER` | yes | — | IMAP account username |
| `GW_IMAP_PASSWORD` | yes | — | IMAP account password |
| `GW_IMAP_MAILBOX` | no | `INBOX` | Mailbox/folder to watch |
| `GW_POLL_INTERVAL` | no | `60s` | Poll interval (Go duration string) |
| `LOG_FORMAT` | no | `text` | `text` or `json` |

---

## Adding a new gateway binary

1. **Create the source** at `cmd/gateway/<binary-name>/main.go`.  
   Follow the `ruriko-gw-imap` pattern:
   - Accept all config via environment variables.
   - Reproduce the `acpEvent` / `acpEventPayload` types locally (zero in-tree
     dependencies) so the binary is a self-contained artefact.
   - POST to `$ACP_URL/events/$GW_SOURCE` with an optional `Authorization: Bearer $ACP_TOKEN` header.
   - Handle `SIGTERM`/`SIGINT` via `signal.NotifyContext`.
   - Exit with code `1` on configuration error; `0` on clean shutdown.

2. **Add a manifest entry** in `deploy/docker/gateway-manifest.yaml`.

3. **Add a build step** in `deploy/docker/Dockerfile.gitai` inside the
   "Gateway binaries" section of the builder stage:

   ```dockerfile
   # <binary-name>: <short description>
   RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
       go build \
         -trimpath \
         -ldflags "-s -w" \
         -o /build/gateways/<binary-name> \
         ./cmd/gateway/<binary-name>
   ```

4. **Add a verification step** to the runtime stage:

   ```dockerfile
   RUN test -x /usr/local/lib/gitai/gateways/<binary-name>
   ```

5. **Write tests** — add a `_test.go` file alongside `main.go` covering at
   minimum config validation and the ACP POST path (with a mock HTTP server).

---

## Security notes

- Gateway binaries are compiled from source inside the controlled builder stage.
  No pre-built artefacts are downloaded from the internet at image build time.
- Each binary runs as the `gitai` (non-root) user inside the container.
- Credentials (IMAP passwords, API keys) are injected via environment variables
  that Ruriko pushes through the secrets provisioning pipeline — they are never
  stored in the image or in Gosuto YAML files checked into version control.
- The `autoRestart: true` setting causes Gitai's supervisor to restart a gateway
  process if it exits unexpectedly, providing the same resilience guarantee as
  MCP server processes.

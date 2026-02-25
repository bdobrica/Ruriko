# Ruriko Operations Guide

> Step-by-step guide for spinning up and testing the Ruriko stack locally.

**Last updated**: 2026-02-20

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Quick Start](#quick-start)
3. [Step-by-Step Setup](#step-by-step-setup)
   - [1. Build Docker Images](#1-build-docker-images)
   - [2. Configure the Environment](#2-configure-the-environment)
   - [3. Start Tuwunel](#3-start-tuwunel-matrix-homeserver)
   - [4. Create Matrix Accounts](#4-create-matrix-accounts)
   - [5. Create the Admin Room](#5-create-the-admin-room)
   - [6. Lock Down Registration](#6-lock-down-registration)
   - [7. Start Ruriko](#7-start-ruriko)
4. [Matrix Client Recommendations](#matrix-client-recommendations)
5. [Available Flows to Test](#available-flows-to-test)
   - [Basic Commands](#flow-1-basic-commands)
   - [Secret Management](#flow-2-secret-management)
   - [Agent Inventory](#flow-3-agent-inventory)
   - [Gosuto Configuration](#flow-4-gosuto-configuration)
   - [Approval Workflow](#flow-5-approval-workflow)
   - [Natural Language Agent Creation](#flow-6-natural-language-agent-creation)
   - [Full Agent Provisioning](#flow-7-full-agent-provisioning-docker-enabled)
   - [Audit & Tracing](#flow-8-audit--tracing)
6. [Environment Variable Reference](#environment-variable-reference)
7. [Hardened Mode Quick-Start](#hardened-mode-quick-start)
8. [Troubleshooting](#troubleshooting)

---

## Prerequisites

- **Docker** and **Docker Compose** (v2+)
- **Go 1.25+** (only for building from source)
- **curl** (for account registration)
- **openssl** (for generating keys and tokens)
- A Matrix client (see [recommendations](#matrix-client-recommendations))

---

## Quick Start

If you want the shortest path to a running system:

```bash
# From the repository root:
make docker-build           # Build ruriko:latest and gitai:latest images
cp .env.example examples/docker-compose/.env
cd examples/docker-compose
$EDITOR .env                # Fill in values (see below)
docker compose up -d        # Start the stack
```

Then follow [Step 4: Create Matrix Accounts](#4-create-matrix-accounts) to register users.

---

## Step-by-Step Setup

### 1. Build Docker Images

From the repository root:

```bash
make docker-build
```

This builds two images:
- `ruriko:latest` — the control plane
- `gitai:latest` — the agent runtime (used when provisioning agents)

Alternatively, build just Ruriko:

```bash
make docker-build-ruriko
```

### 2. Configure the Environment

```bash
cp .env.example examples/docker-compose/.env
cd examples/docker-compose
```

Edit `.env` and generate the required secrets:

```bash
# Generate the master encryption key (required)
echo "RURIKO_MASTER_KEY=$(openssl rand -hex 32)" >> .env

# Generate a temporary registration token for initial account setup
REG_TOKEN=$(openssl rand -hex 16)
echo "TUWUNEL_REGISTRATION_TOKEN=$REG_TOKEN" >> .env
```

Set the registration flag to allow account creation:

```bash
# In .env, set:
TUWUNEL_ALLOW_REGISTRATION=true
```

The defaults in `.env.example` are pre-configured for local development with Tuwunel. The key values you need:

| Variable | Default | Notes |
|----------|---------|-------|
| `MATRIX_HOMESERVER` | `http://tuwunel:8008` | Internal Docker DNS name — do not change |
| `MATRIX_USER_ID` | `@ruriko:localhost` | Ruriko's bot account MXID |
| `MATRIX_ACCESS_TOKEN` | — | Obtained in Step 4 |
| `MATRIX_ADMIN_ROOMS` | — | Obtained in Step 5 |
| `RURIKO_MASTER_KEY` | — | `openssl rand -hex 32` |
| `MATRIX_ADMIN_SENDERS` | `@bogdan:localhost` | Your Matrix user ID |
| `TUWUNEL_SERVER_NAME` | `localhost` | Must match the domain in all MXIDs |

### 3. Start Tuwunel (Matrix homeserver)

Start only the homeserver first:

```bash
docker compose up -d tuwunel
```

Wait for it to become healthy:

```bash
docker compose ps
# Should show tuwunel as "healthy"
```

Verify it's reachable:

```bash
curl -s http://localhost:8008/_matrix/client/versions | head
```

### 4. Create Matrix Accounts

You need two accounts:
- **Your account** (e.g., `bogdan`) — the human operator
- **Ruriko's account** (e.g., `ruriko`) — the bot

Read the registration token from your `.env`:

```bash
source .env
echo "Registration token: $TUWUNEL_REGISTRATION_TOKEN"
```

#### Register the Ruriko bot account

```bash
RURIKO_PASSWORD=$(openssl rand -hex 16)
echo "Ruriko password: $RURIKO_PASSWORD"

curl -s -X POST "http://localhost:8008/_matrix/client/v3/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\": \"ruriko\",
    \"password\": \"$RURIKO_PASSWORD\",
    \"auth\": {
      \"type\": \"m.login.registration_token\",
      \"token\": \"$TUWUNEL_REGISTRATION_TOKEN\"
    }
  }"
```

The response will include an `access_token`. **Copy it** and set it in `.env`:

```bash
# In .env, set MATRIX_ACCESS_TOKEN to the value from the response:
MATRIX_ACCESS_TOKEN=syt_cnVyaWtv_...
```

#### Register your human account

```bash
YOUR_PASSWORD="choose-a-strong-password"

curl -s -X POST "http://localhost:8008/_matrix/client/v3/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\": \"bogdan\",
    \"password\": \"$YOUR_PASSWORD\",
    \"auth\": {
      \"type\": \"m.login.registration_token\",
      \"token\": \"$TUWUNEL_REGISTRATION_TOKEN\"
    }
  }"
```

> **Note**: If the registration endpoint returns an error about `session` / UIA flows,
> you may need a two-step registration. First call without `auth` to get the session ID,
> then call again with the `session` field included. See [Troubleshooting](#registration-returns-uia-error).

### 5. Create the Admin Room

Log in to your Matrix client using your human account (see [Client Recommendations](#matrix-client-recommendations)) with homeserver URL `http://localhost:8008`.

1. **Create a new room** (private, not encrypted for simplicity during testing).
2. **Invite `@ruriko:localhost`** to the room.
3. **Copy the room ID** (it looks like `!abc123:localhost`).
   - In Element: Room Settings → Advanced → Internal Room ID.
4. Set `MATRIX_ADMIN_ROOMS` in `.env`:

```bash
MATRIX_ADMIN_ROOMS=!abc123:localhost
```

### 6. Lock Down Registration

After all accounts are created, disable registration:

```bash
# In .env, set:
TUWUNEL_ALLOW_REGISTRATION=false
TUWUNEL_REGISTRATION_TOKEN=
```

Restart Tuwunel to apply:

```bash
docker compose restart tuwunel
```

### 7. Start Ruriko

```bash
docker compose up -d
```

Check logs:

```bash
docker compose logs -f ruriko
```

You should see:
```
✅ Ruriko control plane started. Type /ruriko help for commands.
```

Verify health:

```bash
curl -s http://localhost:8080/health | jq .
```

Now go to your Matrix client and type in the admin room:

```
/ruriko ping
```

Ruriko should reply with a pong and trace ID.

---

## Matrix Client Recommendations

For connecting to your local Tuwunel homeserver, use one of these clients:

### Element Web (recommended for testing)

The most feature-complete Matrix client. Best for testing because it shows room IDs, supports formatted messages, and has a familiar UI.

- **URL**: https://app.element.io
- **Homeserver URL**: `http://localhost:8008`
- When logging in, click "Edit" next to the homeserver field and enter `http://localhost:8008`
- You can also self-host Element Web in a container if you prefer

### Element Desktop

Same as Element Web but as a native app:
- Download from https://element.io/download
- Set custom homeserver to `http://localhost:8008`

### Cinny (lightweight alternative)

A clean, modern Matrix client with a Discord-like UI:
- **URL**: https://cinny.in or https://app.cinny.in
- Set homeserver to `http://localhost:8008`
- Lighter than Element, good for focused testing

### gomuks (CLI — for terminal lovers)

A terminal-based Matrix client written in Go:
- Install: `go install maunium.net/go/gomuks@latest`
- Set homeserver to `http://localhost:8008` on first login
- Great for quick command testing without leaving the terminal

### nheko (native desktop)

A lightweight C++/Qt Matrix client:
- Available in most Linux package managers
- Supports custom homeservers

> **Recommendation**: Start with **Element Web** at https://app.element.io — it requires no
> installation and you can point it at `http://localhost:8008`. For CLI workflows, **gomuks**
> is excellent.

---

## Available Flows to Test

Below are the flows you can exercise right now, ordered from simplest to most complex.

### Flow 1: Basic Commands

These work immediately after Ruriko connects.

```
/ruriko help              → Shows all available commands
/ruriko version           → Shows version, git commit, build time
/ruriko ping              → Health check — returns pong + trace ID
```

### Flow 2: Secret Management

Store, list, and manage encrypted secrets.

**With Kuze** (one-time link mode — requires `KUZE_BASE_URL`):

```
/ruriko secrets set openai-key --type api_key
```

Ruriko replies with a one-time URL like `http://localhost:8080/s/<token>`. Open it in a browser, paste the secret, and submit. Ruriko confirms in chat once the secret is stored.

**Other secret operations**:

```
/ruriko secrets bind <agent-name> <secret-name>        → Grant agent access to a secret
/ruriko secrets unbind <agent-name> <secret-name>      → Revoke access
/ruriko secrets push <agent-name>                      → Push secrets to a running agent
/ruriko secrets rotate mykey                            → Requires approval, returns one-time Kuze link
/ruriko secrets delete mykey                           → Requires approval
```

### Flow 3: Agent Inventory

Manage agent records in the database. These work even without Docker enabled.

```
/ruriko agents list                                    → Empty list initially
/ruriko agents create --name test-agent --template saito-agent --image gitai:latest
/ruriko agents list                                    → Shows test-agent
/ruriko agents show test-agent                         → Full agent details
/ruriko agents status test-agent                       → Runtime status (container + ACP)
```

> **Note**: Without `DOCKER_ENABLE=true`, agents are created as database records only —
> no container is spawned. With Docker enabled, the full provisioning pipeline runs
> (container start → ACP health → Gosuto push → secrets push).

### Flow 4: Gosuto Configuration

View and manage agent configurations.

```
/ruriko gosuto show test-agent                         → Current config (if set)
/ruriko gosuto versions test-agent                     → Version history
/ruriko gosuto set test-agent --content $(base64 < templates/saito-agent/gosuto.yaml)
                                                       → Store a new version (requires approval)
/ruriko gosuto diff test-agent --from 1 --to 2         → Diff between versions
/ruriko gosuto rollback test-agent --to 1              → Revert (requires approval)
/ruriko gosuto push test-agent                         → Push config to running agent via ACP
```

### Flow 5: Approval Workflow

Six operations require approval before they execute:
- `agents.delete`, `agents.disable`
- `secrets.delete`, `secrets.rotate`
- `gosuto.set`, `gosuto.rollback`

**Testing the approval flow**:

1. Send a gated command:
   ```
   /ruriko agents delete test-agent
   ```
   Ruriko responds with a pending approval ID (e.g., `abc123`).

2. Approve it (from a **different user**, or temporarily remove `MATRIX_ADMIN_SENDERS` to allow self-approval in dev):
   ```
   approve abc123
   ```
   Or deny:
   ```
   deny abc123 reason="not ready yet"
   ```

3. List/inspect approvals:
   ```
   /ruriko approvals list
   /ruriko approvals list --status pending
   /ruriko approvals show abc123
   ```

> **Dev tip**: Self-approval is blocked in production. For single-user testing, you'll need
> to either clear `MATRIX_ADMIN_SENDERS` (allows any room member) or create a second Matrix
> account and invite it to the admin room.

### Flow 6: Natural Language Agent Creation

Ruriko understands free-form requests to create agents (no `/ruriko` prefix needed).

**Example messages to try**:

```
set up Saito
create a news agent called kumo2
I need a browser agent
can you deploy a cron scheduler named daily-check
please create a finance agent
spin up a research agent called deepdive
```

Ruriko responds with a confirmation prompt showing the template, image, and any missing secrets. Reply with:

```
yes     → Proceed with creation
no      → Cancel
```

If required secrets are missing, Ruriko lists the `/ruriko secrets set` commands you need to run first.

**Supported templates**:

| Keywords | Template | Required Secrets |
|----------|----------|-----------------|
| saito, cron, scheduler, trigger, periodic | `saito-agent` | `<agent>.openai-api-key` |
| kumo, news, search, brave | `kumo-agent` | `<agent>.openai-api-key`, `<agent>.brave-api-key` |
| browser, playwright | `browser-agent` | `<agent>.anthropic-api-key` |
| kairo, finance, portfolio, market | `kairo-agent` | — (template WIP) |
| research | `research-agent` | — (template WIP) |

### Flow 7: Full Agent Provisioning (Docker enabled)

This is the most complex flow and requires `DOCKER_ENABLE=true` in `.env`.

**Prerequisites**:
- Docker socket mounted (`/var/run/docker.sock`)
- `gitai:latest` image built (`make docker-build-gitai`)
- Registration token set (so Ruriko can provision Matrix accounts for agents)
- Agent secrets stored

**Full provisioning sequence**:

1. Store the required secrets:
   ```
   /ruriko secrets set saito.openai-api-key --type api_key
   ```
   (Use Kuze link or inline depending on your config.)

2. Create the agent via command:
   ```
   /ruriko agents create --name saito --template saito-agent --image gitai:latest
   ```
   Or via natural language:
   ```
   set up Saito
   ```

3. Ruriko will:
   - Create a DB record
   - Register a Matrix account (`@saito:localhost`)
   - Spawn a Docker container
   - Wait for ACP health check
   - Push Gosuto configuration
   - Push secrets via Kuze tokens
   - Post breadcrumb updates to the admin room at each step

4. Verify the agent is running:
   ```
   /ruriko agents status saito
   /ruriko agents list
   ```

**Lifecycle operations**:
```
/ruriko agents stop saito         → Stop the container
/ruriko agents start saito        → Start it again
/ruriko agents respawn saito      → Force restart
/ruriko agents cancel saito       → Cancel in-flight task
/ruriko agents disable saito      → Full decommission (requires approval)
```

### Flow 8: Audit & Tracing

Every command Ruriko executes is logged with a trace ID.

```
/ruriko audit tail              → Last 10 audit entries
/ruriko audit tail 25           → Last 25 entries
/ruriko trace <trace_id>        → All events for a specific trace
```

Trace IDs appear in most Ruriko responses. You can use them to correlate actions across the audit log.

---

## Environment Variable Reference

### Required

| Variable | Description |
|----------|-------------|
| `MATRIX_HOMESERVER` | Matrix homeserver URL (use `http://tuwunel:8008` for local stack) |
| `MATRIX_USER_ID` | Ruriko's Matrix ID (e.g., `@ruriko:localhost`) |
| `MATRIX_ACCESS_TOKEN` | Access token from account registration |
| `MATRIX_ADMIN_ROOMS` | Comma-separated room IDs for admin commands |
| `RURIKO_MASTER_KEY` | 32-byte hex key for secret encryption (`openssl rand -hex 32`) |

### Recommended

| Variable | Default | Description |
|----------|---------|-------------|
| `MATRIX_ADMIN_SENDERS` | *(empty — all members)* | Comma-separated MXIDs allowed to run commands |
| `DOCKER_ENABLE` | `false` | Enable Docker container lifecycle management |
| `DOCKER_NETWORK` | `ruriko-net` | Docker network for agent containers |
| `KUZE_BASE_URL` | *(empty — disabled)* | Base URL for one-time secret links (e.g., `http://localhost:8080`) |
| `DEFAULT_AGENT_IMAGE` | `gitai:latest` | Container image for new agents |
| `MATRIX_PROVISIONING_ENABLE` | `false` | Auto-create Matrix accounts for agents |

### Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `KUZE_TTL` | `10m` | Lifetime of one-time secret links |
| `MATRIX_HOMESERVER_TYPE` | `tuwunel` | Homeserver type for provisioning |
| `TUWUNEL_REGISTRATION_TOKEN` | *(empty)* | Registration token (set during setup only) |
| `MATRIX_AUDIT_ROOM` | *(empty)* | Room ID for audit event summaries |
| `RECONCILE_INTERVAL` | `30s` | How often to reconcile container state |
| `HTTP_ADDR` | `:8080` | Health/Kuze HTTP server address |
| `LOG_LEVEL` | `info` | Logging level: debug, info, warn, error |
| `LOG_FORMAT` | `text` | Log format: text or json |
| `TEMPLATES_DIR` | `./templates` | Path to Gosuto template directory |

### Docker Compose Only

| Variable | Default | Description |
|----------|---------|-------------|
| `TUWUNEL_SERVER_NAME` | `localhost` | Domain part of all MXIDs |
| `TUWUNEL_HOST` | `127.0.0.1` | Host bind address for Tuwunel |
| `TUWUNEL_PORT` | `8008` | Host port for Tuwunel |
| `TUWUNEL_ALLOW_REGISTRATION` | `false` | Enable account registration (set `true` only during setup) |
| `HTTP_ADDR_HOST` | `127.0.0.1` | Host bind for Ruriko's HTTP endpoint |
| `HTTP_ADDR_PORT` | `8080` | Host port for Ruriko's HTTP endpoint |

---

## Hardened Mode Quick-Start

Use this when you want `DOCKER_ENABLE=true` but with stricter host-hardening defaults.

1. Start from the commented hardened profile in [examples/docker-compose/docker-compose.yaml](examples/docker-compose/docker-compose.yaml).
  - Enable `security_opt: no-new-privileges:true`
  - Enable `read_only`, `tmpfs`, and resource limits (`cpus`, `mem_limit`, `pids_limit`)
2. Configure Docker runtime env vars in `.env` using [.env.example](.env.example) as reference.
  - Set `DOCKER_ENABLE=true`
  - Prefer `DOCKER_HOST=tcp://docker-socket-proxy:2375`
  - If you mount `/var/run/docker.sock` directly, set `DOCKER_GID` appropriately
3. Apply the full hardening checklist in [docs/ops/deployment-docker.md](docs/ops/deployment-docker.md).
  - Use a socket proxy instead of direct socket mount when possible
  - Keep socket/socket-proxy internal-only (no public exposure)
  - Keep Ruriko non-root and avoid `--privileged`

---

## Troubleshooting

### Ruriko doesn't respond to commands

1. Check logs: `docker compose logs ruriko`
2. Verify Ruriko joined the admin room (look for "joined room" in logs)
3. Verify `MATRIX_ADMIN_ROOMS` contains the correct room ID
4. If `MATRIX_ADMIN_SENDERS` is set, verify your MXID is included
5. Make sure you're sending `/ruriko help` (with the slash)

### Registration returns UIA error

Tuwunel (conduwuit) may require a two-step User-Interactive Authentication (UIA) flow:

```bash
# Step 1: Get the session ID
SESSION=$(curl -s -X POST "http://localhost:8008/_matrix/client/v3/register" \
  -H "Content-Type: application/json" \
  -d '{"username":"ruriko","password":"your-password"}' | jq -r '.session')

echo "Session: $SESSION"

# Step 2: Register with the session and token
curl -s -X POST "http://localhost:8008/_matrix/client/v3/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\": \"ruriko\",
    \"password\": \"your-password\",
    \"auth\": {
      \"type\": \"m.login.registration_token\",
      \"token\": \"$TUWUNEL_REGISTRATION_TOKEN\",
      \"session\": \"$SESSION\"
    }
  }"
```

### Tuwunel health check fails

```bash
# Check if Tuwunel is running
docker compose logs tuwunel

# Test connectivity manually
curl -v http://localhost:8008/_matrix/client/versions
```

If Tuwunel is not responding, it may still be starting up. The health check has a 15-second start period — wait and retry.

### Kuze links don't work

1. Verify `KUZE_BASE_URL` is set in `.env` (e.g., `http://localhost:8080`)
2. Verify `HTTP_ADDR` is set (e.g., `:8080`)
3. Both must be set for Kuze to activate. Check logs for: `Kuze secret-entry server ready`
4. If you see `Kuze routes registered on HTTP server`, the routes are active

### Docker agent provisioning fails

1. Verify `DOCKER_ENABLE=true` in `.env`
2. Verify the Docker socket is mounted: check `docker compose logs ruriko` for Docker errors
3. Verify the `gitai:latest` image exists: `docker images | grep gitai`
4. Verify the `ruriko-net` Docker network exists: `docker network ls | grep ruriko`

### How to reset everything

```bash
cd examples/docker-compose
docker compose down -v    # Stop all services and delete volumes
```

This destroys all state (database, homeserver data). Start fresh from [Step 3](#3-start-tuwunel-matrix-homeserver).

---

## Makefile Targets

Useful targets for development and operations:

| Target | Description |
|--------|-------------|
| `make build` | Build all binaries locally |
| `make test` | Run all tests with race detector |
| `make docker-build` | Build `ruriko:latest` and `gitai:latest` images |
| `make compose-up` | Build images and start the full stack |
| `make compose-down` | Stop the stack |
| `make compose-logs` | Tail logs from all services |
| `make compose-ps` | Show service status |
| `make clean` | Remove build artifacts and databases |

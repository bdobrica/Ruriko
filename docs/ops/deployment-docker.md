# Deploying Ruriko with Docker

This guide walks through deploying Ruriko in a Docker or Docker Compose environment.

---

## Prerequisites

- Docker Engine 24+ (or Docker Desktop)
- `docker compose` plugin (v2)
- A Matrix homeserver (self-hosted Synapse/Dendrite, or a public homeserver)
- A Matrix bot account for Ruriko

---

## Architecture overview

```
┌─────────────────────────────────────────────────┐
│                Docker host                       │
│                                                  │
│  ┌────────────┐          ┌──────────────────┐   │
│  │   ruriko   │◄────────►│  Matrix (Synapse) │  │
│  │  :8080     │          │  :8008 / :8448    │  │
│  └────────────┘          └──────────────────┘   │
│       │                                          │
│       │ /var/run/docker.sock (optional)          │
│       │                                          │
│  ┌────▼───────────────────────────────────────┐ │
│  │  Managed agent containers (Gitai)           │ │
│  └────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────┘
```

When `DOCKER_ENABLE=true`, Ruriko manages agent containers on the same host via the Docker socket.

---

## Build the image

From the repository root:

```bash
# Inject version metadata
export GIT_COMMIT=$(git rev-parse --short HEAD)
export GIT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)
export BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)

docker build \
  --file deploy/docker/Dockerfile.ruriko \
  --build-arg GIT_COMMIT="${GIT_COMMIT}" \
  --build-arg GIT_TAG="${GIT_TAG}" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  --tag ruriko:latest \
  .
```

Or via the Makefile:

```bash
make docker-build-ruriko
```

---

## Configuration reference

All configuration is supplied through environment variables.  
See [`.env.example`](../../.env.example) for a complete annotated reference.

### Required variables

| Variable | Description |
|---|---|
| `MATRIX_HOMESERVER` | Full URL of the Matrix homeserver, e.g. `https://matrix.example.com` |
| `MATRIX_USER_ID` | MXID of the Ruriko bot, e.g. `@ruriko:example.com` |
| `MATRIX_ACCESS_TOKEN` | Access token for the bot account |
| `MATRIX_ADMIN_ROOMS` | Comma-separated list of admin room IDs |
| `RURIKO_MASTER_KEY` | 32-byte hex master encryption key |

Generate a master key with:

```bash
openssl rand -hex 32
```

### Optional variables

| Variable | Default | Description |
|---|---|---|
| `MATRIX_ADMIN_SENDERS` | _(any member)_ | Comma-separated allowed command senders |
| `MATRIX_AUDIT_ROOM` | _(disabled)_ | Room ID for audit event notifications |
| `MATRIX_PROVISIONING_ENABLE` | `false` | Enable automatic Matrix account creation |
| `MATRIX_HOMESERVER_TYPE` | `synapse` | Homeserver type (`synapse` or `generic`) |
| `MATRIX_SHARED_SECRET` | | Synapse shared registration secret |
| `DOCKER_ENABLE` | `false` | Enable Docker runtime adapter |
| `DOCKER_NETWORK` | | Docker network for spawned agent containers |
| `RECONCILE_INTERVAL` | `30s` | Reconciliation loop interval |
| `DATABASE_PATH` | `/data/ruriko.db` | SQLite database path |
| `TEMPLATES_DIR` | `/templates` | Agent Gosuto templates directory |
| `HTTP_ADDR` | _(disabled)_ | Health endpoint address (e.g. `:8080`) |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `LOG_FORMAT` | `text` | `text` or `json` |

---

## Standalone container (without Compose)

```bash
docker run -d \
  --name ruriko \
  --restart unless-stopped \
  -e MATRIX_HOMESERVER=https://matrix.example.com \
  -e MATRIX_USER_ID=@ruriko:example.com \
  -e MATRIX_ACCESS_TOKEN=<token> \
  -e MATRIX_ADMIN_ROOMS='!abc:example.com' \
  -e RURIKO_MASTER_KEY=$(openssl rand -hex 32) \
  -e HTTP_ADDR=:8080 \
  -e LOG_FORMAT=json \
  -p 127.0.0.1:8080:8080 \
  -v ruriko-data:/data \
  -v ./templates:/templates:ro \
  ruriko:latest
```

---

## Docker Compose

A complete Compose stack (including an optional local Synapse) lives in  
[`examples/docker-compose/`](../../examples/docker-compose/). See its  
[README](../../examples/docker-compose/README.md) for step-by-step instructions.

---

## Matrix homeserver setup

### Using an existing homeserver

1. Register a dedicated Matrix account for Ruriko (or use an existing one).
2. Note the access token (visible in Element → Settings → Help & About → Access Token).
3. Create an admin room and invite the Ruriko account.
4. Set the variables in your environment or `.env`.

### Using a local Synapse (Docker Compose)

```bash
cd examples/docker-compose

# Generate Synapse config
docker compose run --rm synapse generate

# Edit synapse-data/homeserver.yaml as needed, then start
docker compose up -d synapse

# Register the Ruriko bot account (-a for homeserver admin)
docker compose exec synapse register_new_matrix_user \
  -c /data/homeserver.yaml \
  -u ruriko -p <password> --no-admin

# Retrieve the access token via the Admin API
curl -X POST \
  'http://localhost:8008/_matrix/client/v3/login' \
  -H 'Content-Type: application/json' \
  -d '{"type":"m.login.password","user":"ruriko","password":"<password>"}'
```

Set `MATRIX_HOMESERVER=http://synapse:8008` (service name) when Ruriko  
runs in the same Compose network.

---

## Admin room commands

Once running, send commands to the admin room in Matrix:

```
/ruriko version
/ruriko help
/ruriko agents list
/ruriko secrets list
```

---

## Enabling Docker agent management

To allow Ruriko to spawn/stop agent containers:

1. Set `DOCKER_ENABLE=true`.
2. Mount the Docker socket into the Ruriko container:
   ```yaml
   volumes:
     - /var/run/docker.sock:/var/run/docker.sock:ro
   ```
3. Ensure the Docker network referenced by `DOCKER_NETWORK` exists.

**Security note**: Mounting the Docker socket grants root-equivalent access to the host.  
In production, use a Docker socket proxy (e.g. Tecnativa docker-socket-proxy) to restrict  
which API endpoints Ruriko can reach.

---

## Health check

```bash
curl http://localhost:8080/health
curl http://localhost:8080/status
```

---

## Upgrading Ruriko

1. Pull or build the new image:
   ```bash
   make docker-build-ruriko
   # or
   docker pull ruriko:latest
   ```
2. Restart the container:
   ```bash
   docker compose up -d --no-deps ruriko
   # or
   docker restart ruriko
   ```

Ruriko runs database migrations automatically on startup — no manual migration step is required.

---

## Troubleshooting

### Container exits immediately

Check logs:
```bash
docker logs ruriko
```
Common causes:
- A required environment variable is missing (the entrypoint will print an `ERROR:` line).
- `RURIKO_MASTER_KEY` is invalid (must be 64 hex characters).
- Matrix homeserver is unreachable.

### "Cannot connect to Matrix"

- Verify `MATRIX_HOMESERVER` is reachable from inside the container:
  ```bash
  docker exec ruriko wget -qO- "${MATRIX_HOMESERVER}/_matrix/client/versions"
  ```
- Check `MATRIX_ACCESS_TOKEN` is valid and not expired.

### Commands not being processed

- Confirm the bot account is **invited to and has joined** the admin room.
- Verify `MATRIX_ADMIN_ROOMS` contains the correct room ID (starts with `!`).
- If `MATRIX_ADMIN_SENDERS` is set, ensure your MXID is in the list.

# Ruriko Docker Compose Example

This directory contains a ready-to-use Docker Compose stack that starts:

- **ruriko** – The Ruriko control plane
- **synapse** – A local Matrix homeserver_(optional; comment it out if you already have one)_

---

## Quick Start

### 1. Build the Ruriko image

From the repository root:

```bash
make docker-build-ruriko
```

### 2. Configure the environment

```bash
cp ../../.env.example .env
$EDITOR .env
```

Fill in at minimum:

| Variable | Description |
|---|---|
| `MATRIX_HOMESERVER` | Full URL of your Matrix homeserver |
| `MATRIX_USER_ID` | MXID of the Ruriko bot account |
| `MATRIX_ACCESS_TOKEN` | Access token for the bot account |
| `MATRIX_ADMIN_ROOMS` | Comma-separated admin room IDs |
| `RURIKO_MASTER_KEY` | 32-byte hex key (`openssl rand -hex 32`) |

### 3. (Optional) Bootstrap Synapse

If you want to use the bundled Synapse service instead of an existing homeserver:

```bash
# Generate initial Synapse config
docker compose run --rm synapse generate

# Start Synapse
docker compose up -d synapse

# Register the Ruriko bot account
docker compose exec synapse register_new_matrix_user \
    -c /data/homeserver.yaml \
    -u ruriko -p <password> --no-admin
```

Retrieve the access token through the Synapse admin API or Element web, then set it in `.env`.

### 4. Start the stack

```bash
docker compose up -d
```

### 5. Check logs

```bash
docker compose logs -f ruriko
```

### 6. Verify health

```bash
curl http://127.0.0.1:8080/health
```

---

## Connecting Ruriko to Matrix

1. Create (or use an existing) Matrix room as the **admin room**.
2. Invite the Ruriko bot account to that room.
3. Set the room ID in `MATRIX_ADMIN_ROOMS`.
4. Restart Ruriko.

Then test from your Matrix client:

```
/ruriko version
/ruriko agents list
```

---

## Volumes

| Mount | Purpose |
|---|---|
| `ruriko-data` | SQLite database and persistent state |
| `synapse-data` | Synapse homeserver data |
| `../../templates` | Agent Gosuto templates (read-only) |
| `/var/run/docker.sock` | Docker socket (only needed when `DOCKER_ENABLE=true`) |

---

## Stopping

```bash
docker compose down
```

To also remove volumes (⚠ destroys all data):

```bash
docker compose down -v
```

---

## Further reading

- [Deployment guide](../../docs/ops/deployment-docker.md)
- [Backup and restore](../../docs/ops/backup-restore.md)
- [Environment variable reference](../../.env.example)

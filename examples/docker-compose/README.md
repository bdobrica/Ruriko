# Ruriko Docker Compose Example

This directory contains a ready-to-use Docker Compose stack that starts:

- **ruriko** – The Ruriko control plane
- **tuwunel** – A local Matrix homeserver _(optional; comment it out if you already have one)_

[Tuwunel](https://tuwunel.chat) is a lightweight, self-hostable Matrix homeserver
written in Rust. Federation and registration are disabled by default for security.

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

### 3. Bootstrap Tuwunel and create accounts

Tuwunel uses a registration token flow so registration can be unlocked
temporarily without opening the homeserver to the public.

```bash
# Generate a one-time registration token
TUWUNEL_REGISTRATION_TOKEN=$(openssl rand -hex 16)
echo "Token: $TUWUNEL_REGISTRATION_TOKEN"

# Start Tuwunel with registration temporarily enabled
TUWUNEL_ALLOW_REGISTRATION=true \
TUWUNEL_REGISTRATION_TOKEN=$TUWUNEL_REGISTRATION_TOKEN \
docker compose up -d tuwunel

# Register the Ruriko bot account via the Matrix client API
curl -s -X POST "http://localhost:8008/_matrix/client/v3/register" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"ruriko\",\"password\":\"$(openssl rand -hex 16)\",\"auth\":{\"type\":\"m.login.registration_token\",\"token\":\"$TUWUNEL_REGISTRATION_TOKEN\"}}"
# Save the access_token from the response → MATRIX_ACCESS_TOKEN in .env
```

After creating all required accounts, lock down registration:

```bash
docker compose stop tuwunel
# In .env: clear TUWUNEL_REGISTRATION_TOKEN, leave TUWUNEL_ALLOW_REGISTRATION=false
docker compose up -d tuwunel
```

See [docs/ops/deployment-docker.md](../../docs/ops/deployment-docker.md) for
detailed account setup and room creation instructions.

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

---

## Using Synapse instead of Tuwunel

Synapse is supported as an alternative homeserver. To switch:

1. Comment out the `tuwunel` service in `docker-compose.yaml` and uncomment `synapse`.
2. Remove `tuwunel-data` from the volumes section and uncomment `synapse-data`.
3. Change `depends_on` in the `ruriko` service to reference `synapse`.
4. Set `MATRIX_HOMESERVER_TYPE=synapse` and `MATRIX_SHARED_SECRET` in `.env`.
5. Run `docker compose run --rm synapse generate` to initialise the Synapse config.

---

## Volumes

| Mount | Purpose |
|---|---|
| `ruriko-data` | SQLite database and persistent state |
| `tuwunel-data` | Tuwunel homeserver data |
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

# Backup and Restore — Ruriko

Ruriko uses SQLite as its primary state store.  All agent inventory, secrets
(encrypted at rest), Gosuto version history, approval records, and audit log
entries live in a single file: `ruriko.db`.

---

## What to back up

| Item | Location | Notes |
|---|---|---|
| **SQLite database** | `DATABASE_PATH` (default: `/data/ruriko.db`) | Contains all Ruriko state |
| **Master encryption key** | `RURIKO_MASTER_KEY` env var | Required to decrypt secrets; store securely |
| **Gosuto templates** | `TEMPLATES_DIR` (default: `/templates`) | Needed to recreate agents from templates |

---

## Database backup

SQLite supports online backup via its built-in `.backup` command and the
[SQLite Online Backup API](https://www.sqlite.org/backup.html).  The safest
method for a running process is to use `sqlite3`'s `.backup` or `VACUUM INTO`
because they produce a consistent snapshot without stopping Ruriko.

### One-shot backup

```bash
# Using the sqlite3 CLI
sqlite3 /data/ruriko.db ".backup '/backup/ruriko-$(date +%Y%m%d-%H%M%S).db'"

# Alternatively, using VACUUM INTO (SQLite 3.27+)
sqlite3 /data/ruriko.db "VACUUM INTO '/backup/ruriko-$(date +%Y%m%d-%H%M%S).db'"
```

### Backup inside Docker

```bash
docker exec ruriko \
  sqlite3 /data/ruriko.db \
  ".backup '/data/ruriko-backup-$(date +%Y%m%d-%H%M%S).db'"

# Then copy out of the container
docker cp ruriko:/data/ruriko-backup-<timestamp>.db ./backups/
```

### Scheduled backup with cron

Add to the host's crontab:

```cron
# Back up Ruriko database every 6 hours, keep 30 days of backups
0 */6 * * * docker exec ruriko sqlite3 /data/ruriko.db \
  ".backup '/data/ruriko-$(date +\%Y\%m\%d-\%H\%M\%S).db'" && \
  find /path/to/backups -name "ruriko-*.db" -mtime +30 -delete
```

---

## Backing up the master key

The `RURIKO_MASTER_KEY` is a 32-byte hex string that encrypts all secrets stored
in the database.  **Without it, the database backup is useless for secrets recovery.**

Store it separately from the database backup, for example:

- A secrets manager (HashiCorp Vault, AWS Secrets Manager, Bitwarden)
- An encrypted password manager
- Printed and stored in a physically secure location

**Never store the master key in the same location as the database backup.**

---

## Restore procedure

### 1. Stop Ruriko

```bash
# Docker Compose
docker compose stop ruriko

# Standalone Docker
docker stop ruriko
```

### 2. Replace the database

```bash
# Docker volume
docker run --rm \
  -v ruriko-data:/data \
  -v ./backups:/backup \
  alpine cp /backup/ruriko-<timestamp>.db /data/ruriko.db

# Standalone file path
cp /path/to/backup/ruriko-<timestamp>.db /data/ruriko.db
```

### 3. Set the matching master key

Ensure `RURIKO_MASTER_KEY` in your environment (or `.env`) matches the key that
was in use when the backup was created.  If you are restoring to a different
host, update the environment variable before starting.

### 4. Start Ruriko

```bash
docker compose start ruriko
# or
docker start ruriko
```

Ruriko runs schema migrations automatically on startup.  If the backup came from
an older version, migrations will be applied automatically.

### 5. Verify

```bash
curl http://localhost:8080/health
# And from Matrix:
# /ruriko agents list
# /ruriko audit tail 5
```

---

## Disaster recovery

### Scenario: Database file corrupted

1. Stop Ruriko.
2. Identify the most recent healthy backup.
3. Follow the **Restore procedure** above.
4. Re-push secrets to agents if any have changed since the backup:
   ```
   /ruriko secrets push <agent>
   ```
5. Review the audit log for any operations that occurred after the backup
   timestamp and replay them if necessary.

### Scenario: Master key lost

If the master key is lost, encrypted secrets **cannot be recovered**.  You will
need to:

1. Restore the database from backup (agent inventory, Gosuto versions, and
   audit log are unencrypted and will be intact).
2. Re-set all secrets:
   ```
   /ruriko secrets set <name>
   ```
3. Re-bind secrets to agents:
   ```
   /ruriko secrets bind <agent> <secret>
   ```
4. Push secrets to running agents:
   ```
   /ruriko secrets push <agent>
   ```

---

## Volume backup with Docker

If Ruriko is using a named Docker volume, back up the entire volume:

```bash
# Create a compressed tar of the volume contents
docker run --rm \
  -v ruriko-data:/data:ro \
  -v $(pwd)/backups:/backup \
  alpine tar czf /backup/ruriko-data-$(date +%Y%m%d-%H%M%S).tar.gz -C /data .
```

Restore:

```bash
docker run --rm \
  -v ruriko-data:/data \
  -v $(pwd)/backups:/backup \
  alpine sh -c "cd /data && tar xzf /backup/ruriko-data-<timestamp>.tar.gz"
```

---

## Upgrading Ruriko and rollback

Ruriko applies database migrations automatically on startup.  Migrations are
**non-destructive** (they never drop tables or columns).

If you need to roll back to an older Ruriko version after an upgrade:

1. Restore the database from the pre-upgrade backup.
2. Deploy the older image or binary.
3. Ruriko will start with the restored schema version.

It is strongly recommended to create a backup **before every upgrade**.

#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${CANONICAL_COMPOSE_FILE:-$ROOT_DIR/examples/docker-compose/docker-compose.yaml}"
KEEP_STACK="${KEEP_STACK:-0}"
TIMEOUT_SECONDS="${CANONICAL_LIVE_TIMEOUT_SECONDS:-600}"
POLL_SECONDS="${CANONICAL_LIVE_POLL_SECONDS:-5}"
REQUIRED_CYCLES="${CANONICAL_REQUIRED_CYCLES:-2}"
AUTO_BOOTSTRAP_CANONICAL="${CANONICAL_AUTO_BOOTSTRAP:-1}"
BOOTSTRAP_STATUS_TIMEOUT="${CANONICAL_BOOTSTRAP_STATUS_TIMEOUT:-120}"
CANONICAL_FAST_CRON_EVERY="${CANONICAL_FAST_CRON_EVERY:-10s}"
FAILFAST_AFTER_SECONDS="${CANONICAL_FAILFAST_AFTER_SECONDS:-45}"
ENFORCE_ROOM_JOINS="${CANONICAL_ENFORCE_ROOM_JOINS:-1}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${YELLOW}[INFO]${NC} $*"; }
pass() { echo -e "${GREEN}[PASS]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; exit 1; }

docker_compose() {
	if command -v docker-compose >/dev/null 2>&1; then
		docker-compose -f "$COMPOSE_FILE" "$@"
	else
		docker compose -f "$COMPOSE_FILE" "$@"
	fi
}

cleanup() {
	if [[ "$KEEP_STACK" == "1" ]]; then
		info "KEEP_STACK=1 set; leaving stack running"
		return
	fi
	info "Stopping compose stack"
	docker_compose down -v || true
}
trap cleanup EXIT

require_tool() {
	command -v "$1" >/dev/null 2>&1 || fail "required tool '$1' not found"
}

require_tool docker
require_tool grep
require_tool python3
require_tool curl

info "Starting compose stack"
docker_compose up -d

for attempt in {1..60}; do
	if docker ps --format '{{.Names}}' | grep -qE '^ruriko$' && docker ps --format '{{.Names}}' | grep -qE '^tuwunel$'; then
		break
	fi
	sleep 1
done

for svc in ruriko tuwunel; do
	docker ps --format '{{.Names}}' | grep -q "^${svc}$" || fail "container '${svc}' not running"
done

wait_for_ruriko_db_ready() {
	local timeout_seconds="${1:-60}"
	local start now elapsed
	start="$(date +%s)"
	while true; do
		local tmp_db_dir tmp_db
		tmp_db_dir="$(mktemp -d)"
		tmp_db="${tmp_db_dir}/ruriko.db"
		if docker cp ruriko:/data/ruriko.db "$tmp_db" >/dev/null 2>&1; then
			docker cp ruriko:/data/ruriko.db-wal "${tmp_db_dir}/ruriko.db-wal" >/dev/null 2>&1 || true
			docker cp ruriko:/data/ruriko.db-shm "${tmp_db_dir}/ruriko.db-shm" >/dev/null 2>&1 || true
			if python3 - "$tmp_db" <<'PY'
import sqlite3
import sys

db = sys.argv[1]
conn = sqlite3.connect(db)
cur = conn.cursor()
cur.execute("select name from sqlite_master where type='table' and name='agents'")
ok = cur.fetchone() is not None
conn.close()
raise SystemExit(0 if ok else 1)
PY
			then
				rm -rf "$tmp_db_dir"
				pass "ruriko database ready (agents table present)"
				return 0
			fi
		fi
		rm -rf "$tmp_db_dir"
		now="$(date +%s)"
		elapsed=$((now - start))
		if (( elapsed >= timeout_seconds )); then
			fail "timed out waiting for ruriko database readiness (agents table)"
		fi
		sleep 1
	done
}

wait_for_ruriko_db_ready 90

agent_container_name() {
	local name="$1"
	echo "ruriko-agent-${name}"
}

for agent in saito kairo kumo; do
	cname="$(agent_container_name "$agent")"
	docker ps --format '{{.Names}}' | grep -q "^${cname}$" || fail "canonical agent container '${cname}' not running"
	pass "found ${cname}"
done

SAITO_CONTAINER="$(agent_container_name saito)"
KAIRO_CONTAINER="$(agent_container_name kairo)"
KUMO_CONTAINER="$(agent_container_name kumo)"

for agent in saito kairo kumo; do
	cname="$(agent_container_name "$agent")"
	if ! docker exec "$cname" sh -lc 'env | grep -q "^LLM_API_KEY="'; then
		fail "${cname} is missing LLM_API_KEY; recreate/respawn agents after setting LLM_API_KEY (or GLOBAL_LLM_API_KEY / RURIKO_NLP_API_KEY fallback) for Ruriko"
	fi
done

DB_SNAPSHOT_DIR="$(mktemp -d)"
DB_FILE="${DB_SNAPSHOT_DIR}/ruriko.db"
docker cp ruriko:/data/ruriko.db "$DB_FILE" >/dev/null
docker cp ruriko:/data/ruriko.db-wal "${DB_SNAPSHOT_DIR}/ruriko.db-wal" >/dev/null 2>&1 || true
docker cp ruriko:/data/ruriko.db-shm "${DB_SNAPSHOT_DIR}/ruriko.db-shm" >/dev/null 2>&1 || true

count_cron_events() {
	local since="$1"
	python3 - "$SAITO_CONTAINER" "$since" <<'PY'
import re
import subprocess
import sys

container = sys.argv[1]
since = sys.argv[2]
res = subprocess.run(["docker", "logs", "--since", since, container], capture_output=True, text=True)
text = (res.stdout or "") + (res.stderr or "")
rx = re.compile(r"event processed.*event_type=cron\.tick.*status=success")
print(sum(1 for line in text.splitlines() if rx.search(line)))
PY
}

count_saito_to_kairo() {
	local since="$1"
	python3 - "$SAITO_CONTAINER" "$since" <<'PY'
import re
import subprocess
import sys

container = sys.argv[1]
since = sys.argv[2]
res = subprocess.run(["docker", "logs", "--since", since, container], capture_output=True, text=True)
text = (res.stdout or "") + (res.stderr or "")
rx = re.compile(r"msg=matrix\.send_message.*agent_id=saito.*target=kairo.*status=success")
print(sum(1 for line in text.splitlines() if rx.search(line)))
PY
}

count_kairo_to_kumo() {
	local since="$1"
	python3 - "$KAIRO_CONTAINER" "$since" <<'PY'
import re
import subprocess
import sys

container = sys.argv[1]
since = sys.argv[2]
res = subprocess.run(["docker", "logs", "--since", since, container], capture_output=True, text=True)
text = (res.stdout or "") + (res.stderr or "")
rx = re.compile(r"msg=matrix\.send_message.*agent_id=kairo.*target=kumo.*status=success")
print(sum(1 for line in text.splitlines() if rx.search(line)))
PY
}

count_deliveries() {
	local since="$1"
	python3 - "$KUMO_CONTAINER" "$since" <<'PY'
import re
import subprocess
import sys

container = sys.argv[1]
since = sys.argv[2]
res = subprocess.run(["docker", "logs", "--since", since, container], capture_output=True, text=True)
text = (res.stdout or "") + (res.stderr or "")
rx = re.compile(r"msg=matrix\.send_message.*agent_id=kumo.*target=kairo.*status=success")
print(sum(1 for line in text.splitlines() if rx.search(line)))
PY
}

discover_admin_room() {
	python3 - "$ROOT_DIR/examples/docker-compose/.env" <<'PY'
import sys
from pathlib import Path

env = {}
for raw in Path(sys.argv[1]).read_text(encoding='utf-8').splitlines():
    line = raw.strip()
    if not line or line.startswith('#') or '=' not in line:
        continue
    k, v = line.split('=', 1)
    env[k.strip()] = v.strip()

rooms = [r.strip() for r in env.get('MATRIX_ADMIN_ROOMS', '').split(',') if r.strip()]
print(rooms[0] if rooms else '')
PY
}

discover_matrix_base_url() {
	python3 - "$ROOT_DIR/examples/docker-compose/.env" <<'PY'
import sys
from pathlib import Path

env = {}
for raw in Path(sys.argv[1]).read_text(encoding='utf-8').splitlines():
    line = raw.strip()
    if not line or line.startswith('#') or '=' not in line:
        continue
    k, v = line.split('=', 1)
    env[k.strip()] = v.strip()

host = env.get('TUWUNEL_HOST', '127.0.0.1').strip() or '127.0.0.1'
port = env.get('TUWUNEL_PORT', '8008').strip() or '8008'
print(f"http://{host}:{port}")
PY
}

urlencode() {
	python3 - "$1" <<'PY'
import sys
import urllib.parse

print(urllib.parse.quote(sys.argv[1], safe=''))
PY
}

ensure_canonical_room_joins() {
	local admin_room="$1"
	local matrix_base="$2"
	local encoded_room
	encoded_room="$(urlencode "$admin_room")"

	for agent in saito kairo kumo; do
		local cname token
		cname="$(agent_container_name "$agent")"
		token="$(docker exec "$cname" sh -lc 'printf %s "$MATRIX_ACCESS_TOKEN"' 2>/dev/null || true)"
		[[ -n "$token" ]] || fail "${cname} missing MATRIX_ACCESS_TOKEN; cannot enforce room join"

		if ! curl -fsS -X POST "${matrix_base}/_matrix/client/v3/rooms/${encoded_room}/join" \
			-H "Authorization: Bearer ${token}" \
			-H "Content-Type: application/json" \
			-d '{}' >/dev/null; then
			fail "failed to force ${agent} join into admin room ${admin_room}"
		fi
	done

	pass "canonical agents joined admin room ${admin_room}"
}

bootstrap_canonical_configs() {
	info "Auto-bootstrap canonical Gosuto configs for saito/kairo/kumo"

	local admin_room user_room
	admin_room="$(python3 - "$ROOT_DIR/examples/docker-compose/.env" <<'PY'
import sys
from pathlib import Path

env = {}
for raw in Path(sys.argv[1]).read_text(encoding='utf-8').splitlines():
    line = raw.strip()
    if not line or line.startswith('#') or '=' not in line:
        continue
    k, v = line.split('=', 1)
    env[k.strip()] = v.strip()

rooms = [r.strip() for r in env.get('MATRIX_ADMIN_ROOMS', '').split(',') if r.strip()]
print(rooms[0] if rooms else '')
PY
)"
	user_room="$(python3 - "$ROOT_DIR/examples/docker-compose/.env" <<'PY'
import sys
from pathlib import Path

env = {}
for raw in Path(sys.argv[1]).read_text(encoding='utf-8').splitlines():
    line = raw.strip()
    if not line or line.startswith('#') or '=' not in line:
        continue
    k, v = line.split('=', 1)
    env[k.strip()] = v.strip()

rooms = [r.strip() for r in env.get('MATRIX_ADMIN_ROOMS', '').split(',') if r.strip()]
print(rooms[1] if len(rooms) > 1 else '!canonical-user-fallback:localhost')
PY
)"

	[[ -n "$admin_room" ]] || fail "failed to discover MATRIX_ADMIN_ROOMS from compose .env"
	if [[ "$user_room" == "$admin_room" ]]; then
		user_room="!canonical-user-fallback:localhost"
	fi

	python3 "$ROOT_DIR/test/integration/canonical_workflow_bootstrap.py" \
		--root-dir "$ROOT_DIR" \
		--db-file "$DB_FILE" \
		--admin-room "$admin_room" \
		--user-room "$user_room" \
		--saito-cron-every "$CANONICAL_FAST_CRON_EVERY" \
		--status-timeout "$BOOTSTRAP_STATUS_TIMEOUT"
}

if [[ "$AUTO_BOOTSTRAP_CANONICAL" == "1" ]]; then
	bootstrap_canonical_configs
else
	info "CANONICAL_AUTO_BOOTSTRAP=0; skipping canonical config bootstrap"
fi

if [[ "$ENFORCE_ROOM_JOINS" == "1" ]]; then
	ADMIN_ROOM="$(discover_admin_room)"
	[[ -n "$ADMIN_ROOM" ]] || fail "failed to discover MATRIX_ADMIN_ROOMS for join enforcement"
	MATRIX_BASE_URL="$(discover_matrix_base_url)"
	ensure_canonical_room_joins "$ADMIN_ROOM" "$MATRIX_BASE_URL"
else
	info "CANONICAL_ENFORCE_ROOM_JOINS=0; skipping explicit room-join enforcement"
fi

START_EPOCH="$(date +%s)"
START_TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
info "Watching for canonical cycles (required=${REQUIRED_CYCLES}, timeout=${TIMEOUT_SECONDS}s, poll=${POLL_SECONDS}s)"

while true; do
	NOW_EPOCH="$(date +%s)"
	ELAPSED=$((NOW_EPOCH - START_EPOCH))
	if (( ELAPSED > TIMEOUT_SECONDS )); then
		fail "timed out after ${TIMEOUT_SECONDS}s waiting for canonical cycles"
	fi

	CRON_COUNT="$(count_cron_events "$START_TS")"
	SK_COUNT="$(count_saito_to_kairo "$START_TS")"
	KK_COUNT="$(count_kairo_to_kumo "$START_TS")"
	DELIVERY_COUNT="$(count_deliveries "$START_TS")"

	info "elapsed=${ELAPSED}s cron=${CRON_COUNT} saito→kairo=${SK_COUNT} kairo→kumo=${KK_COUNT} deliveries=${DELIVERY_COUNT}"

	# Fail-fast: once upstream is flowing, continuing to wait is usually wasted
	# time if downstream counters are still stuck at 0 (most often room-membership
	# or config-apply propagation issues). Bail out early with focused diagnostics.
	if (( ELAPSED >= FAILFAST_AFTER_SECONDS )) && (( SK_COUNT >= REQUIRED_CYCLES )) && (( KK_COUNT == 0 || DELIVERY_COUNT == 0 )); then
		info "Fail-fast triggered after ${ELAPSED}s: upstream active but downstream stalled"
		info "Recent Saito logs:"
		docker logs --since 10m "$SAITO_CONTAINER" 2>&1 | tail -n 60 || true
		info "Recent Kairo logs:"
		docker logs --since 10m "$KAIRO_CONTAINER" 2>&1 | tail -n 80 || true
		info "Recent Kumo logs:"
		docker logs --since 10m "$KUMO_CONTAINER" 2>&1 | tail -n 80 || true
		fail "downstream chain stalled (kairo→kumo=${KK_COUNT}, deliveries=${DELIVERY_COUNT}) after ${ELAPSED}s; avoid waiting full timeout"
	fi

	if (( CRON_COUNT >= REQUIRED_CYCLES )) && (( SK_COUNT >= REQUIRED_CYCLES )) && (( KK_COUNT >= REQUIRED_CYCLES )) && (( DELIVERY_COUNT >= REQUIRED_CYCLES )); then
		pass "canonical live cycle observed for required threshold"
		break
	fi

	sleep "$POLL_SECONDS"
done

rm -rf "$DB_SNAPSHOT_DIR"

pass "Canonical live compose workflow check passed"

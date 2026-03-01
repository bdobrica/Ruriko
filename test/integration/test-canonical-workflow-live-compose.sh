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
		fail "${cname} is missing LLM_API_KEY; recreate/respawn agents after setting LLM_API_KEY (or RURIKO_NLP_API_KEY fallback) for Ruriko"
	fi
done

DB_FILE="$(mktemp)"
docker cp ruriko:/data/ruriko.db "$DB_FILE" >/dev/null

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
		--status-timeout "$BOOTSTRAP_STATUS_TIMEOUT"
}

if [[ "$AUTO_BOOTSTRAP_CANONICAL" == "1" ]]; then
	bootstrap_canonical_configs
else
	info "CANONICAL_AUTO_BOOTSTRAP=0; skipping canonical config bootstrap"
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

	if (( CRON_COUNT >= REQUIRED_CYCLES )) && (( SK_COUNT >= REQUIRED_CYCLES )) && (( KK_COUNT >= REQUIRED_CYCLES )) && (( DELIVERY_COUNT >= REQUIRED_CYCLES )); then
		pass "canonical live cycle observed for required threshold"
		break
	fi

	sleep "$POLL_SECONDS"
done

rm -f "$DB_FILE"

pass "Canonical live compose workflow check passed"

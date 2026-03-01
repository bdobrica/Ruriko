#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${CANONICAL_COMPOSE_FILE:-$ROOT_DIR/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV_FILE="$ROOT_DIR/examples/docker-compose/.env"
HELPER_PY="$ROOT_DIR/test/integration/canonical_live_helpers.py"
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
[[ -f "$HELPER_PY" ]] || fail "required helper script not found: $HELPER_PY"

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
			if python3 "$HELPER_PY" db-ready --db-file "$tmp_db"
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
	python3 "$HELPER_PY" count-log \
		--container "$SAITO_CONTAINER" \
		--since "$since" \
		--msg "event processed" \
		--event-type "cron.tick" \
		--status "success"
}

count_saito_to_kairo() {
	local since="$1"
	python3 "$HELPER_PY" count-log \
		--container "$SAITO_CONTAINER" \
		--since "$since" \
		--msg "matrix.send_message" \
		--agent-id "saito" \
		--target "kairo" \
		--status "success"
}

count_kairo_to_kumo() {
	local since="$1"
	python3 "$HELPER_PY" count-log \
		--container "$KAIRO_CONTAINER" \
		--since "$since" \
		--msg "matrix.send_message" \
		--agent-id "kairo" \
		--target "kumo" \
		--status "success"
}

count_deliveries() {
	local since="$1"
	python3 "$HELPER_PY" count-log \
		--container "$KUMO_CONTAINER" \
		--since "$since" \
		--msg "matrix.send_message" \
		--agent-id "kumo" \
		--target "kairo" \
		--status "success"
}

discover_admin_rooms() {
	python3 "$HELPER_PY" admin-rooms-csv --env-file "$COMPOSE_ENV_FILE"
}

discover_matrix_base_url() {
	python3 "$HELPER_PY" matrix-base-url --env-file "$COMPOSE_ENV_FILE"
}

urlencode() {
	python3 "$HELPER_PY" urlencode --value "$1"
}

verify_room_joined() {
	local token="$1"
	local matrix_base="$2"
	local room_id="$3"

	curl -fsS "${matrix_base}/_matrix/client/v3/joined_rooms" \
		-H "Authorization: Bearer ${token}" \
		-H "Accept: application/json" | python3 "$HELPER_PY" joined-has-room --room "$room_id"
}

join_room_with_fallback() {
	local token="$1"
	local matrix_base="$2"
	local room_id_or_alias="$3"
	local encoded
	encoded="$(urlencode "$room_id_or_alias")"

	if curl -fsS -X POST "${matrix_base}/_matrix/client/v3/rooms/${encoded}/join" \
		-H "Authorization: Bearer ${token}" \
		-H "Content-Type: application/json" \
		-d '{}' >/dev/null; then
		return 0
	fi

	curl -fsS -X POST "${matrix_base}/_matrix/client/v3/join/${encoded}" \
		-H "Authorization: Bearer ${token}" \
		-H "Content-Type: application/json" \
		-d '{}' >/dev/null
}

ensure_canonical_room_joins() {
	local admin_rooms_csv="$1"
	local matrix_base="$2"
	local admin_rooms=()
	IFS=',' read -r -a admin_rooms <<< "$admin_rooms_csv"
	[[ ${#admin_rooms[@]} -gt 0 ]] || fail "MATRIX_ADMIN_ROOMS is empty; cannot enforce joins"

	for agent in saito kairo kumo; do
		local cname token
		cname="$(agent_container_name "$agent")"
		token="$(docker exec "$cname" sh -lc 'printf %s "$MATRIX_ACCESS_TOKEN"' 2>/dev/null || true)"
		[[ -n "$token" ]] || fail "${cname} missing MATRIX_ACCESS_TOKEN; cannot enforce room join"

		for admin_room in "${admin_rooms[@]}"; do
			admin_room="${admin_room## }"
			admin_room="${admin_room%% }"
			[[ -n "$admin_room" ]] || continue
			if ! join_room_with_fallback "$token" "$matrix_base" "$admin_room"; then
				fail "failed to force ${agent} join into admin room ${admin_room}"
			fi
			if ! verify_room_joined "$token" "$matrix_base" "$admin_room"; then
				fail "${agent} did not appear in joined_rooms for ${admin_room} after join"
			fi
		done
	done

	pass "canonical agents joined admin room(s): ${admin_rooms_csv}"
}

bootstrap_canonical_configs() {
	info "Auto-bootstrap canonical Gosuto configs for saito/kairo/kumo"

	local admin_room user_room
	admin_room="$(python3 "$HELPER_PY" admin-room --env-file "$COMPOSE_ENV_FILE")"
	user_room="$(python3 "$HELPER_PY" user-room --env-file "$COMPOSE_ENV_FILE" --fallback '!canonical-user-fallback:localhost')"

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
	ADMIN_ROOMS="$(discover_admin_rooms)"
	[[ -n "$ADMIN_ROOMS" ]] || fail "failed to discover MATRIX_ADMIN_ROOMS for join enforcement"
	MATRIX_BASE_URL="$(discover_matrix_base_url)"
	ensure_canonical_room_joins "$ADMIN_ROOMS" "$MATRIX_BASE_URL"
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

	# Fail-fast: once cron ticks are flowing, continuing to wait is usually wasted
	# time if any downstream stage remains stalled (most often room-membership,
	# policy, or config propagation issues). Bail out early with focused diagnostics.
	if (( ELAPSED >= FAILFAST_AFTER_SECONDS )) && (( CRON_COUNT >= REQUIRED_CYCLES )) && (( SK_COUNT < REQUIRED_CYCLES || KK_COUNT == 0 || DELIVERY_COUNT == 0 )); then
		info "Fail-fast triggered after ${ELAPSED}s: cron active but chain is stalled"
		info "Recent Saito logs:"
		docker logs --since 10m "$SAITO_CONTAINER" 2>&1 | tail -n 60 || true
		info "Recent Kairo logs:"
		docker logs --since 10m "$KAIRO_CONTAINER" 2>&1 | tail -n 80 || true
		info "Recent Kumo logs:"
		docker logs --since 10m "$KUMO_CONTAINER" 2>&1 | tail -n 80 || true
		fail "canonical chain stalled (cron=${CRON_COUNT}, saito→kairo=${SK_COUNT}, kairo→kumo=${KK_COUNT}, deliveries=${DELIVERY_COUNT}) after ${ELAPSED}s; avoid waiting full timeout"
	fi

	if (( CRON_COUNT >= REQUIRED_CYCLES )) && (( SK_COUNT >= REQUIRED_CYCLES )) && (( KK_COUNT >= REQUIRED_CYCLES )) && (( DELIVERY_COUNT >= REQUIRED_CYCLES )); then
		pass "canonical live cycle observed for required threshold"
		break
	fi

	sleep "$POLL_SECONDS"
done

rm -rf "$DB_SNAPSHOT_DIR"

pass "Canonical live compose workflow check passed"

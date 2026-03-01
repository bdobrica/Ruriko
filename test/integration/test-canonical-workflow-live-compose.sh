#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${CANONICAL_COMPOSE_FILE:-$ROOT_DIR/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV_FILE="${CANONICAL_COMPOSE_ENV:-$ROOT_DIR/examples/docker-compose/.env}"
HELPER_PY="$ROOT_DIR/test/integration/canonical_live_helpers.py"
KEEP_STACK="${KEEP_STACK:-0}"
TIMEOUT_SECONDS="${CANONICAL_LIVE_TIMEOUT_SECONDS:-600}"
POLL_SECONDS="${CANONICAL_LIVE_POLL_SECONDS:-5}"
REQUIRED_CYCLES="${CANONICAL_REQUIRED_CYCLES:-2}"
VERIFY_STAGE="${CANONICAL_VERIFY_STAGE:-full}"
AUTO_BOOTSTRAP_CANONICAL="${CANONICAL_AUTO_BOOTSTRAP:-1}"
BOOTSTRAP_STATUS_TIMEOUT="${CANONICAL_BOOTSTRAP_STATUS_TIMEOUT:-120}"
CANONICAL_FAST_CRON_EVERY="${CANONICAL_FAST_CRON_EVERY:-10s}"
FAILFAST_AFTER_SECONDS="${CANONICAL_FAILFAST_AFTER_SECONDS:-45}"
ENFORCE_ROOM_JOINS="${CANONICAL_ENFORCE_ROOM_JOINS:-1}"
DB_AGENT_WAIT_SECONDS="${CANONICAL_DB_AGENT_WAIT_SECONDS:-20}"
AUTO_PROVISION_CANONICAL="${CANONICAL_AUTO_PROVISION:-0}"
STOP_SAITO_AFTER_CRONS="${CANONICAL_STOP_SAITO_AFTER_CRONS:-0}"

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
[[ -f "$COMPOSE_ENV_FILE" ]] || fail "compose env file not found: $COMPOSE_ENV_FILE"

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

wait_for_canonical_agents_in_db() {
	local timeout_seconds="${1:-20}"
	local start now elapsed
	start="$(date +%s)"
	while true; do
		local tmp_db_dir tmp_db missing
		tmp_db_dir="$(mktemp -d)"
		tmp_db="${tmp_db_dir}/ruriko.db"
		if docker cp ruriko:/data/ruriko.db "$tmp_db" >/dev/null 2>&1; then
			docker cp ruriko:/data/ruriko.db-wal "${tmp_db_dir}/ruriko.db-wal" >/dev/null 2>&1 || true
			docker cp ruriko:/data/ruriko.db-shm "${tmp_db_dir}/ruriko.db-shm" >/dev/null 2>&1 || true
			if missing="$(python3 "$HELPER_PY" db-has-agents --db-file "$tmp_db" --ids "saito,kairo,kumo" 2>/dev/null)"; then
				rm -rf "$tmp_db_dir"
				pass "ruriko database contains canonical agents: saito,kairo,kumo"
				return 0
			fi
		fi
		rm -rf "$tmp_db_dir"
		now="$(date +%s)"
		elapsed=$((now - start))
		if (( elapsed >= timeout_seconds )); then
			CANONICAL_DB_MISSING="${missing:-saito,kairo,kumo}"
			return 1
		fi
		sleep 1
	done
}

CANONICAL_DB_MISSING=""
if ! wait_for_canonical_agents_in_db "$DB_AGENT_WAIT_SECONDS"; then
	if [[ "$AUTO_PROVISION_CANONICAL" == "1" ]]; then
		info "canonical agents missing in database (missing: ${CANONICAL_DB_MISSING:-saito,kairo,kumo}); CANONICAL_AUTO_PROVISION=1 enabled, will synthesize snapshot rows from running containers"
	else
		fail "canonical agents missing in ruriko database after ${DB_AGENT_WAIT_SECONDS}s (missing: ${CANONICAL_DB_MISSING:-saito,kairo,kumo}); provision agents first (or set CANONICAL_AUTO_PROVISION=1 to synthesize snapshot rows from running containers)"
	fi
fi

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

canonical_template_for_agent() {
	case "$1" in
		saito) echo "saito-agent" ;;
		kairo) echo "kairo-agent" ;;
		kumo) echo "kumo-agent" ;;
		*) return 1 ;;
	esac
}

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

maybe_autoprovision_canonical_db_snapshot() {
	local missing
	if missing="$(python3 "$HELPER_PY" db-has-agents --db-file "$DB_FILE" --ids "saito,kairo,kumo" 2>/dev/null)"; then
		return 0
	fi

	[[ "$AUTO_PROVISION_CANONICAL" == "1" ]] || fail "canonical agents missing in bootstrap snapshot db (missing: ${missing:-saito,kairo,kumo})"

	info "Auto-provisioning canonical bootstrap snapshot rows from running containers (missing: ${missing:-saito,kairo,kumo})"
	local agent template cname
	for agent in saito kairo kumo; do
		template="$(canonical_template_for_agent "$agent")"
		cname="$(agent_container_name "$agent")"
		python3 "$HELPER_PY" db-upsert-agent-from-container \
			--db-file "$DB_FILE" \
			--agent-id "$agent" \
			--template "$template" \
			--container "$cname" >/dev/null || fail "failed to synthesize db row for ${agent} from ${cname}"
	done

	if ! python3 "$HELPER_PY" db-has-agents --db-file "$DB_FILE" --ids "saito,kairo,kumo" >/dev/null 2>&1; then
		fail "auto-provision could not establish canonical rows in bootstrap snapshot db"
	fi
	pass "auto-provisioned canonical bootstrap snapshot rows from running containers"
}

maybe_autoprovision_canonical_db_snapshot

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

maybe_stop_saito_after_crons() {
	local cron_count="$1"
	if [[ "$STOP_SAITO_AFTER_CRONS" == "0" || -z "$STOP_SAITO_AFTER_CRONS" ]]; then
		return
	fi
	if [[ "${SAITO_STOPPED:-0}" == "1" ]]; then
		return
	fi
	if (( cron_count < STOP_SAITO_AFTER_CRONS )); then
		return
	fi
	if docker ps --format '{{.Names}}' | grep -q "^${SAITO_CONTAINER}$"; then
		info "Stopping ${SAITO_CONTAINER} after ${cron_count} cron events (limit=${STOP_SAITO_AFTER_CRONS})"
		docker stop "$SAITO_CONTAINER" >/dev/null 2>&1 || true
	fi
	SAITO_STOPPED=1
}

stage_passed() {
	local stage="$1"
	local required="$2"
	local cron_count="$3"
	local sk_count="$4"
	local kk_count="$5"
	local delivery_count="$6"

	case "$stage" in
		full)
			(( cron_count >= required )) && (( sk_count >= required )) && (( kk_count >= required )) && (( delivery_count >= required ))
			;;
		saito-kairo)
			(( cron_count >= required )) && (( sk_count >= required ))
			;;
		kairo-kumo)
			(( sk_count >= required )) && (( kk_count >= required ))
			;;
		kumo-kairo)
			(( kk_count >= required )) && (( delivery_count >= required ))
			;;
		*)
			fail "unsupported CANONICAL_VERIFY_STAGE=${stage}; expected one of: full, saito-kairo, kairo-kumo, kumo-kairo"
			;;
	esac
}

stage_stalled() {
	local stage="$1"
	local required="$2"
	local cron_count="$3"
	local sk_count="$4"
	local kk_count="$5"
	local delivery_count="$6"

	case "$stage" in
		full)
			(( cron_count >= required )) && (( sk_count < required || kk_count == 0 || delivery_count == 0 ))
			;;
		saito-kairo)
			(( cron_count >= required )) && (( sk_count < required ))
			;;
		kairo-kumo)
			(( sk_count >= required )) && (( kk_count < required ))
			;;
		kumo-kairo)
			(( kk_count >= required )) && (( delivery_count < required ))
			;;
		*)
			return 1
			;;
	esac
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
	local encoded payload
	encoded="$(urlencode "$room_id_or_alias")"

	if payload="$(curl -fsS -X POST "${matrix_base}/_matrix/client/v3/rooms/${encoded}/join" \
		-H "Authorization: Bearer ${token}" \
		-H "Content-Type: application/json" \
		-d '{}')"; then
		python3 "$HELPER_PY" extract-join-room-id --payload "$payload" --fallback "$room_id_or_alias"
		return 0
	fi

	payload="$(curl -fsS -X POST "${matrix_base}/_matrix/client/v3/join/${encoded}" \
		-H "Authorization: Bearer ${token}" \
		-H "Content-Type: application/json" \
		-d '{}')"
	python3 "$HELPER_PY" extract-join-room-id --payload "$payload" --fallback "$room_id_or_alias"
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
			local joined_room_id
			[[ -n "$admin_room" ]] || continue
			if ! joined_room_id="$(join_room_with_fallback "$token" "$matrix_base" "$admin_room")"; then
				fail "failed to force ${agent} join into admin room ${admin_room}"
			fi
			joined_room_id="$(printf '%s' "$joined_room_id" | tr -d '\r\n')"
			[[ -n "$joined_room_id" ]] || joined_room_id="$admin_room"
			if ! verify_room_joined "$token" "$matrix_base" "$joined_room_id"; then
				fail "${agent} did not appear in joined_rooms for ${joined_room_id} after join request ${admin_room}"
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
SAITO_STOPPED=0
info "Watching canonical stage=${VERIFY_STAGE} (required=${REQUIRED_CYCLES}, timeout=${TIMEOUT_SECONDS}s, poll=${POLL_SECONDS}s)"

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
	maybe_stop_saito_after_crons "$CRON_COUNT"

	info "elapsed=${ELAPSED}s cron=${CRON_COUNT} saito→kairo=${SK_COUNT} kairo→kumo=${KK_COUNT} deliveries=${DELIVERY_COUNT}"

	# Fail-fast: once cron ticks are flowing, continuing to wait is usually wasted
	# time if any downstream stage remains stalled (most often room-membership,
	# policy, or config propagation issues). Bail out early with focused diagnostics.
	if (( ELAPSED >= FAILFAST_AFTER_SECONDS )) && stage_stalled "$VERIFY_STAGE" "$REQUIRED_CYCLES" "$CRON_COUNT" "$SK_COUNT" "$KK_COUNT" "$DELIVERY_COUNT"; then
		info "Fail-fast triggered after ${ELAPSED}s: cron active but chain is stalled"
		info "Recent Saito logs:"
		docker logs --since 10m "$SAITO_CONTAINER" 2>&1 | tail -n 60 || true
		info "Recent Kairo logs:"
		docker logs --since 10m "$KAIRO_CONTAINER" 2>&1 | tail -n 80 || true
		info "Recent Kumo logs:"
		docker logs --since 10m "$KUMO_CONTAINER" 2>&1 | tail -n 80 || true
		fail "canonical chain stalled (cron=${CRON_COUNT}, saito→kairo=${SK_COUNT}, kairo→kumo=${KK_COUNT}, deliveries=${DELIVERY_COUNT}) after ${ELAPSED}s; avoid waiting full timeout"
	fi

	if stage_passed "$VERIFY_STAGE" "$REQUIRED_CYCLES" "$CRON_COUNT" "$SK_COUNT" "$KK_COUNT" "$DELIVERY_COUNT"; then
		pass "canonical live stage ${VERIFY_STAGE} observed for required threshold"
		break
	fi

	sleep "$POLL_SECONDS"
done

rm -rf "$DB_SNAPSHOT_DIR"

pass "Canonical live compose workflow check passed (stage=${VERIFY_STAGE})"

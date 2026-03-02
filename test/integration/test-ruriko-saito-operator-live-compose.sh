#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_DIR="${ROOT_DIR}/examples/docker-compose"
COMPOSE_FILE="${RURIKO_SAITO_COMPOSE_FILE:-${COMPOSE_DIR}/docker-compose.yaml}"
COMPOSE_ENV_FILE="${RURIKO_SAITO_COMPOSE_ENV:-${COMPOSE_DIR}/.env}"
TOKENS_FILE="${COMPOSE_DIR}/.agent-tokens"
HELPER_PY="${ROOT_DIR}/test/integration/canonical_live_helpers.py"
PROBE_PY="${ROOT_DIR}/test/integration/ruriko_saito_live_matrix_probe.py"
PROVISION_SCRIPT="${ROOT_DIR}/test/integration/provision-fresh-stack.sh"
TIMEOUT_SECONDS="${RURIKO_SAITO_TIMEOUT_SECONDS:-300}"
CRON_EXPR="${RURIKO_SAITO_CRON_EXPR:-*/2 * * * *}"
CRON_MESSAGE="${RURIKO_SAITO_CRON_MESSAGE:-Saito scheduled heartbeat to operator}"
SYNC_TIMEOUT_MS="${RURIKO_SAITO_SYNC_TIMEOUT_MS:-30000}"
KEEP_STACK="${KEEP_STACK:-0}"

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
		info "KEEP_STACK=1 set; leaving compose stack running"
		return
	fi
	info "Stopping compose stack"
	(
		cd "$COMPOSE_DIR"
		docker_compose down -v --remove-orphans >/dev/null 2>&1 || true
	)
}
trap cleanup EXIT

require_tool() {
	command -v "$1" >/dev/null 2>&1 || fail "required tool '$1' not found"
}

require_tool docker
require_tool python3
require_tool curl
[[ -f "$COMPOSE_FILE" ]] || fail "compose file not found: $COMPOSE_FILE"
[[ -f "$COMPOSE_ENV_FILE" ]] || fail "compose env file not found: $COMPOSE_ENV_FILE"
[[ -f "$HELPER_PY" ]] || fail "helper script not found: $HELPER_PY"
[[ -f "$PROBE_PY" ]] || fail "probe script not found: $PROBE_PY"
[[ -f "$PROVISION_SCRIPT" ]] || fail "provisioning script not found: $PROVISION_SCRIPT"

info "Step 1/10: provisioning a fresh deterministic stack (operator + ruriko accounts, admin/user rooms)"
bash "$PROVISION_SCRIPT"

[[ -f "$TOKENS_FILE" ]] || fail "tokens file not found after provisioning: $TOKENS_FILE"
source "$TOKENS_FILE"
[[ -n "${OPERATOR_TOKEN:-}" ]] || fail "OPERATOR_TOKEN missing in ${TOKENS_FILE}"
[[ -n "${ADMIN_ROOM:-}" ]] || fail "ADMIN_ROOM missing in ${TOKENS_FILE}"
[[ -n "${USER_ROOM:-}" ]] || fail "USER_ROOM missing in ${TOKENS_FILE}"

source "$COMPOSE_ENV_FILE"
AGENT_IMAGE="${DEFAULT_AGENT_IMAGE:-gitai:latest}"
MATRIX_BASE_URL="$(python3 "$HELPER_PY" matrix-base-url --env-file "$COMPOSE_ENV_FILE")"
[[ -n "$MATRIX_BASE_URL" ]] || fail "failed to discover Matrix base URL"

info "Step 2/10: starting compose stack (Ruriko + Tuwunel)"
(
	cd "$COMPOSE_DIR"
	docker_compose up -d
)

info "Step 3/10: waiting for Ruriko and Tuwunel health"
for attempt in {1..90}; do
	if docker ps --format '{{.Names}}' | grep -q '^ruriko$' && docker ps --format '{{.Names}}' | grep -q '^tuwunel$'; then
		if curl -fsS "http://127.0.0.1:${HTTP_ADDR_PORT:-8080}/health" >/dev/null 2>&1; then
			pass "Ruriko health endpoint is reachable"
			break
		fi
	fi
	if [[ "$attempt" == "90" ]]; then
		fail "Ruriko stack did not become ready in time"
	fi
	sleep 1
done

ruriko_db_has_saito() {
	local tmp_db_dir tmp_db
	tmp_db_dir="$(mktemp -d)"
	tmp_db="${tmp_db_dir}/ruriko.db"
	if ! docker cp ruriko:/data/ruriko.db "$tmp_db" >/dev/null 2>&1; then
		rm -rf "$tmp_db_dir"
		return 1
	fi
	docker cp ruriko:/data/ruriko.db-wal "${tmp_db_dir}/ruriko.db-wal" >/dev/null 2>&1 || true
	docker cp ruriko:/data/ruriko.db-shm "${tmp_db_dir}/ruriko.db-shm" >/dev/null 2>&1 || true
	if python3 "$HELPER_PY" db-has-agents --db-file "$tmp_db" --ids "saito" >/dev/null 2>&1; then
		rm -rf "$tmp_db_dir"
		return 0
	fi
	rm -rf "$tmp_db_dir"
	return 1
}

send_operator_command() {
	local command_text="$1"
	local txn_id="$2"
	info "[MATRIX][send] sender=@operator:localhost room=${ADMIN_ROOM} body=${command_text}"
	local event_id
	event_id="$(python3 "$PROBE_PY" send \
		--base-url "$MATRIX_BASE_URL" \
		--token "$OPERATOR_TOKEN" \
		--room "$ADMIN_ROOM" \
		--body "$command_text" \
		--txn-id "$txn_id")"
	pass "[MATRIX][send-ok] event_id=${event_id}"
}

urlencode() {
	python3 "$HELPER_PY" urlencode --value "$1"
}

invite_user_to_room() {
	local token="$1"
	local room_id="$2"
	local mxid="$3"
	local room_enc
	room_enc="$(urlencode "$room_id")"
	curl -fsS -X POST "${MATRIX_BASE_URL}/_matrix/client/v3/rooms/${room_enc}/invite" \
		-H "Authorization: Bearer ${token}" \
		-H "Content-Type: application/json" \
		-d "{\"user_id\":\"${mxid}\"}" >/dev/null
}

join_room_with_token() {
	local token="$1"
	local room_id="$2"
	local room_enc
	room_enc="$(urlencode "$room_id")"
	if ! curl -fsS -X POST "${MATRIX_BASE_URL}/_matrix/client/v3/rooms/${room_enc}/join" \
		-H "Authorization: Bearer ${token}" \
		-H "Content-Type: application/json" \
		-d '{}' >/dev/null 2>&1; then
		curl -fsS -X POST "${MATRIX_BASE_URL}/_matrix/client/v3/join/${room_enc}" \
			-H "Authorization: Bearer ${token}" \
			-H "Content-Type: application/json" \
			-d '{}' >/dev/null
	fi
}

ensure_saito_joined_user_room() {
	local saito_token="$1"
	local saito_mxid="@saito:localhost"

	if curl -fsS "${MATRIX_BASE_URL}/_matrix/client/v3/joined_rooms" \
		-H "Authorization: Bearer ${saito_token}" \
		-H "Accept: application/json" | python3 "$HELPER_PY" joined-has-room --room "$USER_ROOM"; then
		pass "Saito already joined operator room ${USER_ROOM}"
		return 0
	fi

	info "Saito not in operator room yet; inviting and forcing join"
	invite_user_to_room "$OPERATOR_TOKEN" "$USER_ROOM" "$saito_mxid"
	join_room_with_token "$saito_token" "$USER_ROOM"

	if ! curl -fsS "${MATRIX_BASE_URL}/_matrix/client/v3/joined_rooms" \
		-H "Authorization: Bearer ${saito_token}" \
		-H "Accept: application/json" | python3 "$HELPER_PY" joined-has-room --room "$USER_ROOM"; then
		fail "Saito is not joined in USER_ROOM=${USER_ROOM} after invite/join"
	fi

	pass "Saito joined operator room ${USER_ROOM}"
}

wait_for_saito_gosuto_loaded() {
	local deadline=$(( $(date +%s) + 120 ))
	local start_ts
	start_ts="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

	while [[ $(date +%s) -lt $deadline ]]; do
		if docker logs --since "$start_ts" ruriko-agent-saito 2>&1 | grep -qE 'gosuto config applied|ACP: config applied'; then
			local cfg_hash
			cfg_hash="$(docker logs --since "$start_ts" ruriko-agent-saito 2>&1 | grep -E 'gosuto config applied|ACP: config applied' | tail -n1 | sed -n 's/.*hash=\([^ ]*\).*/\1/p')"
			if [[ -n "$cfg_hash" ]]; then
				pass "Saito Gosuto config loaded (hash=${cfg_hash})"
			else
				pass "Saito Gosuto config loaded"
			fi
			return 0
		fi
		sleep 2
	done

	info "Recent Saito logs while waiting for Gosuto convergence:"
	docker logs --since "$start_ts" ruriko-agent-saito 2>&1 | tail -n 80 || true
	fail "Timed out waiting for Saito Gosuto config apply log"
}

get_operator_sync_token() {
	local payload
	payload="$(python3 "$PROBE_PY" sync \
		--base-url "$MATRIX_BASE_URL" \
		--token "$OPERATOR_TOKEN" \
		--since "" \
		--timeout-ms 0 \
		--rooms "${ADMIN_ROOM},${USER_ROOM}")"
	printf '%s' "$payload" | python3 "$PROBE_PY" next-batch
}

wait_for_ruriko_help_response() {
	local since_token="$1"
	local deadline=$(( $(date +%s) + 60 ))
	local token="$since_token"

	while [[ $(date +%s) -lt $deadline ]]; do
		local payload new_token has_schedule_help unknown_command
		payload="$(python3 "$PROBE_PY" sync \
			--base-url "$MATRIX_BASE_URL" \
			--token "$OPERATOR_TOKEN" \
			--since "$token" \
			--timeout-ms 10000 \
			--rooms "${ADMIN_ROOM}" \
			--senders "@ruriko:localhost")"
		printf '%s' "$payload" | python3 "$PROBE_PY" events-print || true
		new_token="$(printf '%s' "$payload" | python3 "$PROBE_PY" next-batch)"
		[[ -n "$new_token" ]] && token="$new_token"

		has_schedule_help="$(printf '%s' "$payload" | python3 "$PROBE_PY" events-count --room "$ADMIN_ROOM" --sender "@ruriko:localhost" --contains "/ruriko schedule upsert")"
		unknown_command="$(printf '%s' "$payload" | python3 "$PROBE_PY" events-count --room "$ADMIN_ROOM" --sender "@ruriko:localhost" --contains "unknown command")"

		if (( has_schedule_help > 0 )); then
			pass "Ruriko help confirms schedule commands are available"
			return 0
		fi
		if (( unknown_command > 0 )); then
			fail "Ruriko returned unknown command while handling /ruriko help"
		fi
	done

	fail "Timed out waiting for /ruriko help response from Ruriko"
}

ensure_ruriko_supports_schedule_commands() {
	local sync_token
	sync_token="$(get_operator_sync_token)"
	[[ -n "$sync_token" ]] || fail "could not obtain operator sync token before /ruriko help preflight"

	send_operator_command "/ruriko help" "rso-help-preflight-$(date +%s)"
	wait_for_ruriko_help_response "$sync_token"
}

wait_for_ruriko_schedule_ack() {
	local since_token="$1"
	local deadline=$(( $(date +%s) + 90 ))
	local token="$since_token"

	while [[ $(date +%s) -lt $deadline ]]; do
		local payload new_token created_count updated_count generic_error_count unknown_cmd_count
		payload="$(python3 "$PROBE_PY" sync \
			--base-url "$MATRIX_BASE_URL" \
			--token "$OPERATOR_TOKEN" \
			--since "$token" \
			--timeout-ms 12000 \
			--rooms "${ADMIN_ROOM}" \
			--senders "@ruriko:localhost")"
		printf '%s' "$payload" | python3 "$PROBE_PY" events-print || true
		new_token="$(printf '%s' "$payload" | python3 "$PROBE_PY" next-batch)"
		[[ -n "$new_token" ]] && token="$new_token"

		created_count="$(printf '%s' "$payload" | python3 "$PROBE_PY" events-count --room "$ADMIN_ROOM" --sender "@ruriko:localhost" --contains "Created schedule")"
		updated_count="$(printf '%s' "$payload" | python3 "$PROBE_PY" events-count --room "$ADMIN_ROOM" --sender "@ruriko:localhost" --contains "Updated schedule")"
		generic_error_count="$(printf '%s' "$payload" | python3 "$PROBE_PY" events-count --room "$ADMIN_ROOM" --sender "@ruriko:localhost" --contains "❌ Error:")"
		unknown_cmd_count="$(printf '%s' "$payload" | python3 "$PROBE_PY" events-count --room "$ADMIN_ROOM" --sender "@ruriko:localhost" --contains "unknown command: schedule.upsert")"

		if (( created_count > 0 )); then
			pass "Ruriko acknowledged schedule creation"
			return 0
		fi
		if (( updated_count > 0 )); then
			pass "Ruriko acknowledged schedule update"
			return 0
		fi
		if (( unknown_cmd_count > 0 )); then
			fail "Ruriko replied 'unknown command: schedule.upsert' — image is likely outdated. Rebuild and retry: make docker-build"
		fi
		if (( generic_error_count > 0 )); then
			fail "Ruriko replied with an error to schedule command; inspect admin-room output above"
		fi
	done

	fail "Timed out waiting for Ruriko schedule acknowledgement in admin room"
}

info "Step 4/10: preflight checking schedule command availability in running Ruriko"
if ! ensure_ruriko_supports_schedule_commands; then
	fail "Ruriko schedule command unavailable in running image. Rebuild images and retry: make docker-build"
fi

info "Step 5/10: ensuring Saito is provisioned via deterministic /ruriko command"
if ruriko_db_has_saito; then
	pass "Saito already provisioned in Ruriko DB"
else
	send_operator_command "/ruriko agents create --name saito --template saito-agent --image ${AGENT_IMAGE}" "rso-create-saito-$(date +%s)"
fi

info "Step 6/10: waiting for Saito container and Ruriko DB row"
for attempt in {1..180}; do
	if docker ps --format '{{.Names}}' | grep -q '^ruriko-agent-saito$' && ruriko_db_has_saito; then
		pass "Saito is running and present in Ruriko DB"
		break
	fi
	if [[ "$attempt" == "180" ]]; then
		docker logs --tail 120 ruriko 2>&1 || true
		fail "Saito did not become ready in time"
	fi
	sleep 1
done

info "Step 7/10: waiting for Saito Gosuto convergence"
wait_for_saito_gosuto_loaded

info "Step 8/10: operator sends deterministic schedule command to Ruriko"
SCHEDULE_CMD="/ruriko schedule upsert --agent saito --cron \"${CRON_EXPR}\" --target user --message \"${CRON_MESSAGE}\""
SYNC_TOKEN_BEFORE_SCHEDULE="$(get_operator_sync_token)"
[[ -n "$SYNC_TOKEN_BEFORE_SCHEDULE" ]] || fail "could not obtain operator sync token before schedule command"
send_operator_command "$SCHEDULE_CMD" "rso-schedule-upsert-$(date +%s)"
wait_for_ruriko_schedule_ack "$SYNC_TOKEN_BEFORE_SCHEDULE"

info "Step 9/10: validating Saito DB contains the cron schedule"
for attempt in {1..90}; do
	tmp_db_dir="$(mktemp -d)"
	tmp_db="${tmp_db_dir}/gitai.db"
	if docker cp ruriko-agent-saito:/data/gitai.db "$tmp_db" >/dev/null 2>&1; then
		docker cp ruriko-agent-saito:/data/gitai.db-wal "${tmp_db_dir}/gitai.db-wal" >/dev/null 2>&1 || true
		docker cp ruriko-agent-saito:/data/gitai.db-shm "${tmp_db_dir}/gitai.db-shm" >/dev/null 2>&1 || true
		if python3 "$HELPER_PY" db-has-schedule \
			--db-file "$tmp_db" \
			--gateway scheduler \
			--cron "$CRON_EXPR" \
			--target user \
			--message "$CRON_MESSAGE" >/dev/null 2>&1; then
			rm -rf "$tmp_db_dir"
			pass "Saito schedule row exists in gitai.db"
			break
		fi
	fi
	rm -rf "$tmp_db_dir"
	if [[ "$attempt" == "90" ]]; then
		fail "Saito schedule row not found in gitai.db"
	fi
	sleep 1
done

info "Step 10/10: verifying Saito joined operator room"
SAITO_TOKEN="$(docker exec ruriko-agent-saito sh -lc 'printf %s "$MATRIX_ACCESS_TOKEN"' 2>/dev/null || true)"
[[ -n "$SAITO_TOKEN" ]] || fail "could not read MATRIX_ACCESS_TOKEN from ruriko-agent-saito"
ensure_saito_joined_user_room "$SAITO_TOKEN"

info "Step 11/11: waiting up to ${TIMEOUT_SECONDS}s for two cron cycles and printing message exchange"
START_TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
INITIAL_SYNC_PAYLOAD="$(python3 "$PROBE_PY" sync \
	--base-url "$MATRIX_BASE_URL" \
	--token "$OPERATOR_TOKEN" \
	--since "" \
	--timeout-ms 0 \
	--rooms "${ADMIN_ROOM},${USER_ROOM}")"
SYNC_TOKEN="$(printf '%s' "$INITIAL_SYNC_PAYLOAD" | python3 "$PROBE_PY" next-batch)"
[[ -n "$SYNC_TOKEN" ]] || fail "failed to obtain initial sync token"

SAITO_MESSAGES=0
SAITO_SEND_SUCCESSES=0
START_EPOCH="$(date +%s)"

while true; do
	NOW_EPOCH="$(date +%s)"
	ELAPSED=$((NOW_EPOCH - START_EPOCH))
	if (( ELAPSED > TIMEOUT_SECONDS )); then
		fail "timeout after ${TIMEOUT_SECONDS}s waiting for two Saito cron messages"
	fi

	SYNC_PAYLOAD="$(python3 "$PROBE_PY" sync \
		--base-url "$MATRIX_BASE_URL" \
		--token "$OPERATOR_TOKEN" \
		--since "$SYNC_TOKEN" \
		--timeout-ms "$SYNC_TIMEOUT_MS" \
		--rooms "${ADMIN_ROOM},${USER_ROOM}")"
	NEW_TOKEN="$(printf '%s' "$SYNC_PAYLOAD" | python3 "$PROBE_PY" next-batch)"
	[[ -n "$NEW_TOKEN" ]] && SYNC_TOKEN="$NEW_TOKEN"

	printf '%s' "$SYNC_PAYLOAD" | python3 "$PROBE_PY" events-print || true

	DELTA_SAITO="$(printf '%s' "$SYNC_PAYLOAD" | python3 "$PROBE_PY" events-count --sender "@saito:localhost" --contains "$CRON_MESSAGE")"
	SAITO_MESSAGES=$((SAITO_MESSAGES + DELTA_SAITO))
	SAITO_SEND_SUCCESSES="$(python3 "$HELPER_PY" count-log --container "ruriko-agent-saito" --since "$START_TS" --msg "matrix.send_message" --agent-id "saito" --target "user" --status "success")"

	info "progress elapsed=${ELAPSED}s saito_messages=${SAITO_MESSAGES} saito_send_success=${SAITO_SEND_SUCCESSES}"

	if (( SAITO_MESSAGES >= 2 && SAITO_SEND_SUCCESSES >= 2 )); then
		pass "Observed two Saito cron cycles and two operator-visible messages"
		break
	fi
done
info "Final: printing Ruriko/Saito runtime log excerpts for send/receive auditing"
echo "--- Ruriko log excerpts ---"
docker logs --since "$START_TS" ruriko 2>&1 | grep -E 'matrix|schedule|agents create|command|audit|trace' | tail -n 120 || true
echo "--- Saito log excerpts ---"
docker logs --since "$START_TS" ruriko-agent-saito 2>&1 | grep -E 'event processed|cron.tick|matrix.send_message|join room|trace' | tail -n 160 || true

pass "Deterministic Ruriko↔Saito live integration flow passed"

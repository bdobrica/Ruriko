#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${CANONICAL_COMPOSE_FILE:-$ROOT_DIR/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV_FILE="${CANONICAL_COMPOSE_ENV:-$ROOT_DIR/examples/docker-compose/.env}"
HELPER_PY="$ROOT_DIR/test/integration/canonical_live_helpers.py"

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

require_tool() {
	command -v "$1" >/dev/null 2>&1 || fail "required tool '$1' not found"
}

require_tool docker
require_tool python3
require_tool curl
[[ -f "$COMPOSE_ENV_FILE" ]] || fail "compose env file not found: $COMPOSE_ENV_FILE"
[[ -f "$HELPER_PY" ]] || fail "required helper script not found: $HELPER_PY"

info "Admin-room join check: ensuring compose stack is up"
docker_compose up -d

discover_admin_rooms() {
	python3 "$HELPER_PY" admin-rooms-csv --env-file "$COMPOSE_ENV_FILE"
}

discover_matrix_base_url() {
	python3 "$HELPER_PY" matrix-base-url --env-file "$COMPOSE_ENV_FILE"
}

urlencode() {
	python3 "$HELPER_PY" urlencode --value "$1"
}

agent_container_name() {
	local name="$1"
	echo "ruriko-agent-${name}"
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

validate_token() {
	local token="$1"
	local matrix_base="$2"
	local status

	status="$(curl -sS -o /dev/null -w '%{http_code}' "${matrix_base}/_matrix/client/v3/joined_rooms" \
		-H "Authorization: Bearer ${token}" \
		-H "Accept: application/json")"
	if [[ "$status" != "200" ]]; then
		return 1
	fi
	return 0
}

ADMIN_ROOMS="$(discover_admin_rooms)"
[[ -n "$ADMIN_ROOMS" ]] || fail "failed to discover MATRIX_ADMIN_ROOMS"
MATRIX_BASE_URL="$(discover_matrix_base_url)"
[[ -n "$MATRIX_BASE_URL" ]] || fail "failed to discover Matrix base URL"

info "Admin rooms: ${ADMIN_ROOMS}"
info "Matrix base URL: ${MATRIX_BASE_URL}"

admin_rooms=()
IFS=',' read -r -a admin_rooms <<< "$ADMIN_ROOMS"
[[ ${#admin_rooms[@]} -gt 0 ]] || fail "no admin rooms configured"

for agent in saito kairo kumo; do
	cname="$(agent_container_name "$agent")"
	docker ps --format '{{.Names}}' | grep -q "^${cname}$" || fail "container '${cname}' not running"
	token="$(docker exec "$cname" sh -lc 'printf %s "$MATRIX_ACCESS_TOKEN"' 2>/dev/null || true)"
	[[ -n "$token" ]] || fail "${cname} missing MATRIX_ACCESS_TOKEN"

	if ! validate_token "$token" "$MATRIX_BASE_URL"; then
		fail "${cname} has invalid/unauthorized MATRIX_ACCESS_TOKEN against ${MATRIX_BASE_URL} (joined_rooms != 200)"
	fi

	for admin_room in "${admin_rooms[@]}"; do
		admin_room="${admin_room## }"
		admin_room="${admin_room%% }"
		[[ -n "$admin_room" ]] || continue

		if ! joined_room_id="$(join_room_with_fallback "$token" "$MATRIX_BASE_URL" "$admin_room")"; then
			fail "failed to join ${agent} into admin room ${admin_room}"
		fi
		joined_room_id="$(printf '%s' "$joined_room_id" | tr -d '\r\n')"
		[[ -n "$joined_room_id" ]] || joined_room_id="$admin_room"

		if ! verify_room_joined "$token" "$MATRIX_BASE_URL" "$joined_room_id"; then
			fail "${agent} not present in joined_rooms for ${joined_room_id} after join request ${admin_room}"
		fi
		pass "${agent} joined admin room ${joined_room_id}"
	done
done

pass "Canonical admin-room join check passed"

#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${CANONICAL_COMPOSE_FILE:-$ROOT_DIR/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV_FILE="${CANONICAL_COMPOSE_ENV:-$ROOT_DIR/examples/docker-compose/.env}"
HELPER_PY="$ROOT_DIR/test/integration/canonical_live_helpers.py"
WAIT_SECONDS="${CANONICAL_DB_AGENT_WAIT_SECONDS:-20}"

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
[[ -f "$COMPOSE_ENV_FILE" ]] || fail "compose env file not found: $COMPOSE_ENV_FILE"
[[ -f "$HELPER_PY" ]] || fail "required helper script not found: $HELPER_PY"

info "Provisioning precheck: starting compose stack"
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
	local timeout_seconds="${1:-90}"
	local start now elapsed
	start="$(date +%s)"
	while true; do
		local tmp_db_dir tmp_db
		tmp_db_dir="$(mktemp -d)"
		tmp_db="${tmp_db_dir}/ruriko.db"
		if docker cp ruriko:/data/ruriko.db "$tmp_db" >/dev/null 2>&1; then
			docker cp ruriko:/data/ruriko.db-wal "${tmp_db_dir}/ruriko.db-wal" >/dev/null 2>&1 || true
			docker cp ruriko:/data/ruriko.db-shm "${tmp_db_dir}/ruriko.db-shm" >/dev/null 2>&1 || true
			if python3 "$HELPER_PY" db-ready --db-file "$tmp_db"; then
				rm -rf "$tmp_db_dir"
				pass "ruriko database ready"
				return 0
			fi
		fi
		rm -rf "$tmp_db_dir"
		now="$(date +%s)"
		elapsed=$((now - start))
		if (( elapsed >= timeout_seconds )); then
			fail "timed out waiting for ruriko database readiness"
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
				pass "ruriko database has canonical agent rows"
				return 0
			fi
		fi
		rm -rf "$tmp_db_dir"
		now="$(date +%s)"
		elapsed=$((now - start))
		if (( elapsed >= timeout_seconds )); then
			fail "canonical agents missing from ruriko db (missing: ${missing:-saito,kairo,kumo})"
		fi
		sleep 1
	done
}

wait_for_canonical_agents_in_db "$WAIT_SECONDS"

agent_container_name() {
	local name="$1"
	echo "ruriko-agent-${name}"
}

for agent in saito kairo kumo; do
	cname="$(agent_container_name "$agent")"
	docker ps --format '{{.Names}}' | grep -q "^${cname}$" || fail "canonical agent container '${cname}' not running"
	if ! docker exec "$cname" sh -lc 'env | grep -q "^LLM_API_KEY="'; then
		fail "${cname} missing LLM_API_KEY"
	fi
	pass "${cname} container and LLM key present"
done

pass "Canonical provisioning precheck passed"

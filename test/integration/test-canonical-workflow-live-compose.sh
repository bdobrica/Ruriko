#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_DIR="${ROOT_DIR}/examples/docker-compose"
COMPOSE_FILE="${CANONICAL_COMPOSE_FILE:-${COMPOSE_DIR}/docker-compose.yaml}"
COMPOSE_ENV_FILE="${CANONICAL_COMPOSE_ENV:-${COMPOSE_DIR}/.env}"
TOKENS_FILE="${COMPOSE_DIR}/.agent-tokens"
WORK_DIR="${CANONICAL_WORK_DIR:-${ROOT_DIR}/test/integration/.canonical-live}"
HELPER_PY="${ROOT_DIR}/test/integration/canonical_live_helpers.py"
PROBE_PY="${ROOT_DIR}/test/integration/ruriko_saito_live_matrix_probe.py"
PROVISION_SCRIPT="${ROOT_DIR}/test/integration/provision-fresh-stack.sh"
OPENAI_PROXY_SCRIPT="${ROOT_DIR}/test/integration/openai_capture_proxy.py"

TIMEOUT_SECONDS="${CANONICAL_LIVE_TIMEOUT_SECONDS:-75}"
SYNC_TIMEOUT_MS="${CANONICAL_LIVE_SYNC_TIMEOUT_MS:-15000}"
POLL_SECONDS="${CANONICAL_LIVE_POLL_SECONDS:-3}"
REQUIRED_CYCLES="${CANONICAL_REQUIRED_CYCLES:-1}"
SAITO_CRON_EXPR="${CANONICAL_SAITO_CRON_EXPR:-@every 30s}"

OPENAI_CAPTURE="${CANONICAL_OPENAI_CAPTURE_FILE:-${WORK_DIR}/openai-calls.jsonl}"
OPENAI_PROXY_LOG="${CANONICAL_OPENAI_PROXY_LOG:-${WORK_DIR}/openai-proxy.log}"
OPENAI_MODE="${CANONICAL_OPENAI_MODE:-stub}"
OPENAI_PROXY_HOST="${CANONICAL_OPENAI_PROXY_HOST:-0.0.0.0}"
OPENAI_PROXY_PORT="${CANONICAL_OPENAI_PROXY_PORT:-18083}"
OPENAI_UPSTREAM_BASE="${CANONICAL_OPENAI_UPSTREAM_BASE:-https://api.openai.com}"
OPENAI_UPSTREAM_TIMEOUT="${CANONICAL_OPENAI_UPSTREAM_TIMEOUT_SECONDS:-20}"
OPENAI_API_KEY="${CANONICAL_OPENAI_API_KEY:-dummy-live-key}"
OPENAI_EXPECT_MIN_CALLS="${CANONICAL_OPENAI_EXPECT_MIN_CALLS:-2}"

DOCKER_BRIDGE_HOST="${CANONICAL_DOCKER_BRIDGE_HOST:-172.17.0.1}"
KUZE_INTERNAL_BASE_URL="${CANONICAL_KUZE_INTERNAL_BASE_URL:-http://ruriko:8080}"
BRAVE_API_KEY="${CANONICAL_BRAVE_API_KEY:-}"
KUMO_REQUEST_PREFIX="${CANONICAL_KUMO_REQUEST_PREFIX:-KUMO_NEWS_REQUEST}"
KUMO_REQUEST_BODY="${CANONICAL_KUMO_REQUEST_BODY:-{\"run_id\":1,\"tickers\":[\"OpenAI\"]}}"
NL_REQUEST_TEXT="${CANONICAL_NL_REQUEST_TEXT:-I would like to get daily news about OpenAI}"
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
		docker-compose -f "${COMPOSE_FILE}" "$@"
	else
		docker compose -f "${COMPOSE_FILE}" "$@"
	fi
}

cleanup() {
	if [[ -n "${OPENAI_PROXY_PID:-}" ]]; then
		kill "${OPENAI_PROXY_PID}" >/dev/null 2>&1 || true
	fi
	if [[ "${KEEP_STACK}" == "1" ]]; then
		info "KEEP_STACK=1 set; leaving stack running"
		return
	fi
	info "Stopping compose stack"
	(
		cd "${COMPOSE_DIR}"
		docker_compose down -v --remove-orphans >/dev/null 2>&1 || true
	)
}
trap cleanup EXIT

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_cmd docker
require_cmd curl
require_cmd python3
[[ -f "${COMPOSE_FILE}" ]] || fail "compose file not found: ${COMPOSE_FILE}"
[[ -f "${COMPOSE_ENV_FILE}" ]] || fail "compose env file not found: ${COMPOSE_ENV_FILE}"
[[ -f "${HELPER_PY}" ]] || fail "helper script not found: ${HELPER_PY}"
[[ -f "${PROBE_PY}" ]] || fail "probe script not found: ${PROBE_PY}"
[[ -f "${PROVISION_SCRIPT}" ]] || fail "provisioning script not found: ${PROVISION_SCRIPT}"
[[ -f "${OPENAI_PROXY_SCRIPT}" ]] || fail "openai proxy helper not found: ${OPENAI_PROXY_SCRIPT}"

if [[ "${OPENAI_MODE}" != "stub" && "${OPENAI_MODE}" != "passthrough" ]]; then
	fail "CANONICAL_OPENAI_MODE must be 'stub' or 'passthrough'"
fi
if [[ "${OPENAI_MODE}" == "passthrough" ]] && [[ -z "${OPENAI_API_KEY}" || "${OPENAI_API_KEY}" == "dummy-live-key" ]]; then
	fail "CANONICAL_OPENAI_MODE=passthrough requires a real CANONICAL_OPENAI_API_KEY"
fi
if [[ -z "${BRAVE_API_KEY}" ]]; then
	fail "CANONICAL_BRAVE_API_KEY is required for the Saito->Kumo canonical flow"
fi

mkdir -p "${WORK_DIR}"
: > "${OPENAI_CAPTURE}"
: > "${OPENAI_PROXY_LOG}"

wait_for_http() {
	local url="$1"
	local timeout="$2"
	local deadline=$(( $(date +%s) + timeout ))
	while true; do
		if curl -fsS "${url}" >/dev/null 2>&1; then
			return 0
		fi
		if [[ $(date +%s) -ge ${deadline} ]]; then
			return 1
		fi
		sleep 1
	done
}

send_matrix() {
	local body="$1"
	local txn_id="$2"
	python3 "${PROBE_PY}" send \
		--base-url "${MATRIX_BASE_URL}" \
		--token "${OPERATOR_TOKEN}" \
		--room "${ADMIN_ROOM}" \
		--body "${body}" \
		--txn-id "${txn_id}" >/dev/null
}

extract_kuze_link() {
	local payload="$1"
	python3 - "${payload}" <<'PY'
import json
import re
import sys

raw = sys.argv[1]
obj = json.loads(raw)
for evt in obj.get("events", []):
    body = str(evt.get("body", ""))
    m = re.search(r'https?://[^\s]+/s/[A-Za-z0-9_-]+', body)
    if m:
        print(m.group(0))
        sys.exit(0)
print("")
PY
}

wait_for_ruriko_text() {
	local contains="$1"
	local timeout="$2"
	local token="$3"
	local deadline=$(( $(date +%s) + timeout ))
	while [[ $(date +%s) -lt ${deadline} ]]; do
		local payload next_token count
		payload="$(python3 "${PROBE_PY}" sync \
			--base-url "${MATRIX_BASE_URL}" \
			--token "${OPERATOR_TOKEN}" \
			--since "${token}" \
			--timeout-ms "${SYNC_TIMEOUT_MS}" \
			--rooms "${ADMIN_ROOM}" \
			--senders "@ruriko:localhost")"
		next_token="$(printf '%s' "${payload}" | python3 "${PROBE_PY}" next-batch)"
		[[ -n "${next_token}" ]] && token="${next_token}"
		count="$(printf '%s' "${payload}" | python3 "${PROBE_PY}" events-count --sender "@ruriko:localhost" --contains "${contains}")"
		if (( count > 0 )); then
			printf '%s' "${token}"
			return 0
		fi
	done
	return 1
}

issue_kuze_secret() {
	local secret_ref="$1"
	local secret_type="$2"
	local secret_value="$3"
	local sync_token="$4"

	send_matrix "/ruriko secrets set ${secret_ref} --type ${secret_type}" "canonical-secret-${secret_ref}-$(date +%s)"

	local deadline=$(( $(date +%s) + 90 ))
	while [[ $(date +%s) -lt ${deadline} ]]; do
		local payload link next_token
		payload="$(python3 "${PROBE_PY}" sync \
			--base-url "${MATRIX_BASE_URL}" \
			--token "${OPERATOR_TOKEN}" \
			--since "${sync_token}" \
			--timeout-ms "${SYNC_TIMEOUT_MS}" \
			--rooms "${ADMIN_ROOM}" \
			--senders "@ruriko:localhost")"
		next_token="$(printf '%s' "${payload}" | python3 "${PROBE_PY}" next-batch)"
		[[ -n "${next_token}" ]] && sync_token="${next_token}"
		link="$(extract_kuze_link "${payload}")"
		if [[ -n "${link}" ]]; then
			# Kuze links may use an internal container hostname (e.g. http://ruriko:8080).
			# For host-side test submission, retry with a localhost URL when needed.
			if curl -fsS -X POST "${link}" \
				-H "Content-Type: application/x-www-form-urlencoded" \
				--data-urlencode "secret_value=${secret_value}" >/dev/null 2>&1; then
				:
			else
				# Rewrite any scheme/host to localhost while preserving path/query.
				link_host_fallback="$(printf '%s' "${link}" | sed -E "s#^https?://[^/]+#http://127.0.0.1:${HTTP_ADDR_PORT:-8080}#")"
				if ! curl -fsS -X POST "${link_host_fallback}" \
					-H "Content-Type: application/x-www-form-urlencoded" \
					--data-urlencode "secret_value=${secret_value}" >/dev/null 2>&1; then
					fail "failed to submit Kuze secret value for ${secret_ref} via both original and fallback URLs"
				fi
			fi
			printf '%s' "${sync_token}"
			return 0
		fi
	done

	fail "timed out waiting for Kuze link for ${secret_ref}"
}

wait_for_container() {
	local name="$1"
	local timeout="$2"
	local deadline=$(( $(date +%s) + timeout ))
	while [[ $(date +%s) -lt ${deadline} ]]; do
		if docker ps --format '{{.Names}}' | grep -q "^${name}$"; then
			return 0
		fi
		sleep 1
	done
	return 1
}

wait_for_log_match() {
	local container="$1"
	local pattern="$2"
	local timeout="$3"
	local deadline=$(( $(date +%s) + timeout ))
	while [[ $(date +%s) -lt ${deadline} ]]; do
		if docker logs --since 5m "${container}" 2>&1 | grep -E "${pattern}" >/dev/null 2>&1; then
			return 0
		fi
		sleep "${POLL_SECONDS}"
	done
	return 1
}

count_openai_calls() {
	python3 - "${OPENAI_CAPTURE}" <<'PY'
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
if not path.exists():
    print(0)
else:
    lines = [ln for ln in path.read_text(encoding='utf-8').splitlines() if ln.strip()]
    print(len(lines))
PY
}

info "Step 1/10: starting OpenAI capture proxy (mode=${OPENAI_MODE})"
python3 "${OPENAI_PROXY_SCRIPT}" \
	--host "${OPENAI_PROXY_HOST}" \
	--port "${OPENAI_PROXY_PORT}" \
	--mode "${OPENAI_MODE}" \
	--upstream-base "${OPENAI_UPSTREAM_BASE}" \
	--upstream-timeout "${OPENAI_UPSTREAM_TIMEOUT}" \
	--capture-file "${OPENAI_CAPTURE}" >"${OPENAI_PROXY_LOG}" 2>&1 &
OPENAI_PROXY_PID=$!

if ! wait_for_http "http://127.0.0.1:${OPENAI_PROXY_PORT}/health" 20; then
	fail "OpenAI proxy did not become healthy; inspect ${OPENAI_PROXY_LOG}"
fi
pass "OpenAI capture proxy ready"

info "Step 2/10: provisioning a fresh stack"
bash "${PROVISION_SCRIPT}"
[[ -f "${TOKENS_FILE}" ]] || fail "tokens file not found after provisioning: ${TOKENS_FILE}"
source "${TOKENS_FILE}"
[[ -n "${OPERATOR_TOKEN:-}" ]] || fail "OPERATOR_TOKEN missing in ${TOKENS_FILE}"
[[ -n "${ADMIN_ROOM:-}" ]] || fail "ADMIN_ROOM missing in ${TOKENS_FILE}"
[[ -n "${USER_ROOM:-}" ]] || fail "USER_ROOM missing in ${TOKENS_FILE}"

MATRIX_BASE_URL="$(python3 "${HELPER_PY}" matrix-base-url --env-file "${COMPOSE_ENV_FILE}")"
[[ -n "${MATRIX_BASE_URL}" ]] || fail "failed to discover Matrix base URL"

source "${COMPOSE_ENV_FILE}"
AGENT_IMAGE="${DEFAULT_AGENT_IMAGE:-gitai:latest}"

info "Step 3/10: starting compose stack with OpenAI proxy routing"
export NLP_ENDPOINT="http://${DOCKER_BRIDGE_HOST}:${OPENAI_PROXY_PORT}/v1"
export RURIKO_NLP_API_KEY="${OPENAI_API_KEY}"
export LLM_BASE_URL="http://${DOCKER_BRIDGE_HOST}:${OPENAI_PROXY_PORT}/v1"
export LLM_API_KEY=""
export GLOBAL_LLM_API_KEY=""
# Ensure Kuze redemption URLs are reachable from agent containers.
export KUZE_BASE_URL="${KUZE_INTERNAL_BASE_URL}"
(
	cd "${COMPOSE_DIR}"
	docker_compose up -d
)

if ! wait_for_http "http://127.0.0.1:${HTTP_ADDR_PORT:-8080}/health" 120; then
	fail "Ruriko health endpoint did not become reachable"
fi
pass "Ruriko and Tuwunel are up"

info "Step 4/10: sending canonical natural-language request"
SYNC_TOKEN="$(python3 "${PROBE_PY}" sync --base-url "${MATRIX_BASE_URL}" --token "${OPERATOR_TOKEN}" --since "" --timeout-ms 0 --rooms "${ADMIN_ROOM}" | python3 "${PROBE_PY}" next-batch)"
send_matrix "${NL_REQUEST_TEXT}" "canonical-nl-$(date +%s)"
NL_SYNC_TOKEN="$(wait_for_ruriko_text "Saito" 45 "${SYNC_TOKEN}" 2>/dev/null || true)"
if [[ -n "${NL_SYNC_TOKEN}" ]]; then
	SYNC_TOKEN="${NL_SYNC_TOKEN}"
	pass "Observed Ruriko NL response mentioning Saito"
else
	info "No explicit 'Saito' reply observed for NL request; continuing with deterministic command path"
fi

info "Step 5/10: setting Kuze secrets for Kumo"
SYNC_TOKEN="$(issue_kuze_secret "kumo.openai-api-key" "api_key" "${OPENAI_API_KEY}" "${SYNC_TOKEN}")"
SYNC_TOKEN="$(issue_kuze_secret "kumo.brave-api-key" "api_key" "${BRAVE_API_KEY}" "${SYNC_TOKEN}")"
pass "Kuze secret entry completed for kumo.openai-api-key and kumo.brave-api-key"

info "Step 6/10: creating Kumo (trusted peer: Saito) and Saito"
send_matrix "/ruriko agents create --name kumo --template kumo-agent --image ${AGENT_IMAGE} --peer-alias saito --peer-mxid @saito:localhost --peer-room ${ADMIN_ROOM} --peer-protocol-id saito.news.request.v1 --peer-protocol-prefix ${KUMO_REQUEST_PREFIX}" "canonical-create-kumo-$(date +%s)"
if ! wait_for_container "ruriko-agent-kumo" 180; then
	docker logs --tail 120 ruriko 2>&1 || true
	fail "kumo container was not created"
fi

send_matrix "/ruriko agents create --name saito --template saito-agent --image ${AGENT_IMAGE} --peer-alias kumo --peer-mxid @kumo:localhost --peer-room ${ADMIN_ROOM} --peer-protocol-id saito.news.request.v1 --peer-protocol-prefix ${KUMO_REQUEST_PREFIX}" "canonical-create-saito-$(date +%s)"
if ! wait_for_container "ruriko-agent-saito" 180; then
	docker logs --tail 120 ruriko 2>&1 || true
	fail "saito container was not created"
fi
pass "Saito and Kumo containers are running"

if ! wait_for_log_match "ruriko-agent-kumo" "gosuto config applied|ACP: config applied" 120; then
	docker logs --since 10m ruriko-agent-kumo 2>&1 | tail -n 120 || true
	fail "kumo did not report gosuto apply"
fi
if ! wait_for_log_match "ruriko-agent-saito" "gosuto config applied|ACP: config applied" 120; then
	docker logs --since 10m ruriko-agent-saito 2>&1 | tail -n 120 || true
	fail "saito did not report gosuto apply"
fi

info "Step 7/10: binding and pushing Kumo secrets"
send_matrix "/ruriko secrets bind kumo kumo.openai-api-key --scope read" "canonical-bind-openai-$(date +%s)"
if SYNC_TOKEN="$(wait_for_ruriko_text "granted access" 60 "${SYNC_TOKEN}" 2>/dev/null || true)" && [[ -n "${SYNC_TOKEN}" ]]; then
	:
else
	ERROR_TOKEN="$(wait_for_ruriko_text "❌ Error" 10 "${SYNC_TOKEN}" 2>/dev/null || true)"
	if [[ -n "${ERROR_TOKEN}" ]]; then
		SYNC_TOKEN="${ERROR_TOKEN}"
		fail "Ruriko reported an error while binding kumo.openai-api-key"
	fi
	info "no bind ack observed for kumo.openai-api-key; proceeding"
fi

send_matrix "/ruriko secrets bind kumo kumo.brave-api-key --scope read" "canonical-bind-brave-$(date +%s)"
if SYNC_TOKEN="$(wait_for_ruriko_text "granted access" 60 "${SYNC_TOKEN}" 2>/dev/null || true)" && [[ -n "${SYNC_TOKEN}" ]]; then
	:
else
	ERROR_TOKEN="$(wait_for_ruriko_text "❌ Error" 10 "${SYNC_TOKEN}" 2>/dev/null || true)"
	if [[ -n "${ERROR_TOKEN}" ]]; then
		SYNC_TOKEN="${ERROR_TOKEN}"
		fail "Ruriko reported an error while binding kumo.brave-api-key"
	fi
	info "no bind ack observed for kumo.brave-api-key; proceeding"
fi

send_matrix "/ruriko secrets push kumo" "canonical-push-kumo-$(date +%s)"
if ! SYNC_TOKEN="$(wait_for_ruriko_text "Pushed" 120 "${SYNC_TOKEN}" 2>/dev/null || true)" || [[ -z "${SYNC_TOKEN}" ]]; then
	ERROR_TOKEN="$(wait_for_ruriko_text "❌ Error" 10 "${SYNC_TOKEN}" 2>/dev/null || true)"
	if [[ -n "${ERROR_TOKEN}" ]]; then
		SYNC_TOKEN="${ERROR_TOKEN}"
		docker logs --since 10m ruriko 2>&1 | tail -n 120 || true
		fail "Ruriko reported an error while pushing secrets to kumo"
	fi
	docker logs --since 10m ruriko 2>&1 | grep -E "secrets.push|secrets token|kuze|error" | tail -n 120 || true
	fail "no secrets push acknowledgement for kumo"
fi
pass "Kumo secrets pushed"

info "Step 8/10: scheduling fast Saito cron ticks toward Kumo path"
CANONICAL_REQUEST_JSON="$(python3 - "${KUMO_REQUEST_BODY}" <<'PY'
import json
import sys

raw = sys.argv[1]
try:
	parsed = json.loads(raw)
except Exception as exc:
	# Support escaped JSON payloads such as {\"run_id\":1}.
	if '\\"' not in raw:
		raise SystemExit(f"invalid CANONICAL_KUMO_REQUEST_BODY JSON: {exc}")
	try:
		parsed = json.loads(raw.replace('\\"', '"'))
	except Exception as exc2:
		raise SystemExit(f"invalid CANONICAL_KUMO_REQUEST_BODY JSON: {exc2}")
print(json.dumps(parsed, separators=(",", ":"), ensure_ascii=False))
PY
)"
REQUEST_MESSAGE="@kumo, ${KUMO_REQUEST_PREFIX} ${CANONICAL_REQUEST_JSON}"
send_matrix "/ruriko schedule upsert --agent saito --cron \"${SAITO_CRON_EXPR}\" --target kumo --message '${REQUEST_MESSAGE}'" "canonical-schedule-$(date +%s)"
if SYNC_TOKEN="$(wait_for_ruriko_text "Created schedule" 90 "${SYNC_TOKEN}" 2>/dev/null || wait_for_ruriko_text "Updated schedule" 90 "${SYNC_TOKEN}" 2>/dev/null || true)" && [[ -n "${SYNC_TOKEN}" ]]; then
	pass "Saito schedule applied"
else
	ERROR_TOKEN="$(wait_for_ruriko_text "❌ Error" 10 "${SYNC_TOKEN}" 2>/dev/null || true)"
	if [[ -n "${ERROR_TOKEN}" ]]; then
		SYNC_TOKEN="${ERROR_TOKEN}"
		fail "Ruriko reported an error while creating/updating the Saito schedule"
	fi
	info "no schedule ack observed; continuing and validating via workflow events"
fi

info "Step 9/10: waiting for ${REQUIRED_CYCLES} cycle(s) of Saito->Kumo->operator"
START_EPOCH="$(date +%s)"
SAITO_COUNT=0
KUMO_SUMMARY_COUNT=0
while true; do
	NOW_EPOCH="$(date +%s)"
	ELAPSED=$((NOW_EPOCH - START_EPOCH))
	if (( ELAPSED > TIMEOUT_SECONDS )); then
		docker logs --since 10m ruriko-agent-saito 2>&1 | tail -n 120 || true
		docker logs --since 10m ruriko-agent-kumo 2>&1 | tail -n 180 || true
		fail "timed out after ${TIMEOUT_SECONDS}s waiting for canonical Saito->Kumo flow"
	fi

	PAYLOAD="$(python3 "${PROBE_PY}" sync \
		--base-url "${MATRIX_BASE_URL}" \
		--token "${OPERATOR_TOKEN}" \
		--since "${SYNC_TOKEN}" \
		--timeout-ms "${SYNC_TIMEOUT_MS}" \
		--rooms "${ADMIN_ROOM},${USER_ROOM}")"
	NEXT_TOKEN="$(printf '%s' "${PAYLOAD}" | python3 "${PROBE_PY}" next-batch)"
	[[ -n "${NEXT_TOKEN}" ]] && SYNC_TOKEN="${NEXT_TOKEN}"

	DELTA_SAITO="$(printf '%s' "${PAYLOAD}" | python3 "${PROBE_PY}" events-count --sender "@saito:localhost" --contains "${KUMO_REQUEST_PREFIX}")"
	DELTA_KUMO="$(printf '%s' "${PAYLOAD}" | python3 "${PROBE_PY}" events-count --sender "@kumo:localhost" --contains "KUMO_NEWS_RESPONSE")"
	# Some runtime paths emit structured JSON directly without protocol prefixes.
	# Accept these as fallback evidence of the Saito<->Kumo exchange.
	DELTA_SAITO_JSON="$(printf '%s' "${PAYLOAD}" | python3 "${PROBE_PY}" events-count --sender "@saito:localhost" --contains '"run_id"')"
	DELTA_KUMO_JSON="$(printf '%s' "${PAYLOAD}" | python3 "${PROBE_PY}" events-count --sender "@kumo:localhost" --contains '"run_id"')"
	if (( DELTA_SAITO == 0 )); then
		DELTA_SAITO="${DELTA_SAITO_JSON}"
	fi
	if (( DELTA_KUMO == 0 )); then
		DELTA_KUMO="${DELTA_KUMO_JSON}"
	fi
	SAITO_COUNT=$((SAITO_COUNT + DELTA_SAITO))
	KUMO_SUMMARY_COUNT=$((KUMO_SUMMARY_COUNT + DELTA_KUMO))

	info "progress elapsed=${ELAPSED}s saito_requests=${SAITO_COUNT} kumo_responses=${KUMO_SUMMARY_COUNT}"
	if (( SAITO_COUNT >= REQUIRED_CYCLES && KUMO_SUMMARY_COUNT >= REQUIRED_CYCLES )); then
		break
	fi
	sleep "${POLL_SECONDS}"
done
pass "Observed required Saito->Kumo canonical workflow cycles"

info "Step 10/10: validating OpenAI proxy capture evidence"
OPENAI_CALLS="$(count_openai_calls)"
if (( OPENAI_CALLS < OPENAI_EXPECT_MIN_CALLS )); then
	fail "insufficient OpenAI calls observed via proxy: calls=${OPENAI_CALLS} min_expected=${OPENAI_EXPECT_MIN_CALLS}"
fi
pass "OpenAI proxy captured ${OPENAI_CALLS} call(s) (mode=${OPENAI_MODE})"

info "OpenAI capture file: ${OPENAI_CAPTURE}"
info "OpenAI proxy log: ${OPENAI_PROXY_LOG}"
pass "Canonical Saito->Kumo live compose workflow passed"

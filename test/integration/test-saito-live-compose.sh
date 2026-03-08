#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${SAITO_LIVE_COMPOSE_FILE:-$ROOT_DIR/test/integration/docker-compose.saito-live.yaml}"
WORK_DIR="${SAITO_LIVE_WORK_DIR:-$ROOT_DIR/test/integration/.saito-live}"
CREDS_FILE="${SAITO_LIVE_CREDS_FILE:-$WORK_DIR/matrix-creds.env}"
SAITO_GOSUTO_FILE="$WORK_DIR/saito-gosuto.yaml"
MATRIX_HELPER="$ROOT_DIR/test/integration/kumo_live_matrix_helpers.py"
VERIFY_HELPER="$ROOT_DIR/test/integration/saito_live_matrix_verify.py"

SAITO_AGENT_USERNAME="${SAITO_LIVE_AGENT_USERNAME:-saito_live}"
SAITO_AGENT_PASSWORD="${SAITO_LIVE_AGENT_PASSWORD:-saito-live-password}"
OPERATOR_USERNAME="${SAITO_LIVE_OPERATOR_USERNAME:-operator_live}"
OPERATOR_PASSWORD="${SAITO_LIVE_OPERATOR_PASSWORD:-operator-live-password}"
REG_TOKEN="${SAITO_LIVE_TUWUNEL_REGISTRATION_TOKEN:-saito-live-registration-token}"
MATRIX_BASE_URL="${SAITO_LIVE_MATRIX_BASE_URL:-http://127.0.0.1:${SAITO_LIVE_TUWUNEL_PORT:-18018}}"
ACP_BASE_URL="${SAITO_LIVE_ACP_BASE_URL:-http://127.0.0.1:${SAITO_LIVE_ACP_PORT:-18765}}"
ACP_TOKEN="${SAITO_LIVE_ACP_TOKEN:-saito-live-acp-token}"

REQUEST_TIMEOUT="${SAITO_LIVE_REQUEST_TIMEOUT_SECONDS:-180}"
POLL_SECONDS="${SAITO_LIVE_POLL_SECONDS:-4}"
SYNC_TIMEOUT_MS="${SAITO_LIVE_SYNC_TIMEOUT_MS:-10000}"
CRON_EXPRESSION="${SAITO_LIVE_CRON_EXPRESSION:-@every 15s}"
TARGET_ALIAS="${SAITO_LIVE_TARGET_ALIAS:-kairo}"
CRON_MESSAGE_PREFIX="${SAITO_LIVE_CRON_MESSAGE_PREFIX:-SAITO_LIVE_TICK}"
REQUIRED_DELIVERIES="${SAITO_LIVE_REQUIRED_DELIVERIES:-1}"
KEEP_STACK="${SAITO_LIVE_KEEP_STACK:-0}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${YELLOW}[INFO]${NC} $*"; }
pass() { echo -e "${GREEN}[PASS]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

docker_compose() {
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose -f "$COMPOSE_FILE" "$@"
  else
    docker compose -f "$COMPOSE_FILE" "$@"
  fi
}

cleanup() {
  if [[ "$KEEP_STACK" == "1" ]]; then
    info "SAITO_LIVE_KEEP_STACK=1 set; leaving compose resources running"
    return
  fi
  info "Stopping Saito live compose stack"
  docker_compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_for_http() {
  local url="$1"
  local timeout="$2"
  local deadline=$(( $(date +%s) + timeout ))
  while true; do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    if [[ $(date +%s) -ge "$deadline" ]]; then
      return 1
    fi
    sleep 2
  done
}

wait_for_log_match() {
  local container="$1"
  local pattern="$2"
  local timeout="$3"
  local deadline=$(( $(date +%s) + timeout ))
  while [[ $(date +%s) -lt "$deadline" ]]; do
    if docker logs --since 5m "$container" 2>&1 | grep -E "$pattern" >/dev/null 2>&1; then
      return 0
    fi
    sleep "$POLL_SECONDS"
  done
  return 1
}

require_cmd docker
require_cmd curl
require_cmd python3
require_cmd sed

mkdir -p "$WORK_DIR"

[[ -f "$COMPOSE_FILE" ]] || fail "compose file not found: $COMPOSE_FILE"
[[ -f "$MATRIX_HELPER" ]] || fail "matrix helper not found: $MATRIX_HELPER"
[[ -f "$VERIFY_HELPER" ]] || fail "verify helper not found: $VERIFY_HELPER"

info "Starting Saito live stack (tuwunel only)"
docker_compose up -d tuwunel

if ! wait_for_http "$MATRIX_BASE_URL/_matrix/client/versions" 60; then
  fail "Matrix homeserver did not become reachable at $MATRIX_BASE_URL"
fi
pass "Matrix homeserver is reachable"

info "Registering Matrix users (operator + saito)"
operator_json="$(python3 "$MATRIX_HELPER" register --base-url "$MATRIX_BASE_URL" --username "$OPERATOR_USERNAME" --password "$OPERATOR_PASSWORD" --registration-token "$REG_TOKEN")" || fail "operator registration failed"
saito_json="$(python3 "$MATRIX_HELPER" register --base-url "$MATRIX_BASE_URL" --username "$SAITO_AGENT_USERNAME" --password "$SAITO_AGENT_PASSWORD" --registration-token "$REG_TOKEN")" || fail "saito registration failed"

OPERATOR_TOKEN="$(python3 - "$operator_json" <<'PY'
import json,sys
print(json.loads(sys.argv[1])["access_token"])
PY
)"
OPERATOR_MXID="$(python3 - "$operator_json" <<'PY'
import json,sys
print(json.loads(sys.argv[1])["user_id"])
PY
)"
SAITO_TOKEN="$(python3 - "$saito_json" <<'PY'
import json,sys
print(json.loads(sys.argv[1])["access_token"])
PY
)"
SAITO_MXID="$(python3 - "$saito_json" <<'PY'
import json,sys
print(json.loads(sys.argv[1])["user_id"])
PY
)"

cat >"$CREDS_FILE" <<EOF
SAITO_LIVE_OPERATOR_MXID=$OPERATOR_MXID
SAITO_LIVE_OPERATOR_TOKEN=$OPERATOR_TOKEN
SAITO_LIVE_SAITO_MXID=$SAITO_MXID
SAITO_LIVE_SAITO_TOKEN=$SAITO_TOKEN
SAITO_LIVE_MATRIX_BASE_URL=$MATRIX_BASE_URL
EOF
pass "Saved generated Matrix credentials to $CREDS_FILE"

ROOM_ID="$(python3 "$MATRIX_HELPER" create-room --base-url "$MATRIX_BASE_URL" --token "$OPERATOR_TOKEN" --name "saito-live-room" --invite "$SAITO_MXID")" || fail "room creation failed"
python3 "$MATRIX_HELPER" join --base-url "$MATRIX_BASE_URL" --token "$SAITO_TOKEN" --room-id "$ROOM_ID" >/dev/null || fail "saito could not join room"
USER_ROOM_ID="$(python3 "$MATRIX_HELPER" create-room --base-url "$MATRIX_BASE_URL" --token "$OPERATOR_TOKEN" --name "saito-live-user-room")" || fail "user room creation failed"
pass "Created rooms and joined Saito (peer_room=$ROOM_ID user_room=$USER_ROOM_ID)"

info "Rendering Saito Gosuto for standalone live test"
sed \
  -e "s|{{.AgentName}}|saito|g" \
  -e "s|{{.AdminRoom}}|$ROOM_ID|g" \
  -e "s|{{.KairoAdminRoom}}|$ROOM_ID|g" \
  -e "s|{{.UserRoom}}|$USER_ROOM_ID|g" \
  -e "s|{{.OperatorMXID}}|$OPERATOR_MXID|g" \
  "$ROOT_DIR/templates/saito-agent/gosuto.yaml" > "$SAITO_GOSUTO_FILE"

# Keep the default static expression inert and drive runtime behavior via ACP schedule.upsert.
sed -i -e 's|expression: "\*/15 \* \* \* \*"|expression: "0 0 31 2 *"|g' "$SAITO_GOSUTO_FILE"

pass "Rendered gosuto file: $SAITO_GOSUTO_FILE"

info "Starting standalone Saito container"
export SAITO_LIVE_GOSUTO_FILE="$SAITO_GOSUTO_FILE"
export SAITO_LIVE_SAITO_MXID="$SAITO_MXID"
export SAITO_LIVE_SAITO_TOKEN="$SAITO_TOKEN"

docker_compose up -d saito

if ! wait_for_log_match "saito-live-saito" "gosuto config applied" 120; then
  info "Recent Saito logs:"
  docker logs --since 5m saito-live-saito 2>&1 | tail -n 120 || true
  fail "Saito did not apply Gosuto config"
fi
pass "Saito container started and Gosuto config is applied"

acp_deadline=$(( $(date +%s) + 30 ))
while true; do
  if curl -fsS "$ACP_BASE_URL/health" -H "Authorization: Bearer ${ACP_TOKEN}" >/dev/null 2>&1; then
    break
  fi
  if [[ $(date +%s) -ge "$acp_deadline" ]]; then
    fail "Saito ACP endpoint did not become reachable at $ACP_BASE_URL"
  fi
  sleep 2
done
pass "Saito ACP endpoint is reachable"

RUN_ID="$(date +%s)"
CRON_MESSAGE="${CRON_MESSAGE_PREFIX} ${RUN_ID}"

info "Calling ACP /tools/call schedule.upsert to configure cron"
apply_payload="$(python3 - "$OPERATOR_MXID" "$CRON_EXPRESSION" "$TARGET_ALIAS" "$CRON_MESSAGE" <<'PY'
import json,sys
print(json.dumps({
    "tool_ref": "schedule.upsert",
  "sender": sys.argv[1],
    "args": {
        "gateway": "scheduler",
    "cron_expression": sys.argv[2],
    "target_alias": sys.argv[3],
    "message": sys.argv[4],
        "enabled": True,
    },
}, ensure_ascii=True))
PY
)"

schedule_http_file="$WORK_DIR/acp-schedule-response.json"
schedule_status="$(curl -sS -o "$schedule_http_file" -w "%{http_code}" \
  -X POST "$ACP_BASE_URL/tools/call" \
  -H "Authorization: Bearer ${ACP_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "$apply_payload")" || fail "ACP schedule.upsert call failed"

schedule_resp="$(cat "$schedule_http_file")"
if [[ "$schedule_status" != "200" ]]; then
  fail "ACP schedule.upsert returned HTTP ${schedule_status}: $schedule_resp"
fi

if ! printf '%s' "$schedule_resp" | grep -E 'Created schedule|Updated schedule' >/dev/null 2>&1; then
  fail "unexpected ACP schedule.upsert response: $schedule_resp"
fi
pass "ACP schedule.upsert accepted: $schedule_resp"

info "Waiting for cron delivery with Python verifier"
if ! python3 "$VERIFY_HELPER" wait-count \
  --base-url "$MATRIX_BASE_URL" \
  --token "$OPERATOR_TOKEN" \
  --room "$ROOM_ID" \
  --sender "$SAITO_MXID" \
  --contains "$CRON_MESSAGE_PREFIX" \
  --count "$REQUIRED_DELIVERIES" \
  --wait-seconds "$REQUEST_TIMEOUT" \
  --poll-seconds "$POLL_SECONDS" \
  --sync-timeout-ms "$SYNC_TIMEOUT_MS"; then
  info "Recent Saito logs:"
  docker logs --since 8m saito-live-saito 2>&1 | tail -n 200 || true
  fail "did not observe ${REQUIRED_DELIVERIES} Saito cron-delivered message(s) within ${REQUEST_TIMEOUT}s"
fi

pass "Observed ${REQUIRED_DELIVERIES} Saito cron-delivered message(s) in Matrix room"
info "Generated Matrix credentials file: $CREDS_FILE"
pass "Saito standalone live compose test completed"

#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${KUMO_LIVE_COMPOSE_FILE:-$ROOT_DIR/test/integration/docker-compose.kumo-live.yaml}"
WORK_DIR="${KUMO_LIVE_WORK_DIR:-$ROOT_DIR/test/integration/.kumo-live}"
OPENAI_CAPTURE="${KUMO_LIVE_OPENAI_CAPTURE_FILE:-$WORK_DIR/openai-calls.jsonl}"
OPENAI_PROXY_LOG="${KUMO_LIVE_OPENAI_PROXY_LOG:-$WORK_DIR/openai-proxy.log}"
CREDS_FILE="${KUMO_LIVE_CREDS_FILE:-$WORK_DIR/matrix-creds.env}"
KUMO_GOSUTO_FILE="$WORK_DIR/kumo-gosuto.yaml"
MATRIX_HELPER="$ROOT_DIR/test/integration/kumo_live_matrix_helpers.py"
PROBE_HELPER="$ROOT_DIR/test/integration/ruriko_saito_live_matrix_probe.py"
OPENAI_PROXY="$ROOT_DIR/test/integration/openai_capture_proxy.py"

KUMO_AGENT_USERNAME="${KUMO_LIVE_AGENT_USERNAME:-kumo_live}"
KUMO_AGENT_PASSWORD="${KUMO_LIVE_AGENT_PASSWORD:-kumo-live-password}"
OPERATOR_USERNAME="${KUMO_LIVE_OPERATOR_USERNAME:-operator_live}"
OPERATOR_PASSWORD="${KUMO_LIVE_OPERATOR_PASSWORD:-operator-live-password}"
REG_TOKEN="${KUMO_LIVE_TUWUNEL_REGISTRATION_TOKEN:-kumo-live-registration-token}"
MATRIX_BASE_URL="${KUMO_LIVE_MATRIX_BASE_URL:-http://127.0.0.1:${KUMO_LIVE_TUWUNEL_PORT:-18008}}"

OPENAI_MODE="${KUMO_LIVE_OPENAI_MODE:-stub}"
OPENAI_PROXY_HOST="${KUMO_LIVE_OPENAI_PROXY_HOST:-0.0.0.0}"
OPENAI_PROXY_PORT="${KUMO_LIVE_OPENAI_PROXY_PORT:-18081}"
OPENAI_UPSTREAM_BASE="${KUMO_LIVE_OPENAI_UPSTREAM_BASE:-https://api.openai.com}"
OPENAI_API_KEY="${KUMO_LIVE_OPENAI_API_KEY:-dummy-live-key}"
BRAVE_API_KEY="${KUMO_LIVE_BRAVE_API_KEY:-}"
BRAVE_TOOL_NAME="${KUMO_LIVE_BRAVE_TOOL_NAME:-brave_web_search}"
BRAVE_PROXY_URL="${KUMO_LIVE_BRAVE_PROXY_URL:-}"
DEBUG="${KUMO_LIVE_DEBUG:-0}"

REQUEST_TIMEOUT="${KUMO_LIVE_REQUEST_TIMEOUT_SECONDS:-180}"
POLL_SECONDS="${KUMO_LIVE_POLL_SECONDS:-4}"
OPENAI_WAIT_TIMEOUT="${KUMO_LIVE_OPENAI_WAIT_TIMEOUT_SECONDS:-45}"
BRAVE_WAIT_TIMEOUT="${KUMO_LIVE_BRAVE_WAIT_TIMEOUT_SECONDS:-45}"
SUMMARY_WAIT_TIMEOUT="${KUMO_LIVE_SUMMARY_WAIT_TIMEOUT_SECONDS:-${REQUEST_TIMEOUT}}"
REQUIRE_SUMMARY="${KUMO_LIVE_REQUIRE_SUMMARY:-0}"
KEEP_STACK="${KUMO_LIVE_KEEP_STACK:-0}"
OPENAI_UPSTREAM_TIMEOUT="${KUMO_LIVE_OPENAI_UPSTREAM_TIMEOUT_SECONDS:-20}"

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
  if [[ -n "${DEBUG_OPENAI_LOG_PID:-}" ]]; then
    kill "$DEBUG_OPENAI_LOG_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "${DEBUG_KUMO_LOG_PID:-}" ]]; then
    kill "$DEBUG_KUMO_LOG_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "${OPENAI_PROXY_PID:-}" ]]; then
    kill "$OPENAI_PROXY_PID" >/dev/null 2>&1 || true
  fi
  if [[ "$KEEP_STACK" == "1" ]]; then
    info "KUMO_LIVE_KEEP_STACK=1 set; leaving compose resources running"
    return
  fi
  info "Stopping Kumo live compose stack"
  docker_compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

start_debug_streams() {
  if [[ "$DEBUG" != "1" ]]; then
    return
  fi

  info "KUMO_LIVE_DEBUG=1: streaming OpenAI proxy and Kumo container logs"
  tail -n 0 -f "$OPENAI_PROXY_LOG" | sed -u 's/^/[openai-proxy] /' &
  DEBUG_OPENAI_LOG_PID=$!

  docker logs -f kumo-live-kumo 2>&1 | sed -u 's/^/[kumo] /' &
  DEBUG_KUMO_LOG_PID=$!
}

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
: > "$OPENAI_CAPTURE"
: > "$OPENAI_PROXY_LOG"

[[ -f "$COMPOSE_FILE" ]] || fail "compose file not found: $COMPOSE_FILE"
[[ -f "$MATRIX_HELPER" ]] || fail "matrix helper not found: $MATRIX_HELPER"
[[ -f "$PROBE_HELPER" ]] || fail "probe helper not found: $PROBE_HELPER"
[[ -f "$OPENAI_PROXY" ]] || fail "openai proxy helper not found: $OPENAI_PROXY"

if [[ "$OPENAI_MODE" == "passthrough" ]] && [[ -z "${OPENAI_API_KEY}" || "${OPENAI_API_KEY}" == "dummy-live-key" ]]; then
  fail "KUMO_LIVE_OPENAI_MODE=passthrough requires a real KUMO_LIVE_OPENAI_API_KEY; use stub mode or set the key"
fi

if [[ "$REQUIRE_SUMMARY" == "1" ]] && [[ -z "${BRAVE_API_KEY}" ]]; then
  fail "KUMO_LIVE_REQUIRE_SUMMARY=1 requires KUMO_LIVE_BRAVE_API_KEY; otherwise workflow cannot complete the search+summary chain"
fi

info "Starting OpenAI capture proxy (mode=$OPENAI_MODE, port=$OPENAI_PROXY_PORT)"
python3 "$OPENAI_PROXY" \
  --host "$OPENAI_PROXY_HOST" \
  --port "$OPENAI_PROXY_PORT" \
  --mode "$OPENAI_MODE" \
  --upstream-base "$OPENAI_UPSTREAM_BASE" \
  --upstream-timeout "$OPENAI_UPSTREAM_TIMEOUT" \
  --capture-file "$OPENAI_CAPTURE" >"$OPENAI_PROXY_LOG" 2>&1 &
OPENAI_PROXY_PID=$!

if ! wait_for_http "http://127.0.0.1:${OPENAI_PROXY_PORT}/health" 20; then
  fail "OpenAI proxy did not become healthy; inspect $OPENAI_PROXY_LOG"
fi
pass "OpenAI capture proxy is ready"

info "Starting Kumo live stack (tuwunel only)"
docker_compose up -d tuwunel

if ! wait_for_http "$MATRIX_BASE_URL/_matrix/client/versions" 60; then
  fail "Matrix homeserver did not become reachable at $MATRIX_BASE_URL"
fi
pass "Matrix homeserver is reachable"

info "Registering Matrix users (operator + kumo)"
operator_json="$(python3 "$MATRIX_HELPER" register --base-url "$MATRIX_BASE_URL" --username "$OPERATOR_USERNAME" --password "$OPERATOR_PASSWORD" --registration-token "$REG_TOKEN")" || fail "operator registration failed"
kumo_json="$(python3 "$MATRIX_HELPER" register --base-url "$MATRIX_BASE_URL" --username "$KUMO_AGENT_USERNAME" --password "$KUMO_AGENT_PASSWORD" --registration-token "$REG_TOKEN")" || fail "kumo registration failed"

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
KUMO_TOKEN="$(python3 - "$kumo_json" <<'PY'
import json,sys
print(json.loads(sys.argv[1])["access_token"])
PY
)"
KUMO_MXID="$(python3 - "$kumo_json" <<'PY'
import json,sys
print(json.loads(sys.argv[1])["user_id"])
PY
)"

cat >"$CREDS_FILE" <<EOF
KUMO_LIVE_OPERATOR_MXID=$OPERATOR_MXID
KUMO_LIVE_OPERATOR_TOKEN=$OPERATOR_TOKEN
KUMO_LIVE_KUMO_MXID=$KUMO_MXID
KUMO_LIVE_KUMO_TOKEN=$KUMO_TOKEN
KUMO_LIVE_MATRIX_BASE_URL=$MATRIX_BASE_URL
EOF
pass "Saved generated Matrix credentials to $CREDS_FILE"

ROOM_ID="$(python3 "$MATRIX_HELPER" create-room --base-url "$MATRIX_BASE_URL" --token "$OPERATOR_TOKEN" --name "kumo-live-room" --invite "$KUMO_MXID")" || fail "room creation failed"
python3 "$MATRIX_HELPER" join --base-url "$MATRIX_BASE_URL" --token "$KUMO_TOKEN" --room-id "$ROOM_ID" >/dev/null || fail "kumo could not join room"

# Use a distinct user room so rendered messaging.allowedTargets has unique room IDs.
USER_ROOM_ID="$(python3 "$MATRIX_HELPER" create-room --base-url "$MATRIX_BASE_URL" --token "$OPERATOR_TOKEN" --name "kumo-live-user-room")" || fail "user room creation failed"

pass "Created rooms and joined Kumo (peer_room=$ROOM_ID user_room=$USER_ROOM_ID)"

info "Rendering Kumo Gosuto for standalone live test"
sed \
  -e "s|{{.AgentName}}|kumo|g" \
  -e "s|{{.AdminRoom}}|$ROOM_ID|g" \
  -e "s|{{.PeerMXID}}|$OPERATOR_MXID|g" \
  -e "s|{{.PeerRoom}}|$ROOM_ID|g" \
  -e "s|{{.PeerAlias}}|operator|g" \
  -e "s|{{.PeerProtocolID}}|operator.news.request.v1|g" \
  -e "s|{{.PeerProtocolPrefix}}|KUMO_NEWS_REQUEST|g" \
  -e "s|{{.UserRoom}}|$USER_ROOM_ID|g" \
  "$ROOT_DIR/templates/kumo-agent/gosuto.yaml" > "$KUMO_GOSUTO_FILE"

# Normalize workflow runtime expressions in the rendered test config.
# Current workflow interpolation in this runtime path expects {{...}} tokens.
sed -i \
  -e 's|\$input\.tickers|{{input.tickers}}|g' \
  -e 's|"\$steps\.step_1\.items"|"{{steps.step_1.items}}"|g' \
  -e 's|"\$state\.plan_item\.query"|"{{state.plan_item.query}}"|g' \
  -e 's|"\$steps\.step_2"|"{{steps.step_2}}"|g' \
  -e 's|\$steps\.step_3|{{steps.step_3}}|g' \
  -e 's|\$input\.run_id|{{input.run_id}}|g' \
  -e 's|\$steps\.step_4|{{steps.step_4}}|g' \
  -e "s|brave-search__web_search|brave-search__${BRAVE_TOOL_NAME}|g" \
  "$KUMO_GOSUTO_FILE"

# Resolve Brave MCP env placeholder for standalone mode. Without this, the
# rendered file keeps the literal token string `{{`${BRAVE_API_KEY}`}}`, which
# overrides the real container env var and causes Brave 422 auth failures.
python3 - "$KUMO_GOSUTO_FILE" "$BRAVE_API_KEY" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
brave_key = sys.argv[2]
text = path.read_text(encoding="utf-8")

placeholder = "{{`${BRAVE_API_KEY}`}}"
replacement = brave_key if brave_key else "${BRAVE_API_KEY}"

if placeholder in text:
    text = text.replace(placeholder, replacement)
    path.write_text(text, encoding="utf-8")
PY

if [[ -n "$BRAVE_PROXY_URL" ]]; then
  # Brave MCP server runs in a Node process and honors standard proxy env vars.
  python3 - "$KUMO_GOSUTO_FILE" "$BRAVE_PROXY_URL" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
proxy = sys.argv[2].strip()
text = path.read_text(encoding="utf-8")

needle = "    env:\n      BRAVE_API_KEY:"
insert = "    env:\n      HTTPS_PROXY: \"%s\"\n      HTTP_PROXY: \"%s\"\n      BRAVE_API_KEY:" % (proxy, proxy)

if needle in text:
    text = text.replace(needle, insert, 1)
    path.write_text(text, encoding="utf-8")
PY
  info "Configured Brave MCP proxy routing via KUMO_LIVE_BRAVE_PROXY_URL"
fi

# Make schema-bound LLM steps deterministic in live passthrough mode by
# explicitly requiring JSON-only responses (no markdown prose/fences).
if [[ "$OPENAI_MODE" == "passthrough" ]]; then
  sed -i \
    -e 's|Build a concise search plan for these tickers: {{input.tickers}}|Build a concise search plan for these tickers: {{input.tickers}}. Return only JSON. Required shape: items is an array with exactly one item, and that item has query as a string. Do not include markdown or prose.|g' \
    -e 's|Summarize these collected search results: {{steps.step_3}} and produce the response schema for run {{input.run_id}}|Summarize these collected search results: {{steps.step_3}} for run {{input.run_id}} in 3-5 factual sentences. Return plain text only.|g' \
    -e 's|outputSchemaRef: kumoNewsResponse|outputSchemaRef: searchResult|g' \
    "$KUMO_GOSUTO_FILE"
fi

pass "Rendered gosuto file: $KUMO_GOSUTO_FILE"

info "Starting standalone Kumo container"
export KUMO_LIVE_GOSUTO_FILE="$KUMO_GOSUTO_FILE"
export KUMO_LIVE_KUMO_MXID="$KUMO_MXID"
export KUMO_LIVE_KUMO_TOKEN="$KUMO_TOKEN"
export KUMO_LIVE_OPENAI_BASE_URL="http://host.docker.internal:${OPENAI_PROXY_PORT}/v1"
export KUMO_LIVE_OPENAI_API_KEY="$OPENAI_API_KEY"
export KUMO_LIVE_BRAVE_API_KEY="$BRAVE_API_KEY"

docker_compose up -d kumo

if ! wait_for_log_match "kumo-live-kumo" "gosuto config applied|ACP server listening" 90; then
  info "Recent Kumo logs:"
  docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 120 || true
  fail "Kumo did not show expected startup log markers"
fi
pass "Kumo container started and loaded config"

start_debug_streams

if ! docker exec kumo-live-kumo sh -lc 'command -v npx >/dev/null 2>&1'; then
  info "Recent Kumo logs:"
  docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 80 || true
  fail "kumo container is missing 'npx'; Brave/Fetch MCP servers cannot start. Rebuild gitai image with Node.js/npm (or provide a custom image via KUMO_LIVE_GITAI_IMAGE)."
fi
pass "Found npx inside Kumo container (MCP servers can be launched)"

REQUEST_BODY="${KUMO_LIVE_REQUEST_BODY:-KUMO_NEWS_REQUEST {\"run_id\":1,\"tickers\":[\"OpenAI\"]}}"
TXN_ID="kumo-live-$(date +%s)"
info "Sending operator request to Kumo workflow"
python3 "$PROBE_HELPER" send \
  --base-url "$MATRIX_BASE_URL" \
  --token "$OPERATOR_TOKEN" \
  --room "$ROOM_ID" \
  --body "$REQUEST_BODY" \
  --txn-id "$TXN_ID" >/dev/null || fail "failed to send matrix message"
pass "Operator request sent"

info "Waiting for OpenAI plan call evidence"
OPENAI_DEADLINE=$(( $(date +%s) + OPENAI_WAIT_TIMEOUT ))
while [[ $(date +%s) -lt "$OPENAI_DEADLINE" ]]; do
  CALLS="$(python3 - "$OPENAI_CAPTURE" <<'PY'
import pathlib,sys
path = pathlib.Path(sys.argv[1])
if not path.exists():
    print(0)
else:
    lines = [ln for ln in path.read_text(encoding='utf-8').splitlines() if ln.strip()]
    print(len(lines))
PY
)"
  if [[ "$CALLS" -ge 1 ]]; then
    break
  fi
  sleep "$POLL_SECONDS"
done

if [[ "${CALLS:-0}" -lt 1 ]]; then
  info "Recent Kumo logs:"
  docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 120 || true
  fail "did not observe any OpenAI calls within ${OPENAI_WAIT_TIMEOUT}s; capture file: $OPENAI_CAPTURE"
fi
pass "Observed OpenAI call(s): $CALLS"

if docker logs --since 5m kumo-live-kumo 2>&1 | grep -E "workflow step 1 \(plan\) failed|step plan failed" >/dev/null 2>&1; then
  info "Recent Kumo logs:"
  docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 120 || true
  fail "workflow plan step failed before tool execution; refusing early instead of waiting for Brave timeout"
fi

if docker logs --since 5m kumo-live-kumo 2>&1 | grep -E "Unknown tool:" >/dev/null 2>&1; then
  info "Recent Kumo logs:"
  docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 160 || true
  fail "workflow tool step failed (possible Brave tool name mismatch). Try KUMO_LIVE_BRAVE_TOOL_NAME=brave_web_search or inspect MCP tool names."
fi

if docker logs --since 5m kumo-live-kumo 2>&1 | grep -E "SUBSCRIPTION_TOKEN_INVALID|invalid subscription token" >/dev/null 2>&1; then
  info "Recent Kumo logs:"
  docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 180 || true
  fail "Brave API authentication failed (invalid subscription token). Verify KUMO_LIVE_BRAVE_API_KEY is injected into the container and rendered gosuto MCP env."
fi

if docker logs --since 5m kumo-live-kumo 2>&1 | grep -E "Rate limit exceeded|rate limit" >/dev/null 2>&1; then
  info "Recent Kumo logs:"
  docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 180 || true
  fail "Brave API rate limit exceeded during workflow execution. Use fewer plan items (single ticker/query) or retry after the provider window resets."
fi

info "Verifying Brave tool path attempt in Kumo logs"
if wait_for_log_match "kumo-live-kumo" "policy evaluation.*tool=${BRAVE_TOOL_NAME}|brave-search__${BRAVE_TOOL_NAME}|tool call brave-search.${BRAVE_TOOL_NAME}|tool call brave-search.web_search" "$BRAVE_WAIT_TIMEOUT"; then
  pass "Observed Brave search tool invocation path"
else
  info "Recent Kumo logs:"
  docker logs --since 8m kumo-live-kumo 2>&1 | tail -n 180 || true
  fail "did not observe Brave search tool invocation evidence within ${BRAVE_WAIT_TIMEOUT}s"
fi

if [[ "$REQUIRE_SUMMARY" == "1" ]]; then
  # A completed summary path should produce a second OpenAI call (summarize step).
  SUMMARY_OPENAI_DEADLINE=$(( $(date +%s) + BRAVE_WAIT_TIMEOUT ))
  while [[ $(date +%s) -lt "$SUMMARY_OPENAI_DEADLINE" ]]; do
    SUMMARY_CALLS="$(python3 - "$OPENAI_CAPTURE" <<'PY'
import pathlib,sys
path = pathlib.Path(sys.argv[1])
if not path.exists():
    print(0)
else:
    lines = [ln for ln in path.read_text(encoding='utf-8').splitlines() if ln.strip()]
    print(len(lines))
PY
)"
    if [[ "${SUMMARY_CALLS:-0}" -ge 2 ]]; then
      break
    fi

    if docker logs --since 5m kumo-live-kumo 2>&1 | grep -E "turn failed|workflow step [0-9]+ \(|tool .* returned error" >/dev/null 2>&1; then
      info "Recent Kumo logs:"
      docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 180 || true
      fail "workflow failed before summary generation (no second OpenAI call observed)"
    fi
    sleep "$POLL_SECONDS"
  done

  if [[ "${SUMMARY_CALLS:-0}" -lt 2 ]]; then
    info "Recent Kumo logs:"
    docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 180 || true
    fail "summary path did not reach summarize step (second OpenAI call not observed within ${BRAVE_WAIT_TIMEOUT}s)"
  fi

  info "Summary required: waiting for KUMO_NEWS_RESPONSE in operator sync"
  since_token=""
  deadline=$(( $(date +%s) + SUMMARY_WAIT_TIMEOUT ))
  found_summary=0
  while [[ $(date +%s) -lt "$deadline" ]]; do
    if docker logs --since 5m kumo-live-kumo 2>&1 | grep -E "turn failed|workflow step [0-9]+ \(|tool .* returned error" >/dev/null 2>&1; then
      info "Recent Kumo logs:"
      docker logs --since 5m kumo-live-kumo 2>&1 | tail -n 200 || true
      fail "workflow turn failed before sending KUMO_NEWS_RESPONSE"
    fi

    payload="$(python3 "$PROBE_HELPER" sync --base-url "$MATRIX_BASE_URL" --token "$OPERATOR_TOKEN" --since "$since_token" --timeout-ms 10000 --timeout 20 --rooms "$ROOM_ID" --senders "$KUMO_MXID")" || true
    if [[ -n "$payload" ]]; then
      python3 "$PROBE_HELPER" events-print --payload "$payload" >/dev/null 2>&1 || true
      count="$(python3 "$PROBE_HELPER" events-count --payload "$payload" --room "$ROOM_ID" --sender "$KUMO_MXID" --contains "KUMO_NEWS_RESPONSE")"
      if [[ "$count" -ge 1 ]]; then
        found_summary=1
        break
      fi
      since_token="$(python3 "$PROBE_HELPER" next-batch --payload "$payload")"
    fi
    sleep "$POLL_SECONDS"
  done
  if [[ "$found_summary" -ne 1 ]]; then
    info "Recent Kumo logs:"
    docker logs --since 8m kumo-live-kumo 2>&1 | tail -n 200 || true
    fail "summary message was not observed (KUMO_LIVE_REQUIRE_SUMMARY=1)"
  fi
  pass "Observed KUMO_NEWS_RESPONSE from Kumo"
fi

info "OpenAI request capture file: $OPENAI_CAPTURE"
info "OpenAI proxy log: $OPENAI_PROXY_LOG"
info "Generated Matrix credentials file: $CREDS_FILE"
if [[ -n "$BRAVE_PROXY_URL" ]]; then
  info "Brave proxy URL in use: $BRAVE_PROXY_URL"
fi

if [[ -n "${BRAVE_API_KEY}" ]]; then
  pass "KUMO_LIVE_BRAVE_API_KEY is set; Brave requests can reach live API"
else
  info "KUMO_LIVE_BRAVE_API_KEY is empty; Brave calls are expected to fail auth, but invocation evidence was still checked"
fi

pass "Kumo standalone live compose test completed"

#!/usr/bin/env bash
# test/integration/test-saito-scheduling-live-compose.sh
#
# Live integration test for Saito scheduling against a compose stack.
#
# Preconditions:
#   - examples/docker-compose/.env exists and is valid
#   - Docker is running
#   - Saito and Kairo agents are already provisioned by Ruriko
#   - Saito has a cron gateway configured and messaging target alias "kairo"
#
# What it does:
#   1) docker compose up -d (ruriko + tuwunel)
#   2) waits for Ruriko health endpoint
#   3) finds the running Saito container
#   4) watches Saito logs for:
#        - event_type=cron.tick
#        - matrix.send_message ... target=kairo ... status=success
#   5) tears down compose stack (unless KEEP_STACK=1)
#
# Usage:
#   ./test/integration/test-saito-scheduling-live-compose.sh
#
# Optional env vars:
#   SAITO_SCHEDULING_COMPOSE_FILE=...      compose file path
#   SAITO_SCHEDULING_COMPOSE_ENV=...       compose env file path
#   SAITO_SCHEDULING_TIMEOUT_SECONDS=180   wait window for log evidence
#   KEEP_STACK=1                           keep compose stack running after test
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE_FILE="${SAITO_SCHEDULING_COMPOSE_FILE:-${REPO_ROOT}/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV="${SAITO_SCHEDULING_COMPOSE_ENV:-${REPO_ROOT}/examples/docker-compose/.env}"
TIMEOUT_SECONDS="${SAITO_SCHEDULING_TIMEOUT_SECONDS:-180}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}✓${NC} $*"; }
fail() { echo -e "  ${RED}✗${NC} $*"; }
info() { echo -e "  ${YELLOW}→${NC} $*"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    fail "required command not found: $1"
    exit 1
  }
}

teardown() {
  if [[ "${KEEP_STACK:-0}" == "1" ]]; then
    info "KEEP_STACK=1 set; leaving compose stack running"
    return
  fi
  info "Stopping compose stack"
  (cd "${REPO_ROOT}/examples/docker-compose" && docker compose --env-file "${COMPOSE_ENV}" -f "${COMPOSE_FILE}" down >/dev/null 2>&1 || true)
}
trap teardown EXIT

require_cmd docker
require_cmd curl

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  fail "compose file not found: ${COMPOSE_FILE}"
  exit 1
fi
if [[ ! -f "${COMPOSE_ENV}" ]]; then
  fail "compose env file not found: ${COMPOSE_ENV}"
  exit 1
fi

info "Starting compose stack"
(cd "${REPO_ROOT}/examples/docker-compose" && docker compose --env-file "${COMPOSE_ENV}" -f "${COMPOSE_FILE}" up -d)

info "Waiting for Ruriko health endpoint"
HEALTH_URL="http://127.0.0.1:8080/health"
HEALTH_DEADLINE=$(( $(date +%s) + 90 ))
while true; do
  if curl -fsS "${HEALTH_URL}" >/dev/null 2>&1; then
    pass "Ruriko health endpoint is reachable"
    break
  fi
  if [[ $(date +%s) -ge ${HEALTH_DEADLINE} ]]; then
    fail "Ruriko health endpoint did not become ready at ${HEALTH_URL}"
    exit 1
  fi
  sleep 2
done

SAITO_CONTAINER="$(docker ps --format '{{.Names}}' | grep -E '(^|[-_])saito($|[-_])' | head -n1 || true)"
if [[ -z "${SAITO_CONTAINER}" ]]; then
  fail "No running Saito container found. Provision Saito first, then rerun this target."
  echo "    Tip: create/provision Saito and Kairo via Ruriko before running the live scheduling test."
  exit 1
fi
pass "Found Saito container: ${SAITO_CONTAINER}"

info "Watching Saito logs for cron.tick + matrix.send_message(target=kairo) evidence"
LOG_DEADLINE=$(( $(date +%s) + TIMEOUT_SECONDS ))
FOUND_CRON=0
FOUND_SEND=0

while [[ $(date +%s) -lt ${LOG_DEADLINE} ]]; do
  LOGS="$(docker logs --since 5m "${SAITO_CONTAINER}" 2>&1 || true)"

  if echo "${LOGS}" | grep -q 'event_type=cron.tick'; then
    FOUND_CRON=1
  fi
  if echo "${LOGS}" | grep -q 'matrix.send_message' && \
     echo "${LOGS}" | grep -q 'target=kairo' && \
     echo "${LOGS}" | grep -q 'status=success'; then
    FOUND_SEND=1
  fi

  if [[ ${FOUND_CRON} -eq 1 && ${FOUND_SEND} -eq 1 ]]; then
    pass "Observed cron.tick and successful matrix.send_message to kairo in Saito logs"
    exit 0
  fi

  sleep 5
done

fail "Timed out after ${TIMEOUT_SECONDS}s waiting for live scheduling evidence in Saito logs"
if [[ ${FOUND_CRON} -eq 0 ]]; then
  echo "    Missing: event_type=cron.tick"
fi
if [[ ${FOUND_SEND} -eq 0 ]]; then
  echo "    Missing: matrix.send_message with target=kairo and status=success"
fi
exit 1

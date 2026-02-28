#!/usr/bin/env bash
# test/integration/test-r6-workflow-live-compose.sh
#
# R6.7 live compose validation: verify at least N consecutive canonical cycles
# (Saito -> Kairo -> Kumo) using runtime log evidence.
#
# Evidence per cycle (default N=3):
#   - Saito processes cron.tick and sends matrix message to Kairo
#   - Kairo sends matrix message to Kumo
#   - Kumo sends matrix message back to Kairo
#
# Usage:
#   ./test/integration/test-r6-workflow-live-compose.sh
#
# Optional env vars:
#   R6_COMPOSE_FILE=...                 compose file path
#   R6_COMPOSE_ENV=...                  compose env file path
#   R6_LIVE_TIMEOUT_SECONDS=2400        wait window (default 40 min)
#   R6_REQUIRED_CYCLES=3                required cycles
#   R6_AUTO_START_STACK=1               run docker compose up -d (default 1)
#   KEEP_STACK=1                        do not tear down stack on exit
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE_FILE="${R6_COMPOSE_FILE:-${REPO_ROOT}/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV="${R6_COMPOSE_ENV:-${REPO_ROOT}/examples/docker-compose/.env}"
TIMEOUT_SECONDS="${R6_LIVE_TIMEOUT_SECONDS:-2400}"
REQUIRED_CYCLES="${R6_REQUIRED_CYCLES:-3}"
AUTO_START_STACK="${R6_AUTO_START_STACK:-1}"

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

count_success_sends() {
  local logs="$1"
  local target_alias="$2"
  local filtered

  filtered="$(echo "${logs}" \
    | grep -E 'matrix\.send_message' \
    | grep -E "target[=:]\"?${target_alias}\"?|\"target\":\"${target_alias}\"" \
    | grep -E 'status[=:]\"?success\"?|\"status\":\"success\"' \
    || true)"

  if [[ -z "${filtered}" ]]; then
    echo 0
    return
  fi

  echo "${filtered}" \
    | wc -l \
    | tr -d ' '
}

count_cron_ticks() {
  local logs="$1"

  echo "${logs}" \
    | grep -Ec 'event_type=cron\.tick|"event_type":"cron\.tick"' \
    || true
}

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

if [[ "${AUTO_START_STACK}" == "1" ]]; then
  info "Starting compose stack"
  (cd "${REPO_ROOT}/examples/docker-compose" && docker compose --env-file "${COMPOSE_ENV}" -f "${COMPOSE_FILE}" up -d)
fi

info "Waiting for Ruriko health endpoint"
HEALTH_URL="http://127.0.0.1:8080/health"
HEALTH_DEADLINE=$(( $(date +%s) + 120 ))
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
KAIRO_CONTAINER="$(docker ps --format '{{.Names}}' | grep -E '(^|[-_])kairo($|[-_])' | head -n1 || true)"
KUMO_CONTAINER="$(docker ps --format '{{.Names}}' | grep -E '(^|[-_])kumo($|[-_])' | head -n1 || true)"

if [[ -z "${SAITO_CONTAINER}" || -z "${KAIRO_CONTAINER}" || -z "${KUMO_CONTAINER}" ]]; then
  fail "Could not discover all canonical agent containers (saito/kairo/kumo)."
  echo "    Found: saito=${SAITO_CONTAINER:-<none>} kairo=${KAIRO_CONTAINER:-<none>} kumo=${KUMO_CONTAINER:-<none>}"
  echo "    Provision and start canonical agents first, then rerun this script."
  exit 1
fi
pass "Found canonical containers: ${SAITO_CONTAINER}, ${KAIRO_CONTAINER}, ${KUMO_CONTAINER}"

START_TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
info "Collecting live evidence from logs since ${START_TS}"

DEADLINE=$(( $(date +%s) + TIMEOUT_SECONDS ))
while [[ $(date +%s) -lt ${DEADLINE} ]]; do
  SAITO_LOGS="$(docker logs --since "${START_TS}" "${SAITO_CONTAINER}" 2>&1 || true)"
  KAIRO_LOGS="$(docker logs --since "${START_TS}" "${KAIRO_CONTAINER}" 2>&1 || true)"
  KUMO_LOGS="$(docker logs --since "${START_TS}" "${KUMO_CONTAINER}" 2>&1 || true)"

  CRON_COUNT="$(count_cron_ticks "${SAITO_LOGS}")"
  SAITO_TO_KAIRO="$(count_success_sends "${SAITO_LOGS}" "kairo")"
  KAIRO_TO_KUMO="$(count_success_sends "${KAIRO_LOGS}" "kumo")"
  KUMO_TO_KAIRO="$(count_success_sends "${KUMO_LOGS}" "kairo")"

  info "progress: cron=${CRON_COUNT} saito->kairo=${SAITO_TO_KAIRO} kairo->kumo=${KAIRO_TO_KUMO} kumo->kairo=${KUMO_TO_KAIRO}"

  if [[ ${CRON_COUNT} -ge ${REQUIRED_CYCLES} && \
        ${SAITO_TO_KAIRO} -ge ${REQUIRED_CYCLES} && \
        ${KAIRO_TO_KUMO} -ge ${REQUIRED_CYCLES} && \
        ${KUMO_TO_KAIRO} -ge ${REQUIRED_CYCLES} ]]; then
    pass "Observed at least ${REQUIRED_CYCLES} canonical cycles in live compose logs"
    exit 0
  fi

  sleep 10
done

fail "Timed out after ${TIMEOUT_SECONDS}s waiting for ${REQUIRED_CYCLES} canonical cycles"
echo "    Last observed counts:"
echo "      cron.tick:       ${CRON_COUNT:-0}"
echo "      saito->kairo:    ${SAITO_TO_KAIRO:-0}"
echo "      kairo->kumo:     ${KAIRO_TO_KUMO:-0}"
echo "      kumo->kairo:     ${KUMO_TO_KAIRO:-0}"
exit 1

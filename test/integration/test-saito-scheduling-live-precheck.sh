#!/usr/bin/env bash
# test/integration/test-saito-scheduling-live-precheck.sh
#
# Precheck helper for live Saito scheduling validation.
#
# It validates that the local environment is ready before running
# test-saito-scheduling-live-compose.sh:
#   - docker and curl are available
#   - compose file and env file exist
#   - Ruriko health endpoint is reachable (optional strict mode)
#   - running Saito and Kairo containers are discoverable
#
# Usage:
#   ./test/integration/test-saito-scheduling-live-precheck.sh
#
# Optional env vars:
#   SAITO_SCHEDULING_COMPOSE_FILE=...         compose file path
#   SAITO_SCHEDULING_COMPOSE_ENV=...          compose env file path
#   SAITO_SCHEDULING_REQUIRE_HEALTH=1         fail if health endpoint not reachable
#   SAITO_SCHEDULING_HEALTH_URL=...           override health endpoint (default: http://127.0.0.1:8080/health)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE_FILE="${SAITO_SCHEDULING_COMPOSE_FILE:-${REPO_ROOT}/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV="${SAITO_SCHEDULING_COMPOSE_ENV:-${REPO_ROOT}/examples/docker-compose/.env}"
REQUIRE_HEALTH="${SAITO_SCHEDULING_REQUIRE_HEALTH:-0}"
HEALTH_URL="${SAITO_SCHEDULING_HEALTH_URL:-http://127.0.0.1:8080/health}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}✓${NC} $*"; }
fail() { echo -e "  ${RED}✗${NC} $*"; }
info() { echo -e "  ${YELLOW}→${NC} $*"; }

REQUIRED_OK=1

check_cmd() {
  local cmd="$1"
  if command -v "${cmd}" >/dev/null 2>&1; then
    pass "command available: ${cmd}"
  else
    fail "required command missing: ${cmd}"
    REQUIRED_OK=0
  fi
}

check_file() {
  local path="$1"
  local label="$2"
  if [[ -f "${path}" ]]; then
    pass "${label}: ${path}"
  else
    fail "${label} not found: ${path}"
    REQUIRED_OK=0
  fi
}

find_running_container_by_name_hint() {
  local hint="$1"
  docker ps --format '{{.Names}}' | grep -E "(^|[-_])${hint}($|[-_])" | head -n1 || true
}

info "Live Saito scheduling precheck"
check_cmd docker
check_cmd curl
check_file "${COMPOSE_FILE}" "compose file"
check_file "${COMPOSE_ENV}" "compose env"

if [[ ${REQUIRED_OK} -ne 1 ]]; then
  echo
  fail "precheck failed: base requirements are missing"
  exit 1
fi

if curl -fsS "${HEALTH_URL}" >/dev/null 2>&1; then
  pass "Ruriko health reachable: ${HEALTH_URL}"
else
  if [[ "${REQUIRE_HEALTH}" == "1" ]]; then
    fail "Ruriko health is not reachable: ${HEALTH_URL}"
    echo "    Start stack first: make compose-up"
    exit 1
  fi
  info "Ruriko health not reachable yet (this is fine; live test can start compose)"
fi

SAITO_CONTAINER="$(find_running_container_by_name_hint saito)"
KAIRO_CONTAINER="$(find_running_container_by_name_hint kairo)"

if [[ -n "${SAITO_CONTAINER}" ]]; then
  pass "running Saito container found: ${SAITO_CONTAINER}"
else
  fail "running Saito container not found"
  REQUIRED_OK=0
fi

if [[ -n "${KAIRO_CONTAINER}" ]]; then
  pass "running Kairo container found: ${KAIRO_CONTAINER}"
else
  fail "running Kairo container not found"
  REQUIRED_OK=0
fi

if [[ ${REQUIRED_OK} -ne 1 ]]; then
  echo
  fail "precheck failed: live Saito scheduling prerequisites not met"
  echo "    Expected: provisioned and running canonical agents Saito and Kairo."
  echo "    Suggested flow:"
  echo "      1) Start control plane: make compose-up"
  echo "      2) Provision agents via Ruriko (saito-agent + kairo-agent templates)"
  echo "      3) Ensure both agent containers are running"
  echo "      4) Re-run: make test-saito-scheduling-live"
  exit 1
fi

pass "Live Saito scheduling precheck passed"

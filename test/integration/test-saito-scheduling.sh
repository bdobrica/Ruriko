#!/usr/bin/env bash
# test/integration/test-saito-scheduling.sh
#
# Integration test entrypoint for deterministic Saito scheduling.
#
# What it verifies:
#   1) Deterministic Saito cron.tick path sends a Matrix message to Kairo via
#      matrix.send_message without requiring an LLM provider.
#   2) Optional compose smoke check for environments that already have a
#      configured examples/docker-compose/.env.
#
# Usage:
#   ./test/integration/test-saito-scheduling.sh
#
# Optional env vars:
#   SAITO_SCHEDULING_COMPOSE_SMOKE=1     Enable docker compose smoke check.
#   SAITO_SCHEDULING_COMPOSE_FILE=...    Compose file path override.
#   SAITO_SCHEDULING_COMPOSE_ENV=...     Env file path override.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}✓${NC} $*"; }
fail() { echo -e "  ${RED}✗${NC} $*"; }
info() { echo -e "  ${YELLOW}→${NC} $*"; }

info "Deterministic Saito scheduling integration (Go test)"
if (cd "${REPO_ROOT}" && go test -v -race -run '^TestRunEventTurn_SaitoCronDeterministic_NoLLMRequired$' ./internal/gitai/app); then
  pass "Deterministic Saito cron.tick -> matrix.send_message path passed"
else
  fail "Deterministic Saito scheduling integration test failed"
  exit 1
fi

if [[ "${SAITO_SCHEDULING_COMPOSE_SMOKE:-0}" != "1" ]]; then
  info "Skipping compose smoke check (set SAITO_SCHEDULING_COMPOSE_SMOKE=1 to enable)"
  exit 0
fi

COMPOSE_FILE="${SAITO_SCHEDULING_COMPOSE_FILE:-${REPO_ROOT}/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV="${SAITO_SCHEDULING_COMPOSE_ENV:-${REPO_ROOT}/examples/docker-compose/.env}"

if ! command -v docker >/dev/null 2>&1; then
  fail "docker is not installed"
  exit 1
fi

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  fail "compose file not found: ${COMPOSE_FILE}"
  exit 1
fi

if [[ ! -f "${COMPOSE_ENV}" ]]; then
  fail "compose env file not found: ${COMPOSE_ENV}"
  exit 1
fi

info "Compose smoke: validating compose config"
if (cd "${REPO_ROOT}" && docker compose --env-file "${COMPOSE_ENV}" -f "${COMPOSE_FILE}" config >/tmp/ruriko-saito-scheduling-compose-config.out); then
  pass "Compose config resolves with ${COMPOSE_ENV}"
else
  fail "Compose config validation failed"
  exit 1
fi

pass "Deterministic Saito scheduling integration entrypoint passed"

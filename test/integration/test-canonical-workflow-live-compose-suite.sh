#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${CANONICAL_COMPOSE_FILE:-$ROOT_DIR/examples/docker-compose/docker-compose.yaml}"
KEEP_SUITE_STACK="${CANONICAL_SUITE_KEEP_STACK:-0}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${YELLOW}[INFO]${NC} $*"; }
pass() { echo -e "${GREEN}[PASS]${NC} $*"; }

docker_compose() {
	if command -v docker-compose >/dev/null 2>&1; then
		docker-compose -f "$COMPOSE_FILE" "$@"
	else
		docker compose -f "$COMPOSE_FILE" "$@"
	fi
}

cleanup_suite() {
	if [[ "$KEEP_SUITE_STACK" == "1" ]]; then
		info "CANONICAL_SUITE_KEEP_STACK=1 set; leaving compose stack running"
		return
	fi
	info "Suite cleanup: stopping compose stack"
	docker_compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup_suite EXIT

export GITAI_LLM_CALL_HARD_LIMIT="${CANONICAL_LLM_CALL_HARD_LIMIT:-12}"
export RURIKO_AGENT_RESTART_POLICY="${CANONICAL_AGENT_RESTART_POLICY:-no}"
export RURIKO_AGENT_RESTART_POLICY="${CANONICAL_AGENT_RESTART_POLICY:-no}"

info "Step 1/8: fresh stack provisioning"
bash "$ROOT_DIR/test/integration/provision-fresh-stack.sh"

info "Step 2/8: start compose stack"
docker_compose up -d

info "Step 3/8: create canonical agents"
bash "$ROOT_DIR/test/integration/create-canonical-agents.sh"

info "Step 4/8: canonical provisioning precheck"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-provisioning.sh"

info "Step 5/8: canonical admin-room join check"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-admin-room.sh"

info "Step 6/8: canonical stage check (saito -> kairo)"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-stage-saito-kairo.sh"

info "Step 7/8: canonical stage check (kairo -> kumo)"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-stage-kairo-kumo.sh"

info "Step 8/8: canonical stage check (kumo -> kairo)"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-stage-kumo-kairo.sh"

pass "Canonical live compose suite passed"

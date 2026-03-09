#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KEEP_SUITE_STACK="${CANONICAL_SUITE_KEEP_STACK:-0}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${YELLOW}[INFO]${NC} $*"; }
pass() { echo -e "${GREEN}[PASS]${NC} $*"; }

# Canonical workflow setup can generate many LLM calls before steady state.
# Default to unlimited unless the caller explicitly requests a hard cap.
export GITAI_LLM_CALL_HARD_LIMIT="${CANONICAL_LLM_CALL_HARD_LIMIT:-0}"
export RURIKO_AGENT_RESTART_POLICY="${CANONICAL_AGENT_RESTART_POLICY:-no}"

if [[ "$KEEP_SUITE_STACK" == "1" ]]; then
	export KEEP_STACK=1
fi

info "Running canonical live compose flow (operator -> Ruriko -> Saito -> Kumo)"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-compose.sh"

pass "Canonical live compose suite passed"

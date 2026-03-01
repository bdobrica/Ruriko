#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${YELLOW}[INFO]${NC} $*"; }
pass() { echo -e "${GREEN}[PASS]${NC} $*"; }

info "Step 1/3: canonical provisioning precheck"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-provisioning.sh"

info "Step 2/3: canonical admin-room join check"
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-admin-room.sh"

info "Step 3/3: canonical cycle verification"
CANONICAL_ENFORCE_ROOM_JOINS="${CANONICAL_ENFORCE_ROOM_JOINS:-0}" \
bash "$ROOT_DIR/test/integration/test-canonical-workflow-live-compose.sh"

pass "Canonical live compose suite passed"

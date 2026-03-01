#!/usr/bin/env bash
# create-canonical-agents.sh — Send /ruriko agents create commands for saito, kairo, kumo
# Requires: provision-fresh-stack.sh to have run, Ruriko to be running
# Usage: bash test/integration/create-canonical-agents.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_DIR="${ROOT_DIR}/examples/docker-compose"
ENV_FILE="${COMPOSE_DIR}/.env"
TOKENS_FILE="${COMPOSE_DIR}/.agent-tokens"

source "$ENV_FILE"
[[ -f "$TOKENS_FILE" ]] && source "$TOKENS_FILE"

BASE_URL="http://${TUWUNEL_HOST:-127.0.0.1}:${TUWUNEL_PORT:-8008}"
AGENT_IMAGE="${DEFAULT_AGENT_IMAGE:-gitai:latest}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${YELLOW}[INFO]${NC} $*"; }
pass()  { echo -e "${GREEN}[PASS]${NC} $*"; }
fail()  { echo -e "${RED}[FAIL]${NC} $*"; exit 1; }

[[ -n "${OPERATOR_TOKEN:-}" ]] || fail "OPERATOR_TOKEN not set — run provision-fresh-stack.sh first"
[[ -n "${ADMIN_ROOM:-}" ]] || fail "ADMIN_ROOM not set — run provision-fresh-stack.sh first"

TXN_PREFIX="create-agents-$(date +%s)"

# --- Helper: send a Matrix message ---
send_message() {
    local token="$1" room_id="$2" body="$3" txn_id="$4"
    local encoded_room encoded_txn
    encoded_room=$(python3 -c "import urllib.parse; print(urllib.parse.quote('${room_id}', safe=''))")
    encoded_txn=$(python3 -c "import urllib.parse; print(urllib.parse.quote('${txn_id}', safe=''))")

    local resp
    resp=$(curl -sS -X PUT \
        "${BASE_URL}/_matrix/client/v3/rooms/${encoded_room}/send/m.room.message/${encoded_txn}" \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        -d "{\"msgtype\":\"m.text\",\"body\":$(python3 -c "import json; print(json.dumps('$body'))")}")

    local event_id
    event_id=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('event_id',''))" 2>/dev/null || true)
    [[ -n "$event_id" ]] || fail "Failed to send message: ${resp}"
    echo "$event_id"
}

# --- Helper: wait for agent to appear in Ruriko DB ---
wait_for_agent_in_db() {
    local agent_id="$1" timeout="${2:-120}"
    local start now elapsed
    start="$(date +%s)"

    while true; do
        local tmp_db_dir tmp_db
        tmp_db_dir="$(mktemp -d)"
        tmp_db="${tmp_db_dir}/ruriko.db"
        if docker cp ruriko:/data/ruriko.db "$tmp_db" >/dev/null 2>&1; then
            docker cp ruriko:/data/ruriko.db-wal "${tmp_db_dir}/ruriko.db-wal" >/dev/null 2>&1 || true
            docker cp ruriko:/data/ruriko.db-shm "${tmp_db_dir}/ruriko.db-shm" >/dev/null 2>&1 || true
            local status
            status=$(sqlite3 "$tmp_db" "SELECT status FROM agents WHERE id='${agent_id}'" 2>/dev/null || true)
            rm -rf "$tmp_db_dir"
            if [[ "$status" == "running" ]]; then
                return 0
            fi
            if [[ "$status" == "error" ]]; then
                fail "Agent ${agent_id} ended up in error state"
            fi
        else
            rm -rf "$tmp_db_dir"
        fi

        now="$(date +%s)"
        elapsed=$((now - start))
        if (( elapsed >= timeout )); then
            return 1
        fi
        sleep 2
    done
}

# --- Helper: wait for agent container to be running ---
wait_for_agent_container() {
    local agent_id="$1" timeout="${2:-60}"
    local container_name="ruriko-agent-${agent_id}"
    local start now elapsed
    start="$(date +%s)"

    while true; do
        if docker ps --format '{{.Names}}' | grep -q "^${container_name}$"; then
            return 0
        fi
        now="$(date +%s)"
        elapsed=$((now - start))
        if (( elapsed >= timeout )); then
            return 1
        fi
        sleep 2
    done
}

# ============================================================
# Wait for Ruriko to be healthy
# ============================================================
info "Waiting for Ruriko to be healthy..."
for i in $(seq 1 60); do
    status=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${HTTP_ADDR_PORT:-8080}/health" 2>/dev/null || echo "000")
    if [[ "$status" == "200" ]]; then
        pass "Ruriko healthy (attempt ${i})"
        break
    fi
    if (( i == 60 )); then
        fail "Ruriko did not become healthy in 60s"
    fi
    sleep 1
done

# ============================================================
# Create agents one by one
# ============================================================
for agent in saito kairo kumo; do
    template="${agent}-agent"
    info "Creating agent: ${agent} (template=${template}, image=${AGENT_IMAGE})"

    event_id=$(send_message "$OPERATOR_TOKEN" "$ADMIN_ROOM" \
        "/ruriko agents create --name ${agent} --template ${template} --image ${AGENT_IMAGE}" \
        "${TXN_PREFIX}-${agent}")
    pass "sent create command for ${agent}: ${event_id}"

    info "Waiting for ${agent} container to appear..."
    if ! wait_for_agent_container "$agent" 120; then
        # Dump Ruriko logs for debugging
        echo "--- Ruriko logs (last 30 lines) ---"
        docker logs --tail 30 ruriko 2>&1 || true
        echo "---"
        fail "Agent container ruriko-agent-${agent} did not appear within 120s"
    fi
    pass "container ruriko-agent-${agent} is running"

    info "Waiting for ${agent} DB status to reach 'running'..."
    if ! wait_for_agent_in_db "$agent" 120; then
        info "Agent ${agent} did not reach 'running' state within 120s; may still be provisioning"
    else
        pass "agent ${agent} is in 'running' state"
    fi

    # Small pause between agent creations to let Ruriko settle
    sleep 3
done

# ============================================================
# Summary
# ============================================================
echo ""
info "=== Agent Creation Summary ==="
for agent in saito kairo kumo; do
    container_name="ruriko-agent-${agent}"
    if docker ps --format '{{.Names}}' | grep -q "^${container_name}$"; then
        pass "${agent}: container running"
    else
        fail "${agent}: container NOT running"
    fi
done
echo ""
pass "All canonical agents created successfully!"

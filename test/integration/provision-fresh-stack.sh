#!/usr/bin/env bash
# provision-fresh-stack.sh — Wipe Tuwunel, re-register all accounts, create rooms, update .env
# Usage: bash test/integration/provision-fresh-stack.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_DIR="${ROOT_DIR}/examples/docker-compose"
ENV_FILE="${COMPOSE_DIR}/.env"

source "$ENV_FILE"

BASE_URL="http://${TUWUNEL_HOST:-127.0.0.1}:${TUWUNEL_PORT:-8008}"
RT="${TUWUNEL_REGISTRATION_TOKEN}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${YELLOW}[INFO]${NC} $*"; }
pass()  { echo -e "${GREEN}[PASS]${NC} $*"; }
fail()  { echo -e "${RED}[FAIL]${NC} $*"; exit 1; }

[[ -n "$RT" ]] || fail "TUWUNEL_REGISTRATION_TOKEN is empty in .env"

# --- Helper: two-step UIA registration ---
register_user() {
    local username="$1" password="$2"
    local resp session token

    # Step 1: get UIA session
    resp=$(curl -sS "${BASE_URL}/_matrix/client/v3/register" -X POST \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"${username}\",\"password\":\"${password}\"}")

    # Check if it succeeded directly (first user on fresh server)
    token=$(echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('access_token',''))" 2>/dev/null || true)
    if [[ -n "$token" ]]; then
        echo "$token"
        return 0
    fi

    # Check for error
    local errcode=$(echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('errcode',''))" 2>/dev/null || true)
    if [[ "$errcode" == "M_USER_IN_USE" ]]; then
        fail "User '${username}' already exists — Tuwunel data was not properly wiped"
    fi

    # Extract session for UIA
    session=$(echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session',''))" 2>/dev/null || true)
    [[ -n "$session" ]] || fail "No UIA session returned for ${username}: ${resp}"

    # Step 2: complete registration with token + session
    resp=$(curl -sS "${BASE_URL}/_matrix/client/v3/register" -X POST \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"${username}\",\"password\":\"${password}\",\"auth\":{\"type\":\"m.login.registration_token\",\"token\":\"${RT}\",\"session\":\"${session}\"}}")

    token=$(echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('access_token',''))" 2>/dev/null || true)
    [[ -n "$token" ]] || fail "Registration failed for ${username}: ${resp}"
    echo "$token"
}

# --- Helper: create room ---
create_room() {
    local token="$1" name="$2"
    local resp room_id

    resp=$(curl -sS "${BASE_URL}/_matrix/client/v3/createRoom" -X POST \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        -d "{\"name\":\"${name}\",\"preset\":\"private_chat\",\"visibility\":\"private\"}")

    room_id=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('room_id',''))" 2>/dev/null || true)
    [[ -n "$room_id" ]] || fail "Failed to create room '${name}': ${resp}"
    echo "$room_id"
}

# --- Helper: invite user to room ---
invite_to_room() {
    local token="$1" room_id="$2" user_id="$3"
    curl -sS "${BASE_URL}/_matrix/client/v3/rooms/$(python3 -c "import urllib.parse; print(urllib.parse.quote('${room_id}', safe=''))")/invite" \
        -X POST \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        -d "{\"user_id\":\"${user_id}\"}" >/dev/null
}

# --- Helper: join room ---
join_room() {
    local token="$1" room_id="$2"
    curl -sS "${BASE_URL}/_matrix/client/v3/join/$(python3 -c "import urllib.parse; print(urllib.parse.quote('${room_id}', safe=''))")" \
        -X POST \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        -d '{}' >/dev/null
}

# ============================================================
# 1. Start fresh Tuwunel
# ============================================================
info "Resetting compose services and volumes for a truly fresh Tuwunel state"
cd "$COMPOSE_DIR"
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

info "Removing stale managed agent containers"
stale_agents="$(docker ps -a --format '{{.Names}}' | grep '^ruriko-agent-' || true)"
if [[ -n "$stale_agents" ]]; then
    while IFS= read -r cname; do
        [[ -n "$cname" ]] || continue
        docker rm -f "$cname" >/dev/null 2>&1 || true
    done <<< "$stale_agents"
    pass "Removed stale agent containers"
else
    pass "No stale agent containers found"
fi

info "Starting fresh Tuwunel"
docker compose up -d tuwunel

info "Waiting for Tuwunel to be ready..."
for i in $(seq 1 60); do
    status=$(curl -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/_matrix/client/versions" 2>/dev/null || echo "000")
    if [[ "$status" == "200" ]]; then
        pass "Tuwunel ready (attempt ${i})"
        break
    fi
    if (( i == 60 )); then
        fail "Tuwunel did not become ready in 60s"
    fi
    sleep 1
done

# ============================================================
# 2. Register accounts (ruriko + operator only; agents are
#    provisioned by Ruriko via `/ruriko agents create`)
# ============================================================
info "Registering accounts..."

RURIKO_TOKEN=$(register_user ruriko "ruriko-pass-$(openssl rand -hex 8)")
pass "ruriko registered: ${RURIKO_TOKEN:0:10}..."

OPERATOR_TOKEN=$(register_user operator "operator-pass-$(openssl rand -hex 8)")
pass "operator registered: ${OPERATOR_TOKEN:0:10}..."

# ============================================================
# 3. Create admin room (operator creates, invites ruriko)
#    Agents will be invited later by Ruriko during `agents create`.
# ============================================================
info "Creating admin room..."
ADMIN_ROOM=$(create_room "$OPERATOR_TOKEN" "ruriko-admin")
pass "Admin room created: ${ADMIN_ROOM}"

# Invite and join ruriko
invite_to_room "$OPERATOR_TOKEN" "$ADMIN_ROOM" "@ruriko:localhost"
join_room "$RURIKO_TOKEN" "$ADMIN_ROOM"
pass "ruriko joined admin room"

# ============================================================
# 4. Create user room (for agent reports)
# ============================================================
info "Creating user room..."
USER_ROOM=$(create_room "$OPERATOR_TOKEN" "ruriko-reports")
pass "User room created: ${USER_ROOM}"

invite_to_room "$OPERATOR_TOKEN" "$USER_ROOM" "@ruriko:localhost"
join_room "$RURIKO_TOKEN" "$USER_ROOM"
pass "ruriko joined user room"

# ============================================================
# 5. Update .env with new tokens and room IDs
# ============================================================
info "Updating .env with new tokens and room IDs..."

# Use sed to replace values in .env
sed -i "s|^MATRIX_ACCESS_TOKEN=.*|MATRIX_ACCESS_TOKEN=${RURIKO_TOKEN}|" "$ENV_FILE"
sed -i "s|^MATRIX_ADMIN_ROOMS=.*|MATRIX_ADMIN_ROOMS=${ADMIN_ROOM},${USER_ROOM}|" "$ENV_FILE"
sed -i "s|^MATRIX_ADMIN_SENDERS=.*|MATRIX_ADMIN_SENDERS=@operator:localhost|" "$ENV_FILE"
pass ".env updated"

# ============================================================
# 6. Print summary for manual reference
# ============================================================
echo ""
info "=== Provisioning Summary ==="
echo "  Ruriko token:    ${RURIKO_TOKEN}"
echo "  Operator token:  ${OPERATOR_TOKEN}"
echo "  Admin room:      ${ADMIN_ROOM}"
echo "  User room:       ${USER_ROOM}"
echo ""
info "Agents (saito, kairo, kumo) are NOT pre-registered."
info "They will be provisioned by Ruriko via '/ruriko agents create' commands."
echo ""

# Save tokens for use by the test/compose stack
cat > "${COMPOSE_DIR}/.agent-tokens" <<EOF
OPERATOR_TOKEN=${OPERATOR_TOKEN}
ADMIN_ROOM=${ADMIN_ROOM}
USER_ROOM=${USER_ROOM}
EOF
pass "saved tokens to ${COMPOSE_DIR}/.agent-tokens"
pass "Provisioning complete! Start Ruriko and create agents via '/ruriko agents create'."

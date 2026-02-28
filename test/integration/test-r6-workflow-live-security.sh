#!/usr/bin/env bash
# test/integration/test-r6-workflow-live-security.sh
#
# R6.7 live security checklist verification:
#   - no secrets in compose logs
#   - no direct MCP call bypass from workflow path (source guard)
#   - approval ledger contains approved/denied decisions in Ruriko DB
#
# Usage:
#   ./test/integration/test-r6-workflow-live-security.sh
#
# Optional env vars:
#   R6_COMPOSE_FILE=...                       compose file path
#   R6_COMPOSE_ENV=...                        compose env file path
#   R6_SECURITY_LOOKBACK=30m                  docker logs lookback
#   R6_SECURITY_REQUIRE_APPROVAL_COUNTS=1     require >=1 approved and >=1 denied
#   R6_SENSITIVE_PATTERNS_FILE=...            newline-delimited patterns (optional)
#   R6_EXTRA_SENSITIVE_PATTERNS="a,b,c"      comma-separated extra patterns
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE_FILE="${R6_COMPOSE_FILE:-${REPO_ROOT}/examples/docker-compose/docker-compose.yaml}"
COMPOSE_ENV="${R6_COMPOSE_ENV:-${REPO_ROOT}/examples/docker-compose/.env}"
LOOKBACK="${R6_SECURITY_LOOKBACK:-30m}"
REQUIRE_APPROVAL_COUNTS="${R6_SECURITY_REQUIRE_APPROVAL_COUNTS:-1}"
PATTERNS_FILE="${R6_SENSITIVE_PATTERNS_FILE:-}"
EXTRA_PATTERNS="${R6_EXTRA_SENSITIVE_PATTERNS:-}"

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

require_cmd docker
require_cmd grep
require_cmd awk

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  fail "compose file not found: ${COMPOSE_FILE}"
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

LOG_DUMP="${TMP_DIR}/compose-logs.txt"
docker compose --env-file "${COMPOSE_ENV}" -f "${COMPOSE_FILE}" logs --since "${LOOKBACK}" --no-color >"${LOG_DUMP}" 2>&1 || true

build_patterns_file() {
  local out="$1"
  : >"${out}"

  if [[ -n "${PATTERNS_FILE}" && -f "${PATTERNS_FILE}" ]]; then
    cat "${PATTERNS_FILE}" >>"${out}"
  fi

  if [[ -n "${EXTRA_PATTERNS}" ]]; then
    echo "${EXTRA_PATTERNS}" | tr ',' '\n' >>"${out}"
  fi

  if [[ -f "${COMPOSE_ENV}" ]]; then
    awk -F= '
      /^[[:space:]]*#/ { next }
      NF < 2 { next }
      {
        key=$1
        val=$0
        sub(/^[^=]*=/, "", val)
        gsub(/^"|"$/, "", val)
        if (key ~ /(API_KEY|TOKEN|SECRET|PASSWORD|MASTER_KEY)/ && length(val) >= 12 && val !~ /^\$\{/ && val != "") {
          print val
        }
      }
    ' "${COMPOSE_ENV}" >>"${out}"
  fi

  sed -i '/^[[:space:]]*$/d' "${out}"
  awk '!seen[$0]++' "${out}" >"${out}.dedup"
  mv "${out}.dedup" "${out}"
}

PAT_FILE="${TMP_DIR}/sensitive-patterns.txt"
build_patterns_file "${PAT_FILE}"

if [[ -s "${PAT_FILE}" ]]; then
  LEAK_FOUND=0
  while IFS= read -r pattern; do
    if grep -Fq "${pattern}" "${LOG_DUMP}"; then
      fail "secret-like value found in compose logs: ${pattern:0:6}..."
      LEAK_FOUND=1
    fi
  done <"${PAT_FILE}"

  if [[ ${LEAK_FOUND} -eq 1 ]]; then
    exit 1
  fi
  pass "No configured secret patterns found in compose logs (lookback=${LOOKBACK})"
else
  info "No sensitive patterns configured/found; skipped secrets-in-logs assertion"
fi

# Guardrail: no direct MCP calls from workflow package (bypass risk).
if grep -R --line-number -E '\.CallTool\(' "${REPO_ROOT}/internal/gitai/workflow" >/dev/null 2>&1; then
  fail "detected direct CallTool usage in internal/gitai/workflow (bypass risk)"
  exit 1
fi

# Guardrail: canonical deterministic bypass hooks should remain absent.
if grep -R --line-number -E 'dispatchCallerPipeline|tryRunKairoDeterministicTurn|tryRunKumoDeterministicTurn|runDeterministicSaitoCronTurn' "${REPO_ROOT}/internal/gitai/app" >/dev/null 2>&1; then
  fail "detected legacy canonical bypass hooks in internal/gitai/app"
  exit 1
fi
pass "No workflow-path MCP bypass signatures detected in source checks"

RURIKO_CONTAINER="$(docker ps --format '{{.Names}}' | grep -E '(^|[-_])ruriko($|[-_])' | head -n1 || true)"
if [[ -z "${RURIKO_CONTAINER}" ]]; then
  fail "running Ruriko container not found; cannot verify approval ledger"
  exit 1
fi

APPROVED=0
DENIED=0

query_counts_with_go() {
  local db_file="$1"
  local go_file="${TMP_DIR}/query_approvals.go"

  cat >"${go_file}" <<'EOF'
package main

import (
  "database/sql"
  "fmt"
  "os"

  _ "modernc.org/sqlite"
)

func main() {
  if len(os.Args) != 2 {
    fmt.Println("0 0")
    return
  }
  db, err := sql.Open("sqlite", os.Args[1])
  if err != nil {
    fmt.Println("0 0")
    return
  }
  defer db.Close()

  var approved int
  var denied int

  if err := db.QueryRow("SELECT COUNT(*) FROM approvals WHERE status='approved'").Scan(&approved); err != nil {
    fmt.Println("0 0")
    return
  }
  if err := db.QueryRow("SELECT COUNT(*) FROM approvals WHERE status='denied'").Scan(&denied); err != nil {
    fmt.Println("0 0")
    return
  }

  fmt.Printf("%d %d\n", approved, denied)
}
EOF

  (cd "${REPO_ROOT}" && go run "${go_file}" "${db_file}")
}

if command -v sqlite3 >/dev/null 2>&1; then
  docker cp "${RURIKO_CONTAINER}:/data/ruriko.db" "${TMP_DIR}/ruriko.db" >/dev/null 2>&1 || true
  if [[ -f "${TMP_DIR}/ruriko.db" ]]; then
    APPROVED="$(sqlite3 "${TMP_DIR}/ruriko.db" "SELECT COUNT(*) FROM approvals WHERE status='approved';" 2>/dev/null || echo 0)"
    DENIED="$(sqlite3 "${TMP_DIR}/ruriko.db" "SELECT COUNT(*) FROM approvals WHERE status='denied';" 2>/dev/null || echo 0)"
  fi
elif docker exec "${RURIKO_CONTAINER}" sh -lc 'command -v sqlite3 >/dev/null 2>&1'; then
  APPROVED="$(docker exec "${RURIKO_CONTAINER}" sh -lc "sqlite3 /data/ruriko.db \"SELECT COUNT(*) FROM approvals WHERE status='approved';\"" 2>/dev/null || echo 0)"
  DENIED="$(docker exec "${RURIKO_CONTAINER}" sh -lc "sqlite3 /data/ruriko.db \"SELECT COUNT(*) FROM approvals WHERE status='denied';\"" 2>/dev/null || echo 0)"
else
  docker cp "${RURIKO_CONTAINER}:/data/ruriko.db" "${TMP_DIR}/ruriko.db" >/dev/null 2>&1 || true
  if [[ ! -f "${TMP_DIR}/ruriko.db" ]]; then
    fail "cannot copy /data/ruriko.db from Ruriko container; approval ledger check unavailable"
    exit 1
  fi

  COUNTS="$(query_counts_with_go "${TMP_DIR}/ruriko.db" 2>/dev/null || echo "0 0")"
  APPROVED="$(echo "${COUNTS}" | awk '{print $1}')"
  DENIED="$(echo "${COUNTS}" | awk '{print $2}')"
fi

APPROVED="${APPROVED//[^0-9]/}"
DENIED="${DENIED//[^0-9]/}"
APPROVED="${APPROVED:-0}"
DENIED="${DENIED:-0}"

if [[ "${REQUIRE_APPROVAL_COUNTS}" == "1" ]]; then
  if [[ ${APPROVED} -lt 1 || ${DENIED} -lt 1 ]]; then
    fail "approval ledger incomplete (approved=${APPROVED}, denied=${DENIED}); require both >=1"
    exit 1
  fi
fi

pass "Approval ledger check passed (approved=${APPROVED}, denied=${DENIED})"
pass "R6.7 live security checks completed"

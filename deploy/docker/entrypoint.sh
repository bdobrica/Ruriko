#!/bin/sh
# Ruriko container entrypoint
#
# Responsibilities:
#   1. Validate that mandatory environment variables are set.
#   2. Apply sensible defaults for optional variables.
#   3. Exec the requested command (default: ruriko).
#
# All configuration is done through environment variables — see .env.example
# in the repository root for a complete reference.

set -e

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
die() {
    echo "ERROR: $*" >&2
    exit 1
}

require_env() {
    local var="$1"
    eval "local val=\$$var"
    [ -n "$val" ] || die "Required environment variable $var is not set."
}

# ---------------------------------------------------------------------------
# Validate mandatory variables (only when the default ruriko command is run)
# ---------------------------------------------------------------------------
if [ "${1:-ruriko}" = "ruriko" ]; then
    require_env MATRIX_HOMESERVER
    require_env MATRIX_USER_ID
    require_env MATRIX_ACCESS_TOKEN
    require_env MATRIX_ADMIN_ROOMS
    require_env RURIKO_MASTER_KEY
fi

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
: "${DATABASE_PATH:=/data/ruriko.db}"
: "${TEMPLATES_DIR:=/templates}"
: "${LOG_LEVEL:=info}"
: "${LOG_FORMAT:=json}"
: "${RECONCILE_INTERVAL:=30s}"

export DATABASE_PATH TEMPLATES_DIR LOG_LEVEL LOG_FORMAT RECONCILE_INTERVAL

# ---------------------------------------------------------------------------
# Execute
# ---------------------------------------------------------------------------
exec "$@"

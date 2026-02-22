#!/usr/bin/env bash
# test/integration/test-gateway-binaries.sh
#
# Integration test: verify that the Gitai Docker image contains all gateway
# binaries declared in deploy/docker/gateway-manifest.yaml at their declared
# install paths, and that each binary is executable.
#
# Usage:
#   ./test/integration/test-gateway-binaries.sh [image-tag]
#
# Arguments:
#   image-tag   Docker image tag to test (default: gitai:latest)
#
# Exit codes:
#   0  All declared gateway binaries are present and executable.
#   1  One or more binaries are missing, not executable, or the build failed.
#
# The script will build the image if it does not already exist. Set
# SKIP_BUILD=1 to skip the build step (useful in CI where the image is
# built as a separate job).
#
# Required tools: docker, yq (or python3 for YAML parsing fallback)
set -euo pipefail

IMAGE="${1:-gitai:latest}"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
MANIFEST="${REPO_ROOT}/deploy/docker/gateway-manifest.yaml"

# ─── Colours ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Colour

pass() { echo -e "  ${GREEN}✓${NC} $*"; }
fail() { echo -e "  ${RED}✗${NC} $*"; }
info() { echo -e "  ${YELLOW}→${NC} $*"; }

# ─── Build ────────────────────────────────────────────────────────────────────
if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  info "Building ${IMAGE} from ${REPO_ROOT} ..."
  docker build \
    -f "${REPO_ROOT}/deploy/docker/Dockerfile.gitai" \
    --build-arg GIT_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
    --build-arg GIT_TAG="$(git -C "${REPO_ROOT}" describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)" \
    --build-arg BUILD_TIME="$(date -u '+%Y-%m-%d_%H:%M:%S')" \
    -t "${IMAGE}" \
    "${REPO_ROOT}"
  pass "Image built: ${IMAGE}"
fi

# ─── Parse manifest ───────────────────────────────────────────────────────────
# Extract installPath values from the manifest. We support two parsers:
#   1. yq (https://github.com/mikefarah/yq) — preferred
#   2. python3 with PyYAML — fallback

extract_install_paths() {
  if command -v yq &>/dev/null; then
    yq eval '.gateways[].installPath' "${MANIFEST}"
  elif command -v python3 &>/dev/null && python3 -c "import yaml" 2>/dev/null; then
    python3 - "${MANIFEST}" <<'EOF'
import sys, yaml
with open(sys.argv[1]) as f:
    data = yaml.safe_load(f)
for gw in data.get('gateways', []):
    print(gw['installPath'])
EOF
  else
    echo "ERROR: Neither yq nor python3+PyYAML is available. Install yq to run this test." >&2
    exit 1
  fi
}

mapfile -t INSTALL_PATHS < <(extract_install_paths)

if [[ ${#INSTALL_PATHS[@]} -eq 0 ]]; then
  echo "ERROR: No gateways found in ${MANIFEST}" >&2
  exit 1
fi

info "Gateway manifest declares ${#INSTALL_PATHS[@]} binary(ies):"
for p in "${INSTALL_PATHS[@]}"; do
  echo "     ${p}"
done
echo ""

# ─── Verify each binary inside the image ──────────────────────────────────────
FAILURES=0

for INSTALL_PATH in "${INSTALL_PATHS[@]}"; do
  BINARY_NAME="$(basename "${INSTALL_PATH}")"
  # Run a one-shot container and use `test -x` to check the binary exists and
  # is executable, then print its size for confirmation.
  if docker run --rm --entrypoint sh "${IMAGE}" \
       -c "test -x '${INSTALL_PATH}' && ls -lh '${INSTALL_PATH}'" \
       2>/dev/null; then
    pass "${BINARY_NAME} present and executable at ${INSTALL_PATH}"
  else
    fail "${BINARY_NAME} MISSING or not executable at ${INSTALL_PATH}"
    FAILURES=$((FAILURES + 1))
  fi
done

echo ""

# ─── Summary ─────────────────────────────────────────────────────────────────
if [[ ${FAILURES} -eq 0 ]]; then
  echo -e "${GREEN}All ${#INSTALL_PATHS[@]} gateway binary(ies) verified successfully.${NC}"
  exit 0
else
  echo -e "${RED}${FAILURES} binary check(s) failed.${NC}"
  exit 1
fi

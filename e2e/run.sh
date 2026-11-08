#!/usr/bin/env bash
# Runs the end-to-end tests against a Docker image of the app.
#
#   e2e/run.sh                 build webdav-nvr-player:e2e from this checkout, then test it
#   NVR_IMAGE=foo:bar SKIP_BUILD=1 e2e/run.sh   test an image that's already built
#
# Extra arguments go to `playwright test` (e.g. --headed, -g "sign in").
set -euo pipefail

cd "$(dirname "$0")"
export NVR_IMAGE="${NVR_IMAGE:-webdav-nvr-player:e2e}"
export NVR_PORT="${NVR_PORT:-18080}"
compose=(docker compose -f compose.yaml)

if [[ -z "${SKIP_BUILD:-}" ]]; then
  docker build -t "$NVR_IMAGE" ..
fi

cleanup() {
  status=$?
  if [[ $status -ne 0 ]]; then
    echo "::group::container logs"
    "${compose[@]}" logs --no-color || true
    echo "::endgroup::"
  fi
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  exit $status
}
trap cleanup EXIT

"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
"${compose[@]}" up -d --wait --wait-timeout 120 nvr

# First start prints the generated admin password; sign in with it like a new user would.
password=$("${compose[@]}" logs --no-color nvr | sed -n 's/.*created the admin user.* password=\([a-z0-9-]*\).*/\1/p' | head -n1)
if [[ -z "$password" ]]; then
  echo "no admin password in the server log" >&2
  exit 1
fi

[[ -d node_modules ]] || npm ci --no-audit --no-fund
NVR_URL="http://127.0.0.1:${NVR_PORT}" NVR_PASSWORD="$password" npx playwright test "$@"

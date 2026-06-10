#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
orchestrator="${repo_root}/orchestrator"
dist_bin="${repo_root}/dist/wisdev"

run_bin() {
  if [[ "${WISDEV_VERBOSE:-}" == "1" ]]; then
    echo "wisdev: using $1" >&2
  fi
  exec "$1" "${@:2}"
}

# Force dist/wisdev: WISDEV_USE_DIST=1 ./wisdev tui
if [[ "${WISDEV_USE_DIST:-}" == "1" ]]; then
  if [[ -x "${dist_bin}" ]]; then
    run_bin "${dist_bin}" "$@"
  fi
  echo "WISDEV_USE_DIST=1 but dist/wisdev not found. Run scripts/build-wisdev-cli.ps1 first." >&2
  exit 1
fi

if command -v wisdev >/dev/null 2>&1; then
  run_bin "$(command -v wisdev)" "$@"
fi

if [[ -x "${dist_bin}" ]]; then
  run_bin "${dist_bin}" "$@"
fi

cd "${orchestrator}"
exec go run ./cmd/wisdev "$@"

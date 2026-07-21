#!/usr/bin/env bash
# Allocate free local ports for the WisDev ARC stack and start sidecar + orchestrator.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
orchestrator="${repo_root}/orchestrator"
sidecar="${repo_root}/sidecar"
ports_file="${repo_root}/.wisdev/ports.env"

mkdir -p "${repo_root}/.wisdev"

export WISDEV_AUTO_PORT="${WISDEV_AUTO_PORT:-1}"
if [[ -f "${repo_root}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${repo_root}/.env"
  set +a
fi

run_wisdev() {
  if [[ -x "${repo_root}/dist/wisdev" ]]; then
    "${repo_root}/dist/wisdev" "$@"
  elif command -v wisdev >/dev/null 2>&1; then
    wisdev "$@"
  else
    (cd "${orchestrator}" && go run ./cmd/wisdev "$@")
  fi
}

run_wisdev stack ports --write

set -a
# shellcheck disable=SC1090
source "${ports_file}"
set +a

# Re-export after allocation so subprocesses see the resolved ports.
export WISDEV_AUTO_PORT=1

echo "WisDev stack ports:"
echo "  orchestrator: ${WISDEV_ORCHESTRATOR_URL}"
echo "  sidecar:      ${PYTHON_SIDECAR_HTTP_URL}"
echo "  sidecar grpc: ${PYTHON_SIDECAR_GRPC_ADDR}"
echo "  ports file:   ${ports_file}"

sidecar_python="${sidecar}/.venv/bin/python"
if [[ ! -x "${sidecar_python}" ]]; then
  sidecar_python="$(command -v python3)"
fi

export MANUSCRIPT_LLM_PROVIDER="${MANUSCRIPT_LLM_PROVIDER:-gemini}"

cleanup() {
  [[ -n "${sidecar_pid:-}" ]] && kill "${sidecar_pid}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "Starting Python sidecar on ${PYTHON_SIDECAR_HTTP_URL} ..."
(
  cd "${sidecar}"
  export PORT="${PYTHON_SIDECAR_PORT}"
  export PYTHON_SIDECAR_GRPC_ADDR
  exec "${sidecar_python}" main.py
) &
sidecar_pid=$!

for _ in $(seq 1 60); do
  if curl -fsS "${PYTHON_SIDECAR_HTTP_URL}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

echo "Starting Go orchestrator on ${WISDEV_ORCHESTRATOR_URL} ..."
(
  cd "${orchestrator}"
  export PORT
  export INTERNAL_METRICS_PORT
  export GO_INTERNAL_GRPC_ADDR
  export PYTHON_SIDECAR_HTTP_URL
  export PYTHON_SIDECAR_GRPC_ADDR
  export WISDEV_ORCHESTRATOR_URL
  exec go run ./cmd/server
)

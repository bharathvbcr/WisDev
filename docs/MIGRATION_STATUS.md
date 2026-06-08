# Migration Status

## Implemented In This Pass

- Created a fresh top-level `wisdev-agent-os` tree.
- Seeded `orchestrator/` from current `backend/go_orchestrator`, including current WisDev YOLO code and its dependent Go packages.
- Seeded `sidecar/` from current `backend/python_sidecar`, excluding generated coverage and local Python install artifacts.
- Rewrote Go import paths from the private parent module path to `github.com/wisdev/wisdev-agent-os/orchestrator`.
- Added open-source config, environment template, Makefile, docker-compose, adapter boundary notes, and README.
- Promoted the first stable embedding facade at `orchestrator/pkg/wisdev`.
- Added `wisdev yolo --local` so the CLI can execute YOLO without an HTTP server.
- Added `--offline` for deterministic local smoke runs without network-backed search providers.
- Added public search provider injection with public `Paper`, `SearchOptions`, and `SearchProvider` types.

## Canonical Source

The current extracted WisDev implementation is canonical:

- `orchestrator/internal/wisdev`
- `orchestrator/internal/api/wisdev*`
- `orchestrator/internal/api/yolo*`
- `orchestrator/internal/search`
- `orchestrator/internal/rag`
- `orchestrator/internal/evidence`
- `orchestrator/internal/llm`
- `orchestrator/proto`
- `sidecar/prompts`
- `sidecar/routers/wisdev_action_router.py`

## Still To Port

- Expand `pkg/wisdev` beyond YOLO into guided mode, streaming events, LLM provider injection, and durable job execution.
- Replace direct cloud/infra dependencies with `StorageProvider`, `SecretProvider`, `LLMProvider`, and `ExecutionBackend`.
- Make SQLite the default durable store.
- Split optional GCP, Redis, Postgres, and Temporal features behind build tags or provider packages.
- Remove remaining private-app edge bridge code paths from the extracted Go runtime.
- Add release CI for Go, Python, generated proto checks, secret scanning, and license scanning.

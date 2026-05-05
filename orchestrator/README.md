# WisDev Agent OS Go Orchestrator

The primary logic layer for the open-source WisDev agent. Coordinated research flows, RAG, and academic search stay in Go.

## Responsibilities
- **Research Agent:** Orchestrates the WisDev loop across canonical `/wisdev/*` routes.
- **Brain Capabilities:** Owns Go-native task DAG decomposition, hypothesis generation, and replanning.
- **RAG Engine:** High-performance retrieval-augmented generation using RAPTOR, BM25, and semantic re-ranking.
- **Search:** Executes parallel academic search across the registered provider set.
- **Evidence Gate:** Verifies synthesized claims against sources using AI extraction and token-overlap heuristics.
- **Persistence:** Manages persistent research sessions, policy, and user-tier state in PostgreSQL.

## Architecture
- **Language:** Go (Logic Core)
- **Database:** PostgreSQL (via pgxpool) + Redis for caching.
- **Protocols:** HTTP/JSON for API, plus adaptive Python sidecar RPC: local gRPC and remote HTTP/JSON.

## Environment Variables
- `PORT`: Listen port (default: 8081)
- `DATABASE_URL`: PostgreSQL connection string.
- `UPSTASH_REDIS_URL`: Redis/Upstash connection string.
- `PYTHON_SIDECAR_HTTP_URL`: Base URL of the Python sidecar HTTP service.
- `PYTHON_SIDECAR_LLM_TRANSPORT`: `grpc` for local/container overlays, `http-json` for remote overlays.
- `PYTHON_SIDECAR_GRPC_ADDR`: Address of the Python sidecar gRPC server for local/container overlays (default: `127.0.0.1:50052`).
- `GO_INTERNAL_GRPC_ADDR`: Internal Go gRPC listen address (default: `127.0.0.1:50053`).
- `INTERNAL_METRICS_PORT`: Internal metrics listener port (default: `9090`).
- `INTERNAL_SERVICE_KEY`: Optional shared secret for Go-to-Python sidecar calls.
- `SEARCH_API_KEYS`: Comma-separated list of provider keys.

## API Paths (Canonical)
- `/search/*`: Parallel search, hybrid search, query expansion, batch search, and tool search.
- `/wisdev/*`: Plan, decide, critique, execute, research, policy, feedback, and runtime helpers.
- `/rag/*`: Retrieval, RAPTOR, BM25, answer generation, and evidence grounding.
- `/paper/*` and `/papers/*`: PDF processing, paper metadata, and related/network views.
- `/full-paper/*`, `/drafting/*`, `/manuscript/*`, `/reviewer/*`: Long-form generation surfaces.
- `/vector/*`, `/query/*`, `/summarization/*`, `/source/*`, `/topic-tree/*`: Helper primitives.
- `/internal/*`: Service-to-service operational hooks such as account deletion audit.

## Compatibility Notes
- Canonical runtime contracts are unversioned.
- The Go router no longer publishes versioned runtime aliases. Any remaining versioned route mentions are limited to intentional negative tests, denylist guardrails, or archived migration notes, not stable public APIs.
- Any remaining `/wisdev/agent/*` handlers are compatibility residue inside Go, not canonical browser contracts.
- Citation promotion and verification are Go-owned in the open-source project.

---
name: wisdev-embed
description: "Use when the user wants to build on top of WisDev as a library or service rather than driving it as a CLI/MCP tool — embedding the Go agent in another program, calling the HTTP/gRPC API, or adding a custom search provider. Examples: \"embed wisdev in my Go app\", \"call the wisdev HTTP API\", \"add a custom search provider\", \"run wisdev as a server for another service to call\""
---

# Embedding and Serving WisDev

## When to Use

- The user is writing Go code that needs to *run* a WisDev research loop programmatically (not shelling out to the CLI).
- They want to call WisDev over HTTP/gRPC from another service.
- They want to inject a custom search provider instead of (or alongside) the built-ins.
- For interactive/one-shot terminal use, prefer [[wisdev-cli]]; for IDE-agent tool calls, prefer [[wisdev-mcp-research]].

## Go embedding API

The stable public surface is `orchestrator/pkg/wisdev/` (do not import `orchestrator/internal/...` from outside the module — that's unstable).

```go
import "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"

agent := wisdev.NewAgent(wisdev.WithNoSearchProviders())
result, err := agent.RunYOLO(context.Background(), wisdev.YOLORequest{
    Task: "map open source research agent evidence",
})
// result.FinalAnswer (string), result.Papers ([]Paper),
// result.ExecutedQueries, result.Hypotheses, result.Iterations, result.Converged, result.Gaps
```

Inject a custom provider with `wisdev.WithSearchProviders(myProvider)` instead of (or in addition to) `WithNoSearchProviders()`.

## Running as a server

```bash
cd wisdev-arc/orchestrator
go run ./cmd/server          # or: wisdev serve (from repo root)
```

Port from `$PORT` (default `8081`); gRPC on `:50053`. Call it from the CLI with `wisdev yolo --remote --url http://localhost:8081 "question"` (see [[wisdev-yolo]]), or directly over HTTP.

## HTTP route families

| Route family | Purpose |
|---|---|
| `/health`, `/healthz`, `/readiness`, `/metrics` | Health, readiness, Prometheus metrics |
| `/search/*` | Parallel, hybrid, batch, tool search |
| `/expand/*`, `/query/*` | Query expansion (aggressive, SPLADE) |
| `/wisdev/*` | Planning, sessions, guided/yolo execution, research, policy, traces |
| `/wisdev/job`, `/wisdev/job/{id}`, `/wisdev/job/{id}/stream`, `/wisdev/job/{id}/cancel` | YOLO job start/status/stream/cancel |
| `/agent/*` | A2A agent-gateway surface (`card`, `sessions`, `tools`, `registration-contract`) — distinct from the YOLO job routes above |
| `/rag/*` | RAG retrieve/hybrid/CRAG/agentic-hybrid |
| `/wisdev/rag/*` | RAG answer, section context, BM25, RAPTOR |
| `/paper/*`, `/papers/*` | PDF extraction, paper profile/count/related/network |
| `/export/*` | Markdown, HTML, LaTeX export |
| `/full-paper/*`, `/drafting/*`, `/manuscript/*`, `/reviewer/*` | DocGen and review (same engine as [[wisdev-docgen]]) |
| `/wisdev/mcp` | HTTP-transport MCP (vs. the stdio server in [[wisdev-mcp-research]]) |

Optional Python sidecar route families (`PYTHON_SIDECAR_HTTP_URL`): `/ml/pdf`, `/ml/embed`, `/ml/bm25/*`, `/llm/generate[/stream]`, `/llm/structured-output`, `/llm/embed[/batch]`. The agent degrades gracefully when the sidecar is absent.

## Custom search providers

Implement the provider interface in `orchestrator/internal/search/` for an in-tree addition, or inject one at embedding time via `wisdev.WithSearchProviders(...)` for an out-of-tree one. Check `wisdev sources`/`wisdevListProviders` for the existing provider set and their domain routing before adding a duplicate.

## Config

The runtime config actually wired into Go is `WISDEV_ADK_CONFIG` (default `config/wisdev-adk.yaml`), loaded by `ResolveADKConfigPath()`/`LoadADKRuntime()` in `orchestrator/internal/wisdev/adk_runtime.go` — it controls policy overrides, plugins, A2A, and HITL settings. (`config/wisdev.example.yaml` is referenced in docs/README but is not loaded by any Go code path — don't point users at `WISDEV_CONFIG`, it doesn't exist.) Model tier defaults resolve from the parent `scholar_models.json` (`SCHOLAR_MODELS_CONFIG` when running outside the ScholarLM checkout). See `docs/CLI.md` "Environment variables" for the full env var table (LLM provider, GCP/Vertex, sidecar, Temporal).

Full reference: README "Embedding API" / "API Surface" sections, `docs/MIGRATION_STATUS.md`.

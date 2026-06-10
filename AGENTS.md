# AGENTS.md — WisDev Research Runtime

Primary instruction file for coding agents (Claude Code, Codex, Cursor, etc.) working in
this repository. Read this before editing. For human-facing docs see `README.md`,
`CONTRIBUTING.md`, and `docs/`.

## What this repo is

WisDev Research Runtime is a terminal-first autonomous research agent stack: it plans,
executes, and synthesizes evidence-grounded research across academic sources
(Google ADK + MCP + Gemini on GCP). The CLI entrypoint is `orchestrator/cmd/wisdev`.

This tree is also maintained inside a larger parent application and published here as a
scrubbed open-source mirror. Treat this directory as the source of truth for the runtime;
do **not** add references to private parent-app internals. `adapters/scholarlm/` is
documentation-only — the boundary, not an integration.

## Runtime ownership (hard rules)

The runtime target is **Go plus optional Python only**:

- **Go owns** the agent, API, CLI, orchestration, search, RAG, evidence, policy,
  persistence, telemetry, and local validation. New runtime logic goes in Go.
- **Python is optional** and limited to ML / LLM / embedding / PDF / document-processing
  primitives in `sidecar/`. The agent must degrade gracefully when the sidecar is absent.
- **Rust is intentionally excluded** from this open-source project. Do not add Rust.

When unsure where code belongs: orchestration/decision logic → Go; a model/ML primitive
that genuinely needs Python libraries → `sidecar/`. Default to Go.

## Layout

```text
orchestrator/cmd/wisdev/          CLI entrypoint
orchestrator/cmd/server/          HTTP (:8081) / gRPC (:50053) server
orchestrator/pkg/wisdev/          Public Go embedding API (stable surface)
orchestrator/internal/wisdev/     YOLO + guided planning, execution, memory
orchestrator/internal/search/     Academic providers, routing, fan-out
orchestrator/internal/rag/        Retrieval, BM25, RAPTOR, evidence helpers
orchestrator/internal/evidence/   Citation and evidence utilities
orchestrator/internal/policy/     Go-local policy and validation
orchestrator/proto/               Go protobuf contracts
sidecar/                          Optional Python ML/LLM worker (:8090 / gRPC :50052)
config/                           Open-source config templates
docs/                             Migration status, CLI, MCP clients, release checklist
scripts/                          Local verification + build helpers
```

`orchestrator/pkg/wisdev/` is the public embedding API — keep it backward-compatible;
breaking changes need a deliberate call-out in the PR.

## Build, test, verify

Run verification before claiming a change works. Windows/PowerShell:

```powershell
.\scripts\verify.ps1 -StaticRelease   # static checks / release gate
.\scripts\verify.ps1 -Go              # Go build + tests
.\scripts\verify.ps1 -PythonContract  # Python sidecar contract tests
.\scripts\verify.ps1 -SmokeLocal      # offline YOLO smoke
```

Unix-like with `make`: `make test-go`, `make test-python-contract`, `make smoke-local`,
`make test-all`.

Direct Go tests from `orchestrator/`: `go test ./... -count=1`.
Offline smoke: `wisdev search --offline --max-iterations 1 "..."` (`--offline` keeps the
loop local with no external providers — use it for CI and quick checks).

## Conventions

- **Secrets/config:** never commit `.env`, credentials, journals, or `*_prm_rewards.jsonl`.
  `.gitignore` already covers these — keep it that way. Use `.env.example` for new vars.
- **Logging/observability:** structured logs with the documented trace fields
  (`service`, `runtime`, `component`, `operation`, `stage`, `trace_id`, `request_id`,
  `session_id`, `provider`, `latency_ms`, `result`, `error_code`). Prompts are redacted by
  default (`redact_prompts: true`) — don't log raw prompt content.
- **MCP tools:** `wisdevSearchPapers` is the canonical tool name; legacy
  `scholarlmSearchPapers` aliases remain accepted on `tools/call` — keep both working.
- **Models:** tier defaults come from `scholar_models.json`. Outside the parent checkout,
  set `SCHOLAR_MODELS_CONFIG` to the canonical path.
- Match the style and patterns of the file you're editing.

## GitNexus (code intelligence)

This sub-repo is indexed by GitNexus; the index lives in `.gitnexus/`. Skills are in
`.claude/skills/gitnexus/`. After code edits, refresh:

```powershell
.\scripts\gitnexus.ps1 index
.\scripts\gitnexus.ps1 status
```

Use `gitnexus context`/`impact` to understand call graphs and blast radius before
non-trivial edits, rather than grepping blind.

## Before opening a PR

1. `verify.ps1 -StaticRelease -Go -PythonContract -SmokeLocal` (or the `make` equivalents)
   pass.
2. No secrets, credentials, journals, or local research artifacts staged.
3. Public embedding API in `orchestrator/pkg/wisdev/` stays backward-compatible (or the
   break is called out).
4. No Rust, and no private parent-app references introduced.
5. Migration provenance noted in `docs/MIGRATION_STATUS.md` when relevant.

License: Apache-2.0 (`LICENSE`, `NOTICE`).

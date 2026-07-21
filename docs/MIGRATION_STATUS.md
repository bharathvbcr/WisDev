# Migration Status

**Last sync from ScholarLM (shared quality port): 2026-07-20**

### MCP client surface (2026-07-20 follow-up)
- Agent-oriented tool descriptions + `initialize.instructions`
- MCP prompts: `wisdev_literature_search`, `wisdev_evidence_check`, `wisdev_docgen`
- `wisdev setup --binary` writes absolute binary path, merges into existing `mcpServers`, PATH env hint
- Skills install: `scripts/install-skills.sh` → `~/.claude/skills`, `~/.cursor/skills`, parent `.agents`/`.claude`
- Docs: `docs/MCP_CLIENTS.md`, `.claude/skills/wisdev/README.md`

## Ownership Rules (Scholar → ARC selective port)

ScholarLM is the canonical master for **shared research/manuscript quality**. WisDev-ARC keeps **OSS packaging** and ARC-only surfaces. Port is allowlist + merge — never wholesale directory overwrite.

| Owner | Surfaces |
|-------|----------|
| **Scholar → port** | Manuscript pipeline quality, DocGen HTTP (`full_paper` / `yolo`), search rate-gate / Priority-5 HTTP, questioning, secrets helpers as needed, `manuscript_router` prompts |
| **ARC → keep** | `cli/`, `docgen/`, `citations/`, `cmd/wisdev`, `pkg/wisdev`, 4× `budget_unleashed.go` helpers, `stackconfig` v4 + ports, `policy/validation.go`, `failure_policy.go`, `session.go` / SessionManager, MCP DocGen injection, **`envload` (ARC ahead)** |
| **Never port** | Product routes (assist/grant/prisma/chat/grammar/SR/…), `rust_bridge`, Firestore `research_run.go`, wholesale `router.go` / gateway / stackconfig |

**Unleashed:** Scholar has zero `WISDEV_UNLEASHED` hooks. ARC keeps the four `budget_unleashed.go` helpers and call-site patches. This sync did **not** overwrite those helpers, so no re-apply was required. Smoke (2026-07-20): helpers present; hooks still in `research_profile`, `tree_loop`, `agent_swarm`, `gateway`, `autonomous`, `policy/engine` (caps 96/8/500), CLI `wisdev max`, etc.

---

## Ported in 2026-07-20 sync (from ScholarLM)

### Go — manuscript + DocGen HTTP
- Manuscript set: pipeline, checkpoint, coordination, logging, revise, sidecar, citation hygiene (+ tests)
- `full_paper_routes`, `yolo`, DocGen control/budget tests; `attached_sources`
- MCP: kept ARC DocGen injection; Scholar instructions where missing

### Go — Priority-5 search / API
- API: `paper_fulltext_resolve`, `provider_doi`, `provider_enrichment`, `search_quick_mode`, `citations_routes` (HTTP graph ≠ ARC `internal/citations`), `search_decision`, `search_plan`, `relevance`, `query_intent` (+ tests)
- Search deps: `query_policy`, `quick_mode`, `unpaywall`, `retractions`, `bibliometrics`, `doi_normalize`, ranking merges; optional `memcache`, `venue_prestige`
- Policy: `searchdecisions`, `quality`, `research_modes` (no `rust_bridge`)
- Router: P5 blocks only (no product routes); `NewWisDevHandler` keeps SessionManager

### Go — questioning + hypothesis
- `questioning_routes`: overwrite + branding scrub (“WisDev” adaptive assistant; no ScholarLM product copy)
- `wisdev_research_hypothesis`: ported with 3-arg register; **TierPro / subscription gate stripped** for OSS

### Python
- `sidecar/routers/manuscript_router.py` (+ gemini / pdf extraction as needed)
- ARC `wisdev_action_router.py` 501 stubs left intentional
- Excluded: `manuscript_orchestrator/**`

### Deleted / not revived
- Unregistered ARC `drafting.go` removed; do not revive `/drafting/*`

---

## Kept in ARC (not overwritten this pass)

- Entire `internal/cli/` (incl. `WISDEV_UNLEASHED=1` via `wisdev max`)
- `internal/docgen/`, `internal/citations/`
- 4× `budget_unleashed.go` (`wisdev`, `llm`, `policy`, `pycompute`)
- `envload`, `stackconfig`, gateway SessionManager, MCP DocGen wiring
- `policy/validation.go`, resilience `failure_policy.go`
- `api/internal_ops` / billing routes (left alone)
- Product-only Scholar packages and Firestore telemetry

---

## Verification (2026-07-20)

### Green
- `internal/wisdev` manuscript / attached-sources / checkpoint / MCP manuscript tests
- `internal/api` scoped: FullPaper, DocGen, YOLO, P5 routes (DOI/enrichment/search decision/plan/quick-mode/relevance/query-intent/citations), ResearchHypothesis
- `internal/policy` (incl. quality / research modes / search decisions)
- `internal/docgen`
- `internal/search` scoped P5 deps (query policy, quick mode, DOI, etc.) — excluding known registry flakes below
- `internal/cli` Max/Guide wiring (unleashed docs/env)
- Unleashed hooks still present (grep + helpers on disk); helpers not overwritten → no re-apply needed
- Python: `test_manuscript_router.py` **37 passed** + `test_manuscript_router_additional.py` **7 passed** (44 total)
- Indexes: `npm run gitnexus:analyze` refreshed (2026-07-20). Graphify `watch._rebuild_code` unavailable in this environment (vendored `graphify/` has no `watch.py`; no installable `graphify` wheel) — noted, not blocking.

### Known remaining flakes / pre-existing failures (not fixed this pass)
- **Search registry:** `TestRegistryGetCitationsAndSetRedis`, `TestRegistryGetReferences` → `no healthy citation graph providers found` (env / provider health)
- **API LLM-mock / questioning:** `TestRegisterQuestioningRoutes_WithSeededSession` subcases expect structured LLM mock responses but fall back to heuristics (`llm_unavailable` / mock EOF / gRPC `:50052` refused)
- **API synthesis (out of port scope):** `TestVerifyCitations_*` return 400 vs expected 200 when broad `-run Citation` matches — pre-existing, unrelated to P5 citations routes

---

## Implemented In Earlier Passes

- Created a fresh top-level `wisdev-arc` tree.
- Seeded `orchestrator/` from current `backend/go_orchestrator`, including current WisDev YOLO code and its dependent Go packages.
- Seeded `sidecar/` from current `backend/python_sidecar`, excluding generated coverage and local Python install artifacts.
- Rewrote Go import paths from the private parent module path to `github.com/bharathvbcr/wisdev-arc/orchestrator`.
- Added open-source config, environment template, Makefile, docker-compose, adapter boundary notes, and README.
- Promoted the first stable embedding facade at `orchestrator/pkg/wisdev`.
- Added `wisdev yolo --local` so the CLI can execute YOLO without an HTTP server.
- Added `--offline` for deterministic local smoke runs without network-backed search providers.
- Added public search provider injection with public `Paper`, `SearchOptions`, and `SearchProvider` types.
- **ScholarDoc DocGen port (2026-07):** headless document generation via `internal/docgen`
  and `internal/citations`. Three intents (`report`, `litreview`, `fullpaper`), seven
  citation styles, and export to markdown/latex/html/json/docx. Wired through CLI
  (`wisdev docgen`), TUI (DocGen toggle with intent/format/style cycling), MCP
  (`wisdevGenerateManuscript`), and the additive `pkg/wisdev.GenerateDocument` API.

## Canonical Source

Shared quality continues to flow **ScholarLM → ARC** for the ported packages above. ARC-owned packaging remains canonical here:

- `orchestrator/internal/cli`, `docgen`, `citations`
- `orchestrator/pkg/wisdev`
- Unleashed helpers + `envload` / `stackconfig`
- `sidecar/routers/wisdev_action_router.py` (stubs)

Research/manuscript quality mirrors live under:

- `orchestrator/internal/wisdev` (manuscript*)
- `orchestrator/internal/api` (full_paper, yolo, P5, questioning, hypothesis)
- `orchestrator/internal/search`, `policy`
- `sidecar/routers/manuscript_router.py`

## Still To Port / OSS follow-ups

- Expand `pkg/wisdev` beyond YOLO into guided mode, streaming events, LLM provider injection, and durable job execution.
- Replace direct cloud/infra dependencies with `StorageProvider`, `SecretProvider`, `LLMProvider`, and `ExecutionBackend`.
- Make SQLite the default durable store.
- Split optional GCP, Redis, Postgres, and Temporal features behind build tags or provider packages.
- Remove remaining private-app edge bridge code paths from the extracted Go runtime.
- Add release CI for Go, Python, generated proto checks, secret scanning, and license scanning.
- Optional later: `/export/docx` signature alignment; scrub `internal_ops` billing leftovers (explicit non-goal of this sync).

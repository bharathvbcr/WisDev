# WisDev Agent (Open Source)

A terminal-first, open-source AI research agent. WisDev plans, executes, and synthesizes deep research tasks across academic sources using a multi-agent architecture.

```
⚡ Query → 📋 Plan → 🔍 Search → 📄 Analyze → 📝 Synthesize → 📊 Report
```

## Table of Contents

- [Architecture](#architecture)
  - [System Overview](#system-overview)
  - [Component Detail](#component-detail)
  - [Data Flow](#data-flow)
  - [Agent Execution Loop](#agent-execution-loop)
- [Quick Start](#quick-start)
- [CLI Reference](#cli-reference)
- [Configuration](#configuration)
- [LLM Providers](#llm-providers)
- [Search Providers](#search-providers)
- [Storage Backends](#storage-backends)
- [API Reference](#api-reference)
- [Observability](#observability)
- [Development](#development)
- [Deployment](#deployment)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [License](#license)

---

## Architecture

### System Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                              CLI (cmd/wisdev)                        │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐              │
│  │   TUI Layer  │  │  Plain Text  │  │   JSON Out   │              │
│  │ (bubbletea)  │  │   (--no-tui) │  │   (--json)   │              │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘              │
│         └─────────────────┼──────────────────┘                       │
└───────────────────────────┼──────────────────────────────────────────┘
                            │
┌───────────────────────────▼──────────────────────────────────────────┐
│                       Go Orchestrator                                 │
│  ┌────────────┐  ┌──────────┐  ┌───────────┐  ┌──────────────────┐  │
│  │  Planning  │  │  Policy  │  │  Search   │  │  Autonomous Loop │  │
│  │  Engine    │  │  Engine  │  │ Registry  │  │  (Budgeted)      │  │
│  │            │  │          │  │           │  │                  │  │
│  │ • Query    │  │ • Token  │  │ • arXiv   │  │ • Iterative      │  │
│  │   analysis │  │   budget │  │ • PubMed  │  │   research       │  │
│  │ • Plan     │  │ • Rate   │  │ • CrossRef│  │ • Sufficiency    │  │
│  │   gen      │  │   limits │  │ • OpenAlex│  │   evaluation     │  │
│  │ • Session  │  │ • Guard- │  │ + 12 more │  │ • Evidence       │  │
│  │   mgmt     │  │   rails  │  │           │  │   synthesis      │  │
│  └─────┬──────┘  └─────┬────┘  └─────┬─────┘  └────────┬─────────┘  │
│        │               │             │                   │            │
│  ┌─────▼──────┐  ┌─────▼────┐  ┌─────▼─────┐  ┌────────▼─────────┐  │
│  │    RAG     │  │  State   │  │  Circuit  │  │   Runtime        │  │
│  │  Pipeline  │  │ Storage  │  │ Breakers  │  │   Journal        │  │
│  │            │  │          │  │           │  │                  │  │
│  │ • BM25     │  │ • In-    │  │ • Per-    │  │ • Event log      │  │
│  │ • Evidence │  │   Memory │  │   provider│  │ • Trace corr.    │  │
│  │   gate     │  │ • SQLite │  │ • Fail-   │  │ • Session state  │  │
│  │ • Raptor   │  │          │  │   over    │  │                  │  │
│  └────────────┘  └──────────┘  └───────────┘  └──────────────────┘  │
│                                                                       │
│  HTTP :8081  │  gRPC :50052  │  Metrics :9090                       │
└───────────────────────────────┬───────────────────────────────────────┘
                                │ gRPC (protobuf)
┌───────────────────────────────▼───────────────────────────────────────┐
│                        Python Sidecar                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐                │
│  │  LLM Gateway │  │  Semantic    │  │  PDF Extract │                │
│  │              │  │  Cache       │  │              │                │
│  │ • Generate   │  │              │  │ • Text       │                │
│  │ • Stream     │  │ • Embedding  │  │   extraction │                │
│  │ • Structured │  │   similarity │  │ • Layout     │                │
│  │   output     │  │ • LRU evict  │  │   analysis   │                │
│  │ • Embed      │  │ • TTL        │  │ • Table      │                │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘                │
│         │                 │                  │                        │
│  ┌──────▼───────┐  ┌──────▼───────┐  ┌───────▼──────┐                │
│  │  Skill       │  │  Embedding   │  │  Export      │                │
│  │  Registry    │  │  Service     │  │  Pipeline    │                │
│  │              │  │              │  │              │                │
│  │ • Dynamic    │  │ • Vector     │  │ • Markdown   │                │
│  │   skills     │  │   search     │  │ • HTML       │                │
│  │ • File-based │  │ • Cosine     │  │ • LaTeX      │                │
│  │   persist    │  │   sim.       │  │ • PDF        │                │
│  └──────────────┘  └──────────────┘  └──────────────┘                │
│                                                                       │
│  HTTP :8000  │  gRPC :50051                                          │
└───────────────────────────────────────────────────────────────────────┘
                                │
┌───────────────────────────────▼───────────────────────────────────────┐
│                     External Services                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐             │
│  │  LLM     │  │ Academic │  │  OTel    │  │  Local   │             │
│  │  APIs    │  │ Search   │  │  Collector│  │  Files   │             │
│  │          │  │ APIs     │  │           │  │          │             │
│  │ • OpenAI │  │ • arXiv  │  │ • Jaeger  │  │ • PDFs   │             │
│  │ • Gemini │  │ • PubMed │  │ • Tempo   │  │ • .docx  │             │
│  │ • Ollama │  │ + 13 more│  │ • Other   │  │ • .txt   │             │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘             │
└───────────────────────────────────────────────────────────────────────┘
```

### Component Detail

#### Go Orchestrator (`orchestrator/`)

The Go orchestrator is the brain of WisDev. It handles all planning, execution, search, and state management.

| Package | Purpose | Key Files |
|---------|---------|-----------|
| `api/` | HTTP handlers and route registration | `router.go`, `search.go`, `wisdev_v2.go` |
| `internal/wisdev/` | Core agent logic — planning, execution, autonomous loop | `gateway.go`, `autonomous.go`, `session_store.go` |
| `internal/search/` | Academic search across 15+ providers with circuit breakers | `provider.go`, `registry.go`, `circuit.go` |
| `internal/rag/` | RAG pipeline — BM25, evidence gating, Raptor | `engine.go`, `bm25.go`, `evidence_gate.go` |
| `internal/policy/` | Guardrails — token budgets, rate limits, content filters | `policy.go`, `enforcer.go` |
| `internal/llm/` | LLM client abstraction over gRPC sidecar | `client.go` |
| `internal/storage/` | Session storage — in-memory and SQLite backends | `provider.go` |
| `internal/resilience/` | Circuit breakers, degraded mode, secret management | `circuit.go`, `resilience.go`, `secrets.go` |
| `internal/telemetry/` | OpenTelemetry instrumentation with OTLP exporters | `otel.go`, `logger.go`, `metrics.go` |
| `internal/paper/` | Paper profiling, PDF analysis, citation networks | `profiler.go` |

#### Python Sidecar (`sidecar/`)

The Python sidecar handles LLM integrations, prompt engineering, and ML primitives.

| Module | Purpose | Key Files |
|--------|---------|-----------|
| `services/gemini_service.py` | LLM gateway — supports OpenAI-compatible endpoints | `gemini_service.py` |
| `services/semantic_cache.py` | Embedding-based cache with LRU eviction | `semantic_cache.py` |
| `services/dynamic_skill_registry.py` | File-based skill persistence | `dynamic_skill_registry.py` |
| `routers/` | FastAPI route groups for ML, PDF, skills | `ml_router.py`, `pdf_router.py` |
| `models/` | Pydantic request/response schemas | `requests.py`, `responses.py` |
| `prompts/` | AI prompt templates for research tasks | `*.j2` files |
| `storage.py` | Python-side storage provider (mirror of Go) | `storage.py` |
| `telemetry.py` | OTel tracing with structlog correlation | `telemetry.py` |

#### Protobuf (`proto/`)

Shared gRPC definitions for type-safe communication between Go and Python.

| Proto File | Purpose |
|------------|---------|
| `wisdev_v2.proto` | WisDev agent protocol — sessions, plans, execution, observation |
| `llm_v1.proto` | LLM service protocol — generate, stream, embed, structured output |

### Data Flow

```
User Query
    │
    ▼
┌─────────────────────────────────────────┐
│  1. Query Analysis                       │
│  • Complexity scoring                    │
│  • Domain classification                 │
│  • Clarification budget allocation       │
└──────────────────┬──────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────┐
│  2. Questioning Phase (if guided mode)   │
│  • Adaptive question generation          │
│  • User clarification                    │
│  • Query refinement                      │
└──────────────────┬──────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────┐
│  3. Planning Phase                       │
│  • Research plan generation              │
│  • Search strategy selection             │
│  • Provider routing                      │
└──────────────────┬──────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────┐
│  4. Search Phase                         │
│  • Parallel search across providers      │
│  • Result deduplication                  │
│  • Quality scoring                       │
└──────────────────┬──────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────┐
│  5. Analysis Phase                       │
│  • Paper profiling                       │
│  • Evidence extraction                   │
│  • RAG pipeline (BM25 + embedding)       │
└──────────────────┬──────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────┐
│  6. Synthesis Phase                      │
│  • Answer generation                     │
│  • Citation verification                 │
│  • Evidence gate                         │
└──────────────────┬──────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────┐
│  7. Output Phase                         │
│  • Report generation                     │
│  • Export (Markdown/HTML/LaTeX/PDF)      │
│  • Session persistence                   │
└─────────────────────────────────────────┘
```

### Agent Execution Loop

The autonomous research loop is a budgeted iterative process:

```
┌──────────────────────────────────────────────────────────────┐
│                    Autonomous Research Loop                   │
│                                                               │
│  ┌─────────────┐                                             │
│  │  Iteration 1 │                                             │
│  │              │                                             │
│  │  1. Search   │────┐                                        │
│  │     (parallel│    │                                        │
│  │     across   │    │   ┌─────────────────────────────┐     │
│  │     N prov.) │    │   │   Sufficiency Evaluation    │     │
│  │              │    │   │                              │     │
│  │  2. Analyze  │    │   │  • Coverage score            │     │
│  │     papers   │    └──►│  • Evidence quality          │     │
│  │              │        │  • Gap identification        │     │
│  │  3. Synthesize│       │  • Novelty assessment        │     │
│  │     findings │        │                              │     │
│  │              │    ┌──►│  ┌─────────┐    ┌─────────┐│     │
│  │  4. Evaluate │    │   │  Sufficient│    │Continue ││     │
│  │     coverage │    │   │  → Output  │    │→ Next   ││     │
│  └─────────────┘    │   │  Report    │    │Iter.    ││     │
│                     │   └─────────┘    └─────────┘│     │
│  ┌─────────────┐    │                              │     │
│  │  Iteration 2 │    │   ┌──────────────────────┐  │     │
│  │  ...         │    │   │  Budget Check         │  │     │
│  │              │    │   │  • Max iterations     │  │     │
│  └─────────────┘    │   │  • Token budget       │  │     │
│                     │   │  • Time limit         │  │     │
│        ...          │   └──────────────────────┘  │     │
│                     │                              │     │
│  ┌─────────────┐    └──────────────────────────────┘     │
│  │  Iteration N │                                         │
│  │  (final)     │                                         │
│  │              │                                         │
│  │  → Final     │                                         │
│  │    Report    │                                         │
│  └─────────────┘                                         │
└──────────────────────────────────────────────────────────────┘
```

Each iteration:
1. **Searches** across multiple academic providers in parallel
2. **Analyzes** papers for relevance, quality, and novelty
3. **Synthesizes** findings into a coherent narrative
4. **Evaluates** whether the research is sufficient or needs more iterations
5. **Checks** budget constraints (max iterations, token budget, time limit)

---

## Quick Start

### 1. Clone and Configure

```bash
git clone https://github.com/wisdev-agent/wisdev-agent-os.git
cd wisdev-agent-os
cp config/wisdev.example.yaml wisdev.yaml
```

### 2. Configure Your LLM

Edit `wisdev.yaml`:

```yaml
llm:
  provider: openai
  model: gpt-4o
  api_key: ${WISDEV_LLM_API_KEY}
  # For local Ollama:
  # base_url: "http://localhost:11434/v1"
  # model: "llama3.1"
```

### 3. Run with Docker Compose

```bash
docker compose up -d
```

Starts orchestrator, sidecar, and Jaeger.

### Kubernetes

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wisdev-orchestrator
spec:
  replicas: 2
  selector:
    matchLabels:
      app: wisdev-orchestrator
  template:
    spec:
      containers:
      - name: orchestrator
        image: wisdev-agent-os/orchestrator:latest
        ports:
        - containerPort: 8081
        - containerPort: 50052
        env:
        - name: WISDEV_CONFIG
          value: /app/config/wisdev.yaml
        - name: SIDECAR_HOST
          value: wisdev-sidecar
        - name: SIDECAR_PORT
          value: "8000"
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: jaeger:4317
        volumeMounts:
        - name: config
          mountPath: /app/config
      volumes:
      - name: config
        configMap:
          name: wisdev-config
```

### Bare Metal

```bash
# Build
make build

# Run server
./bin/wisdev-server --config wisdev.yaml

# Run CLI
./bin/wisdev run -q "your query"
```

---

## Troubleshooting

### Common Issues

| Issue | Cause | Solution |
|-------|-------|----------|
| `config error: no such file` | Missing config file | Run `wisdev config init` |
| `LLM API key is not set` | Missing API key | Set `WISDEV_LLM_API_KEY` env var |
| `storage error` | Invalid storage path | Use `type: memory` or valid SQLite path |
| `research failed: context deadline exceeded` | LLM timeout | Increase timeout or check LLM endpoint |
| `search backend unavailable` | All providers failing | Check network, verify provider URLs |

### Debug Mode

```bash
# Enable verbose logging
export LOG_LEVEL=debug

# Run with debug output
wisdev run -q "query" --no-tui
```

### Health Checks

```bash
# Check server health
curl http://localhost:8081/healthz

# Check sidecar health
curl http://localhost:8000/health

# Check Jaeger
curl http://localhost:16686
```

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed contribution guidelines.

### Quick Start for Contributors

```bash
# 1. Fork and clone
git clone https://github.com/YOUR_USERNAME/wisdev-agent-os.git
cd wisdev-agent-os

# 2. Set up development environment
cp config/wisdev.example.yaml wisdev.yaml
make build
make test

# 3. Make your changes
# ... edit code ...

# 4. Run tests
make test-race

# 5. Commit and push
git add .
git commit -m "feat: add new search provider"
git push origin main
```

### Code Style

#### Go
- Follow `gofmt` formatting
- Use `go vet` before committing
- Table-driven tests with descriptive subtest names
- Document all exported functions, types, and constants

#### Python
- Follow PEP 8
- Run `ruff check .` before committing
- Type hints on all function signatures
- Docstrings for public functions and classes

### Commit Messages

Use conventional commit format:

```
feat: add SQLite storage provider
fix: handle empty query in semantic cache
docs: update README with Ollama setup
test: add policy engine edge cases
```

---

## License

[To be determined — see LICENSE file]

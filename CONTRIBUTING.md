# Contributing to WisDev Agent

Thank you for your interest in contributing! This guide covers the essentials for getting started.

## Development Setup

### Prerequisites

- Go 1.26+
- Python 3.11+
- Docker & Docker Compose
- `buf` CLI (for protobuf generation)

### Quick Start

```bash
git clone https://github.com/wisdev-agent/wisdev-agent-os.git
cd wisdev-agent-os
cp config/wisdev.example.yaml wisdev.yaml
# Edit wisdev.yaml with your LLM API key
make build
```

## Project Structure

```
wisdev-agent-os/
├── cmd/wisdev/          # CLI entrypoint
├── orchestrator/        # Go service (planning, policy, execution)
│   ├── internal/        # Internal packages
│   │   ├── wisdev/      # Core agent logic
│   │   ├── search/      # Academic search providers
│   │   ├── rag/         # RAG pipeline
│   │   ├── policy/      # Guardrails and policy engine
│   │   ├── llm/         # LLM client abstraction
│   │   ├── telemetry/   # OpenTelemetry instrumentation
│   │   └── storage/     # Session storage (memory/SQLite)
│   └── api/             # HTTP/gRPC handlers
├── sidecar/             # Python service (LLM, ML primitives)
│   ├── services/        # LLM, cache, skill registry
│   ├── models/          # Pydantic request/response models
│   └── prompts/         # AI prompt templates
└── proto/               # Shared protobuf definitions
```

## Making Changes

### 1. Protobuf Changes

If you modify `.proto` files, regenerate stubs:

```bash
make proto
```

### 2. Go Changes

```bash
cd orchestrator
go mod tidy
go vet ./...
go test ./...
```

### 3. Python Changes

```bash
cd sidecar
pip install -r requirements.txt
ruff check .
pytest
```

### 4. Full Stack

```bash
docker compose up --build
```

## Testing

- **Go**: Write table-driven tests. Use `t.Parallel()` where possible.
- **Python**: Use `pytest` with `pytest-asyncio` for async tests.
- **Integration**: Test the full Plan-Execute-Observe loop with a mock LLM endpoint.

## Code Style

### Go
- Follow `gofmt` formatting
- Use `go vet` before committing
- Table-driven tests with descriptive subtest names
- Document all exported functions, types, and constants

### Python
- Follow PEP 8
- Run `ruff check .` before committing
- Type hints on all function signatures
- Docstrings for public functions and classes

## Commit Messages

Use conventional commit format:

```
feat: add SQLite storage provider
fix: handle empty query in semantic cache
docs: update README with Ollama setup
test: add policy engine edge cases
```

## Pull Requests

1. Create a feature branch from `main`
2. Make your changes with tests
3. Ensure CI passes (lint, test, proto check, docker build)
4. Open a PR with a clear description of the change

## Architecture Decisions

### Why Go + Python?

Go handles orchestration, policy, search, and state management efficiently. Python manages LLM integrations, prompt engineering, and ML primitives where the ecosystem is strongest.

### Storage Abstraction

The `StorageProvider` interface (Go: `internal/storage/`, Python: `storage.py`) allows swapping between in-memory (ephemeral CLI) and SQLite (durable) backends without changing business logic.

### Observability

Standard OpenTelemetry with OTLP exporters means you can use Jaeger, Tempo, or any OTLP-compatible collector. No vendor lock-in.

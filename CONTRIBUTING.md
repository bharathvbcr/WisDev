# Contributing

WisDev Agent OS is Go-first, with an optional Python sidecar for bounded ML,
LLM, and document-processing primitives.

## Development Checks

Run these before opening a pull request:

```powershell
.\scripts\verify.ps1 -Go -PythonContract -SmokeLocal
.\scripts\verify.ps1 -StaticRelease
```

On Unix-like systems, use `make test-go`, `make test-python-contract`, and
`make smoke-local`.

If you change protobufs, regenerate both Go and Python stubs in the same
change and include the generated artifacts.

## Ownership Rules

- Put agent orchestration, planning, search fan-out, RAG, evidence handling,
  policy, API handlers, and CLI behavior in `orchestrator/`.
- Put optional ML, LLM, embedding, and document worker logic in `sidecar/`.
- Do not add Rust to this open-source project.
- Keep private app integrations behind `adapters/`; do not make them part of
  the default runtime.

## Secrets

Do not commit credentials, API keys, service account JSON, tokens, local
journals, or `.env` files. Add placeholder names to `.env.example` instead.

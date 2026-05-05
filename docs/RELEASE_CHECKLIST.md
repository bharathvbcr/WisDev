# Release Checklist

Use this checklist before publishing WisDev Agent OS as a standalone
open-source repository.

## Required

- Confirm `LICENSE`, `NOTICE`, `SECURITY.md`, `CONTRIBUTING.md`, and
  `.env.example` are present at the repository root.
- Run `.\scripts\verify.ps1 -Go`.
- Run `.\scripts\verify.ps1 -PythonContract`.
- Run `.\scripts\verify.ps1 -SmokeLocal`.
- Run `.\scripts\verify.ps1 -StaticRelease`.
- On Unix-like systems, the equivalent targets are `make test-go`,
  `make test-python-contract`, and `make smoke-local`.
- Confirm `rg -n "paperclip|stripe|billing" orchestrator sidecar config adapters README.md .env.example` has no active runtime hits.
- Confirm `rg -n "(?i)rust_gateway|compute_rust|\\brust\\b" orchestrator sidecar config adapters README.md .env.example` only finds the no-Rust stack-contract guard.
- Confirm `.env`, local journals, generated caches, service-account JSON, and
  coverage artifacts are absent from the commit.
- Confirm Docker contexts have `.dockerignore` files and default Python
  requirements do not contain unpinned VCS dependencies.

## Compatibility

- Keep `scholarlmSearchPapers` only as a compatibility alias until adapters
  have moved to `wisdevSearchPapers`.
- Keep `adapters/scholarlm/` documentation-only unless the private parent app
  adds an explicit adapter package.
- Document any removed private endpoint in `docs/MIGRATION_STATUS.md`.

## Packaging

- Verify `docker compose config`.
- Verify `docker compose build orchestrator`.
- Verify the root GitHub Actions workflow runs from repository root after this
  folder is pushed as its own repository.

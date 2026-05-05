# WisDev Python ML Sidecar

Specialized ML worker service for heavy compute primitives. Stateless and optimized for Cloud Run.

## Responsibilities
- **PDF Extraction:** Extracts structured text and metadata from academic PDFs using PyMuPDF.
- **Embeddings:** High-throughput text embedding generation.
- **LLM Serving:** Provides canonical LLM generation routes over HTTP and keeps local gRPC for same-stack sidecar hops.
- **WisDev Sidecar Primitives:** Exposes internal compute/helper routes used by Go while Go remains the public WisDev contract owner.
- **Skill Registry:** Supports dynamic skill registration for runtime discovery.

## Architecture
- **Language:** Python (ML Worker)
- **Framework:** FastAPI
- **Protocol:** HTTP for primitives and remote LLM RPCs, gRPC for local/container sidecar LLM RPCs.

## Production notes
- Keep Cloud Run on `min-instances=0` if you want scale-to-zero cost behavior.
- Prefer `startup CPU boost` over warm instances for cold-start mitigation.
- Keep Cloud Run timeouts above app-owned LLM latency budgets so the request budget fails first, not the platform ceiling.
- Use bounded concurrency for the sidecar in production so cold-start recovery does not pile too many LLM requests onto a single fresh instance.

## Environment Variables
- `PORT`: HTTP listen port passed to Uvicorn. The container entrypoint defaults to `8090`.
- `GOOGLE_CLOUD_PROJECT`: Project ID for Vertex AI and Cloud Trace.
- `GOOGLE_API_KEY`: Optional direct Gemini API key. If unset, the sidecar now looks for `GOOGLE_API_KEY` or `GEMINI_API_KEY` in Google Secret Manager.
- `GOOGLE_API_KEY_SECRET_NAME`: Optional explicit Secret Manager secret name for the Gemini API key.
- `GEMINI_API_KEY_SECRET_NAME`: Optional alias for the same Secret Manager override.
- `PYTHON_SIDECAR_GRPC_ADDR`: gRPC bind address override (default: `127.0.0.1:50052`).
- `GRPC_PORT`: Fallback gRPC port when `PYTHON_SIDECAR_GRPC_ADDR` is not set (default: `50052`).
- `PYTHON_SIDECAR_DISABLE_GRPC`: Emergency-only switch to skip gRPC startup and run HTTP primitives only.
- `INTERNAL_SERVICE_KEY`: Optional shared secret for trusted Go-to-Python calls.
- `OIDC_AUDIENCE`: Optional audience for service-to-service ID token verification.
- `UPSTASH_REDIS_URL`: Optional Redis/Upstash URL for semantic cache and rate limiting.
- `VERTEX_FUNCTION_URL`: (Legacy) URL for cloud-proxied Gemini calls.
- `GEMINI_RUNTIME_MODE`: Runtime selector for Gemini calls. `auto` is fine for mixed workloads, but structured JSON routes now require the native Gemini/Vertex runtime and will reject `vertex_proxy`.
- `WISDEV_MODEL_CONFIG`: Optional path to the model-tier JSON config. Defaults fall back to `wisdev_models.json` and then built-in tier defaults.
- `AI_NATIVE_STRUCTURED_ENABLED`: Keep this enabled for `generate_json(...)` and other schema-backed routes. Defaults to `true`; turning it off now causes structured callers to fail fast instead of proxy-parsing JSON.
- `SKIP_GCP_SECRET_MANAGER`: Set to `true` to disable Secret Manager lookup and rely on process env only.

## Local Environment
- Prefer a repo-local environment at `sidecar/.venv`.
- Bootstrap or refresh it with `python -m venv .venv`, then install `requirements.txt` from this directory.
- Root verification scripts prefer this sidecar directory and fall back to the system launcher only when a local environment does not exist.
- The PDF metadata LLM fallback can use `langextract` when installed separately. It is intentionally not pinned in `requirements.txt`; the service falls back to regex extraction when the package is absent.

## Protobuf / gRPC Parity
- Checked-in gRPC stubs should match the `grpcio` / `grpcio-tools` pins in `requirements.txt`.
- Checked-in protobuf message stubs record the minimum supported protobuf runtime version; the pinned `protobuf` runtime may be newer, but it must remain compatible with the generated headers.
- Regenerate stubs with `gen_proto_stubs.ps1`, which prefers the repo-local `sidecar/.venv` and uses `python -m grpc_tools.protoc` so the generated headers stay aligned with the sidecar runtime pins.
- The sidecar exposes protobuf/gRPC runtime diagnostics in `/health`, `/readiness`, and `/`, and it fails startup when gRPC is enabled but the checked-in stubs cannot be imported cleanly.

## Gemini Credential Resolution
- Startup now primes `GOOGLE_API_KEY` before the FastAPI app imports Gemini consumers.
- Resolution order is: `GOOGLE_API_KEY` env, `GEMINI_API_KEY` env, `GOOGLE_API_KEY_SECRET_NAME` / `GEMINI_API_KEY_SECRET_NAME`, then default Secret Manager secrets named `GOOGLE_API_KEY` or `GEMINI_API_KEY`.
- Health and root metadata report the non-sensitive credential source/status only; the secret value is never exposed.
- Structured output is native-only now: schema-backed routes use the `google-genai` client against Vertex/Gemini controlled generation and fail fast when the runtime resolves to proxy-only mode.

## Endpoints
- `POST /ml/pdf`: Extract text from base64 PDF.
- `POST /ml/embed`: Generate embedding vector for text.
- `POST /ml/bm25/index`, `POST /ml/bm25/search`, `DELETE /ml/bm25`: Local BM25 helper surface.
- `POST /llm/generate`, `POST /llm/generate/stream`, `POST /llm/structured-output`: Canonical LLM generation HTTP surface.
- `POST /llm/embed`, `POST /llm/embed/batch`, `GET /llm/health`: Canonical remote LLM helper surface.
- `GET /wisdev/agent/card`, `GET /wisdev/deep-agents/capabilities`, `POST /wisdev/deep-agents/execute`: internal sidecar helpers retained for Go-owned orchestration and migration compatibility.
- `POST /skills/register`: Dynamic skill registration.
- `gRPC :50052`: LLM service implementation for local/container overlays.

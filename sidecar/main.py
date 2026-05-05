"""
WisDev API - Python sidecar
Backend for WisDev Agent OS handling compute-heavy primitives.
The Go orchestrator owns product orchestration; this service stays stateless.

Sidecar Endpoints:
- /llm/*     — Canonical remote LLM RPC surface (HTTP)
- /ml/pdf   — PDF extraction (HTTP)
- /ml/embed — Text embeddings (HTTP)
- /skills/register — retained dynamic skill registration (HTTP)
- gRPC      — Local/container LLM sidecar transport (port 50052)
"""

import asyncio
import json
import os
import threading
import time
from contextlib import asynccontextmanager
from contextlib import suppress
from functools import lru_cache
from pathlib import Path
from typing import Any, AsyncGenerator
from uuid import uuid4

# ── Observability bootstrap (MUST precede all other imports that log/trace) ───
# configure_telemetry() registers the OTel TracerProvider and reconfigures
# structlog with GCP trace correlation before any request handling begins.
from telemetry import configure_telemetry

_SERVICE_VERSION = "1.1.0"
configure_telemetry(service_version=_SERVICE_VERSION)

from services.secret_manager import (
    get_google_api_key_resolution,
    get_project_id_resolution,
    prime_google_api_key,
)

_GEMINI_API_KEY_RESOLUTION = prime_google_api_key()

# ── Application imports (after telemetry is wired) ───────────────────────────
import structlog
import grpc
from fastapi import FastAPI, HTTPException, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from slowapi import _rate_limit_exceeded_handler
from slowapi.errors import RateLimitExceeded

from limiter_config import limiter
from proto import (
    get_proto_runtime_diagnostics,
    llm_pb2,
    llm_pb2_grpc,
    require_proto_runtime_compatibility,
)
from routers.llm_router import router as llm_router
from routers.ml_router import router as ml_router
from routers.agent_router import router as agent_router
from routers.azure_compute_router import router as azure_compute_router
from routers.wisdev_action_router import router as wisdev_action_router
from services.dynamic_skill_registry import router as skill_registry_router
from services.gemini_service import GeminiService, get_gemini_runtime_diagnostics
from services.semantic_cache import semantic_cache
from stack_contract import ENDPOINTS_MANIFEST_VERSION
from stack_manifest import (
    current_overlay_name,
    resolve_env,
    resolve_listen_port,
    validate_service,
)

logger = structlog.get_logger(__name__)
validate_service("python_sidecar")

_grpc_thread: threading.Thread | None = None
_grpc_failed = threading.Event()


def _positive_float_env(name: str, default: float) -> float:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = float(raw)
    except ValueError:
        logger.warning(
            "invalid_float_env",
            name=name,
            value=raw,
            default=default,
        )
        return default
    if value <= 0:
        logger.warning(
            "invalid_float_env",
            name=name,
            value=raw,
            default=default,
        )
        return default
    return value


_GRPC_HEALTH_PROBE_TIMEOUT_SECONDS = 3.0
_GRPC_STARTUP_TIMEOUT_SECONDS = _positive_float_env(
    "PYTHON_SIDECAR_GRPC_STARTUP_TIMEOUT_SECONDS",
    90.0,
)
_GRPC_RETRY_INTERVAL_SECONDS = 0.25
_ARTIFACT_SCHEMA_VERSION = "artifacts-v1"
_COLD_START_WINDOW_MS = 90_000
_SIDECAR_START_MONOTONIC = time.monotonic()
_WARMUP_STATE_LOCK = threading.Lock()
_warmup_state: dict[str, Any] = {
    "state": "starting",
    "firstCallReady": False,
    "detail": "",
    "lastProbeAtMs": 0,
    "probeEnabled": True,
}


def _grpc_disabled() -> bool:
    return os.environ.get("PYTHON_SIDECAR_DISABLE_GRPC", "").strip().lower() in {
        "1",
        "true",
        "yes",
        "on",
    }


def _warm_probe_enabled() -> bool:
    raw = os.environ.get("PYTHON_SIDECAR_WARM_PROBE", "true").strip().lower()
    return raw not in {"0", "false", "no", "off"}


def _startup_age_ms() -> int:
    return int((time.monotonic() - _SIDECAR_START_MONOTONIC) * 1000)


def _cold_start_suspected() -> bool:
    return _startup_age_ms() < _COLD_START_WINDOW_MS


def _update_warmup_state(**updates: Any) -> None:
    with _WARMUP_STATE_LOCK:
        _warmup_state.update(updates)


def _warmup_snapshot(*, grpc_ready: bool) -> dict[str, Any]:
    diagnostics = get_gemini_runtime_diagnostics()
    with _WARMUP_STATE_LOCK:
        snapshot = dict(_warmup_state)
    snapshot.update(
        {
            "startupAgeMs": _startup_age_ms(),
            "coldStartSuspected": _cold_start_suspected(),
            "grpcReady": grpc_ready,
            "runtimeMode": diagnostics.get("mode", "auto"),
            "credentialSource": diagnostics.get("source", "none"),
        }
    )
    return snapshot


async def _run_gemini_warm_probe() -> None:
    probe_enabled = _warm_probe_enabled()
    _update_warmup_state(probeEnabled=probe_enabled)
    if not probe_enabled:
        _update_warmup_state(
            state="skipped",
            firstCallReady=False,
            detail="warm probe disabled",
            lastProbeAtMs=_startup_age_ms(),
        )
        logger.info(
            "gemini_warm_probe_skipped",
            startup_age_ms=_startup_age_ms(),
            warmup_state="skipped",
            cold_start_suspected=_cold_start_suspected(),
        )
        return

    runtime = get_gemini_runtime_diagnostics()
    _update_warmup_state(
        state="warming",
        firstCallReady=False,
        detail="warming",
        lastProbeAtMs=_startup_age_ms(),
    )
    logger.info(
        "gemini_warm_probe_start",
        startup_age_ms=_startup_age_ms(),
        warmup_state="warming",
        runtime_mode=runtime.get("mode", "auto"),
        credential_source=runtime.get("source", "none"),
        cold_start_suspected=_cold_start_suspected(),
    )

    try:
        svc = GeminiService()
        # Use the full warm_up() method which actually exercises the SDK path
        # (ADC token acquisition, connection pool, model routing) rather than
        # just checking if credentials exist. This absorbs 2-5s of cold-start
        # latency before real user requests arrive.
        warm_result = await svc.warm_up(timeout_s=15.0)
        ready = bool(warm_result.get("ready"))
        state = "ready" if ready else "degraded"
        detail = (
            ""
            if ready
            else str(
                warm_result.get("error")
                or warm_result.get("detail")
                or "gemini warm-up incomplete"
            )
        )
        _update_warmup_state(
            state=state,
            firstCallReady=bool(ready),
            detail=detail,
            lastProbeAtMs=_startup_age_ms(),
            warmUpLatencyMs=warm_result.get("latency_ms", 0),
        )
        logger.info(
            "gemini_warm_probe_complete",
            startup_age_ms=_startup_age_ms(),
            warmup_state=state,
            warm_up_latency_ms=warm_result.get("latency_ms", 0),
            warm_up_transport=warm_result.get("transport", "unknown"),
            runtime_mode=runtime.get("mode", "auto"),
            credential_source=runtime.get("source", "none"),
            grpc_ready=True,
            cold_start_suspected=_cold_start_suspected(),
        )
    except Exception as exc:
        _update_warmup_state(
            state="degraded",
            firstCallReady=False,
            detail=str(exc),
            lastProbeAtMs=_startup_age_ms(),
        )
        logger.warning(
            "gemini_warm_probe_failed",
            startup_age_ms=_startup_age_ms(),
            warmup_state="degraded",
            runtime_mode=runtime.get("mode", "auto"),
            credential_source=runtime.get("source", "none"),
            grpc_ready=True,
            cold_start_suspected=_cold_start_suspected(),
            error=str(exc),
        )


@lru_cache(maxsize=1)
def _load_wisdev_artifact_schema_document() -> dict:
    schema_path = (
        Path(__file__).resolve().parents[1].parent
        / "schema"
        / "artifact_schema_v1.json"
    )
    with schema_path.open("r", encoding="utf-8") as handle:
        return json.load(handle)


def get_wisdev_artifact_schema_profile() -> dict:
    bundles = [
        "paperBundle",
        "citationBundle",
        "reasoningBundle",
        "claimEvidenceArtifact",
    ]
    version = _ARTIFACT_SCHEMA_VERSION
    try:
        schema_doc = _load_wisdev_artifact_schema_document()
        version = schema_doc.get("version") or version
        properties = schema_doc.get("properties") or {}
        schema_bundles = sorted(
            key
            for key in properties
            if key not in {"action", "schemaVersion", "artifacts"}
        )
        if schema_bundles:
            bundles = schema_bundles
    except OSError:
        pass

    return {
        "version": version,
        "bundles": bundles,
        "legacyKeys": [
            "papers",
            "citations",
            "canonicalSources",
            "verifiedRecords",
            "branches",
            "reasoningVerification",
            "claimEvidenceTable",
        ],
    }


def get_wisdev_runtime_profile() -> dict:
    gemini_api_key = get_google_api_key_resolution()
    gemini_runtime = get_gemini_runtime_diagnostics()
    deepagents_profile = {
        "enabled": False,
        "backend": "deepagents",
    }
    try:
        from services.deepagents_service import get_deepagents_capabilities

        caps = get_deepagents_capabilities()
        deepagents_profile = {
            "enabled": True,
            **caps,
        }
    except Exception:
        pass

    gemini_runtime_profile = {
        "status": gemini_runtime["status"],
        "source": gemini_runtime["source"],
        "mode": gemini_runtime["mode"],
    }
    if "ready" in gemini_runtime:
        gemini_runtime_profile["ready"] = bool(gemini_runtime["ready"])
    if gemini_runtime.get("detail"):
        gemini_runtime_profile["detail"] = str(gemini_runtime["detail"])

    return {
        "executionModes": ["guided", "yolo"],
        "defaultExecutionMode": os.environ.get(
            "WISDEV_DEFAULT_EXECUTION_MODE", "guided"
        ),
        "academicIntegrity": {
            "requireCanonicalBibliography": True,
            "requirePrimarySourceForScientificClaims": True,
            "requireSnippetVerification": True,
        },
        "compute": {
            "orchestrationLanguage": "go",
            "securityBoundaryLanguage": "go",
            "cognitionLanguage": "python",
        },
        "proto": get_proto_runtime_diagnostics(),
        "geminiApiKey": {
            "status": gemini_api_key["status"],
            "source": gemini_api_key["source"],
        },
        "geminiRuntime": gemini_runtime_profile,
        "artifactSchema": get_wisdev_artifact_schema_profile(),
        "deepagents": deepagents_profile,
    }


# ── gRPC LLM sidecar ─────────────────────────────────────────────────────────


def _start_grpc_server() -> None:
    """Start the gRPC LLM sidecar server in a background thread.

    The OTel gRPC server interceptor (from telemetry.make_grpc_server_interceptor)
    is injected here so that inbound gRPC calls from Go carry the trace context
    through to Python spans.
    """
    try:
        from grpc_server import serve
        from telemetry import make_grpc_server_interceptor

        interceptor = make_grpc_server_interceptor()
        serve(interceptors=[interceptor] if interceptor else [])
    except Exception as exc:
        _grpc_failed.set()
        logger.error("grpc_server_failed", detail=str(exc))
        # If gRPC fails after we've already passed startup checks,
        # we must exit so the process manager (Cloud Run) restarts us.
        # os._exit is used to bypass any cleanup that might hang.
        import os

        os._exit(1)


def _grpc_sidecar_address() -> str:
    return resolve_env("PYTHON_SIDECAR_GRPC_ADDR") or "127.0.0.1:50052"


def _runtime_envelope(status: str, dependencies: list[dict[str, str]]) -> dict:
    return {
        "service": "python_sidecar",
        "status": status,
        "manifestVersion": ENDPOINTS_MANIFEST_VERSION,
        "environment": current_overlay_name(),
        "dependencies": dependencies,
        "transport": "http-json+grpc-protobuf",
        "latencyMs": 0,
        "lastCheckedAt": int(time.time() * 1000),
    }


def _gemini_api_key_dependency() -> dict[str, str]:
    resolution = get_google_api_key_resolution()
    return {
        "name": "gemini_api_key",
        "transport": "env+secret-manager",
        "status": str(resolution["status"]),
        "source": str(resolution["source"]),
    }


def _gemini_runtime_dependency() -> dict[str, str]:
    runtime = get_gemini_runtime_diagnostics()
    dependency = {
        "name": "gemini_runtime",
        "transport": "vertex-sdk-or-proxy",
        "status": str(runtime["status"]),
        "source": str(runtime["source"]),
        "detail": str(runtime.get("detail") or ""),
    }
    return dependency


def _probe_grpc_sidecar_status(
    timeout_seconds: float = _GRPC_HEALTH_PROBE_TIMEOUT_SECONDS,
) -> tuple[bool, bool, str]:
    address = _grpc_sidecar_address()
    channel = grpc.insecure_channel(address)
    try:
        grpc.channel_ready_future(channel).result(timeout=timeout_seconds)
        stub = llm_pb2_grpc.LLMServiceStub(channel)
        response = stub.Health(llm_pb2.HealthRequest(), timeout=timeout_seconds)
        if response.ok:
            return True, True, ""
        return True, False, response.error or "gRPC sidecar reported not ready"
    except Exception as exc:
        return False, False, str(exc)
    finally:
        channel.close()


def _probe_grpc_transport_ready(
    timeout_seconds: float = _GRPC_HEALTH_PROBE_TIMEOUT_SECONDS,
) -> tuple[bool, str]:
    address = _grpc_sidecar_address()
    channel = grpc.insecure_channel(address)
    try:
        grpc.channel_ready_future(channel).result(timeout=timeout_seconds)
        return True, ""
    except Exception as exc:
        return False, str(exc) or exc.__class__.__name__
    finally:
        channel.close()


async def _grpc_sidecar_ready(
    timeout_seconds: float = _GRPC_HEALTH_PROBE_TIMEOUT_SECONDS,
) -> tuple[bool, str]:
    if _grpc_disabled():
        return True, ""
    return await asyncio.to_thread(
        _probe_grpc_transport_ready, timeout_seconds
    )


async def _grpc_sidecar_health(
    timeout_seconds: float = _GRPC_HEALTH_PROBE_TIMEOUT_SECONDS,
) -> tuple[str, str]:
    if _grpc_disabled():
        return "disabled", ""

    transport_ready, model_ready, detail = await asyncio.to_thread(
        _probe_grpc_sidecar_status, timeout_seconds
    )
    if not transport_ready:
        return "unavailable", detail or "gRPC sidecar transport unavailable"
    if model_ready:
        return "ok", ""
    return "degraded", detail or "gRPC sidecar models unavailable"


async def _wait_for_grpc_sidecar_ready(
    timeout_seconds: float = _GRPC_STARTUP_TIMEOUT_SECONDS,
) -> None:
    deadline = asyncio.get_running_loop().time() + timeout_seconds
    last_error = "gRPC sidecar readiness probe failed"
    while True:
        if _grpc_failed.is_set():
            raise RuntimeError("gRPC sidecar thread failed during startup")

        ready, error = await _grpc_sidecar_ready()
        if ready:
            logger.info("grpc_sidecar_ready", address=_grpc_sidecar_address())
            return
        last_error = error or last_error
        if asyncio.get_running_loop().time() >= deadline:
            raise RuntimeError(f"gRPC sidecar readiness timeout: {last_error}")
        await asyncio.sleep(_GRPC_RETRY_INTERVAL_SECONDS)


async def _grpc_heartbeat_task() -> None:
    """Periodically check gRPC health without terminating the HTTP sidecar."""
    if _grpc_disabled():
        return
    consecutive_failures = 0
    max_failures = 3
    await asyncio.sleep(5)  # Initial grace period
    while True:
        if _grpc_failed.is_set():
            logger.error("grpc_heartbeat_failed", reason="event_set")
            return

        ready, error = await _grpc_sidecar_ready()
        if ready:
            consecutive_failures = 0
        else:
            consecutive_failures += 1
            runtime = _gemini_runtime_dependency()
            logger.warning(
                "grpc_heartbeat_degraded",
                failure_count=consecutive_failures,
                error=error,
                gemini_runtime_status=runtime["status"],
                gemini_runtime_source=runtime["source"],
                gemini_runtime_detail=runtime["detail"],
            )

        if consecutive_failures >= max_failures:
            logger.error(
                "grpc_heartbeat_persistent_degradation",
                reason="max_failures_reached",
                failure_count=consecutive_failures,
            )
            consecutive_failures = 0

        await asyncio.sleep(5)


# ── Application lifespan ─────────────────────────────────────────────────────


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    """Application lifespan: start background services, yield, then shut down."""
    logger.info(
        "wisdev_sidecar_starting",
        version=_SERVICE_VERSION,
        project=str(get_project_id_resolution().get("projectId") or "unset"),
        project_source=str(get_project_id_resolution().get("projectSource") or "none"),
        gemini_api_key_source=_GEMINI_API_KEY_RESOLUTION["source"],
        gemini_api_key_status=_GEMINI_API_KEY_RESOLUTION["status"],
    )
    _update_warmup_state(
        state="starting",
        firstCallReady=False,
        detail="startup",
        lastProbeAtMs=0,
        probeEnabled=_warm_probe_enabled(),
    )
    if _grpc_disabled():
        _update_warmup_state(
            state="disabled",
            firstCallReady=False,
            detail="grpc disabled",
            lastProbeAtMs=_startup_age_ms(),
        )
        logger.warning(
            "grpc_sidecar_disabled", reason="PYTHON_SIDECAR_DISABLE_GRPC enabled"
        )
        yield
        logger.info("wisdev_sidecar_stopping")
        return

    try:
        require_proto_runtime_compatibility()
    except Exception as exc:
        logger.error(
            "wisdev_sidecar_proto_contract_failed",
            error=str(exc),
            diagnostics=get_proto_runtime_diagnostics(),
        )
        raise

    global _grpc_thread
    _grpc_failed.clear()
    _grpc_thread = threading.Thread(target=_start_grpc_server, daemon=True)
    _grpc_thread.start()

    try:
        await _wait_for_grpc_sidecar_ready()
    except Exception as exc:
        logger.error("wisdev_sidecar_startup_failed", error=str(exc))
        raise

    await _run_gemini_warm_probe()

    # Start the heartbeat task
    heartbeat = asyncio.create_task(_grpc_heartbeat_task())

    yield
    heartbeat.cancel()
    with suppress(asyncio.CancelledError):
        await heartbeat
    logger.info("wisdev_sidecar_stopping")


def create_app() -> FastAPI:
    app = FastAPI(
        title="WisDev LLM Sidecar",
        version=_SERVICE_VERSION,
        lifespan=lifespan,
    )

    # Wire the OTel ASGI middleware BEFORE any route decorators so that every
    # request gets a span from the moment it enters the Python process.
    # The FastAPIInstrumentor reads the incoming W3C traceparent header
    # (injected by the Go orchestrator's otelhttp layer) and continues the
    # existing trace.
    FastAPIInstrumentor.instrument_app(
        app,
        excluded_urls="/health,/healthz,/readiness",
    )

    # This sidecar is called only by the Go orchestrator (internal, service-to-service).
    # CORS is configured narrowly: the wildcard below is intentionally permissive for
    # local development, but credentials are disabled (allow_credentials=False) to
    # satisfy browser CORS requirements and prevent `allow_origins=["*"]` +
    # `allow_credentials=True` from being a misconfiguration.
    # In production the Go orchestrator does not use a browser origin, so these
    # headers are never exercised by real browser clients.
    app.add_middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_credentials=False,
        allow_methods=["GET", "POST", "OPTIONS"],
        allow_headers=[
            "Content-Type",
            "Authorization",
            "X-Internal-Service-Key",
            "X-Trace-Id",
        ],
    )

    app.state.limiter = limiter
    app.add_exception_handler(RateLimitExceeded, _rate_limit_exceeded_handler)
    app.include_router(llm_router)
    app.include_router(ml_router)
    app.include_router(agent_router)
    app.include_router(azure_compute_router)
    app.include_router(skill_registry_router)
    app.include_router(wisdev_action_router, prefix="/wisdev")
    # Compatibility shim for older Go-side fallback clients that called the
    # Python action stubs before the canonical /wisdev prefix was restored.
    app.include_router(wisdev_action_router)

    # trace_id_middleware is registered FIRST so it runs outermost in
    # Starlette/FastAPI's reverse-registration order (last registered = innermost).
    # This ensures every response — including auth rejections — carries X-Trace-Id.
    @app.middleware("http")
    async def trace_id_middleware(request: Request, call_next):
        traceparent = request.headers.get("traceparent", "")
        trace_id = ""
        if traceparent:
            parts = traceparent.split("-")
            if len(parts) >= 2 and len(parts[1]) == 32:
                trace_id = parts[1]
        if not trace_id:
            trace_id = uuid4().hex
        request.state.trace_id = trace_id
        response = await call_next(request)
        response.headers["X-Trace-Id"] = trace_id
        return response

    # internal_key_middleware is registered SECOND so it runs innermost
    # (after trace_id_middleware has set request.state.trace_id and will later
    # set the X-Trace-Id response header).
    @app.middleware("http")
    async def internal_key_middleware(request: Request, call_next):
        # Skip auth for health checks and root
        if request.url.path in ["/", "/health", "/healthz", "/readiness", "/metrics"]:
            return await call_next(request)

        internal_key = os.environ.get("INTERNAL_SERVICE_KEY")
        oidc_audience = os.environ.get("OIDC_AUDIENCE")

        if not internal_key and not oidc_audience:
            # If not configured, allow access (dev mode) but log warning
            logger.warning("No internal auth configured, skipping check")
            return await call_next(request)

        auth_header = request.headers.get("Authorization", "")
        provided_key = request.headers.get("X-Internal-Service-Key", "")

        # 1. Try OIDC ID Token if audience is set
        if oidc_audience and auth_header.startswith("Bearer "):
            id_token_str = auth_header[7:]
            try:
                import asyncio
                from google.oauth2 import id_token
                from google.auth.transport import requests as google_requests

                # verify_oauth2_token makes a blocking HTTP request to fetch Google's
                # public keys. Run it in a thread pool to avoid blocking the event loop.
                await asyncio.to_thread(
                    id_token.verify_oauth2_token,
                    id_token_str,
                    google_requests.Request(),
                    oidc_audience,
                )
                return await call_next(request)  # Success
            except Exception as e:
                logger.warning("oidc_token_verification_failed", error=str(e))
                # Fall through to check internal key if verification fails

        # 2. Try static internal service key
        if not provided_key and auth_header.startswith("Bearer "):
            provided_key = auth_header[7:]

        if internal_key and provided_key == internal_key:
            return await call_next(request)  # Success

        trace_id = getattr(request.state, "trace_id", "")
        logger.warning("unauthorized_access_attempt", path=request.url.path)
        # Return 401 (unauthenticated), not 403 (authenticated but forbidden).
        # Include X-Trace-Id so auth failures are correlated in logs.
        return JSONResponse(
            status_code=401,
            content={
                "error": {
                    "code": "UNAUTHORIZED",
                    "message": "Invalid service credentials",
                }
            },
            headers={"X-Trace-Id": trace_id} if trace_id else {},
        )

    @app.exception_handler(Exception)
    async def handle_general_exception(request: Request, exc: Exception):
        logger.error("unhandled_exception", path=request.url.path, detail=str(exc))
        return JSONResponse(
            status_code=500,
            content={"error": {"code": "INTERNAL_ERROR", "message": str(exc)}},
        )

    @app.exception_handler(HTTPException)
    async def handle_http_exception(request: Request, exc: HTTPException):
        if isinstance(exc.detail, dict):
            detail = exc.detail
            content = {"detail": detail, **detail}
        else:
            content = {"detail": str(exc.detail)}
        return JSONResponse(status_code=exc.status_code, content=content)

    @app.get("/")
    async def root():
        return {
            "service": "wisdev-sidecar",
            "version": _SERVICE_VERSION,
            "wisdev": get_wisdev_runtime_profile(),
        }

    @app.get("/health")
    async def health_check(request: Request):
        dependencies: list[dict[str, str]] = []
        status = "ok"
        status_code = 200
        grpc_ready = False

        redis_client = await semantic_cache._get_redis()
        if redis_client is not None:
            try:
                await redis_client.ping()
                dependencies.append(
                    {"name": "redis", "transport": "tcp", "status": "ok"}
                )
            except Exception as exc:
                status = "degraded"
                dependencies.append(
                    {"name": "redis", "transport": "tcp", "status": f"error:{exc}"}
                )
        else:
            dependencies.append(
                {"name": "redis", "transport": "tcp", "status": "disabled"}
            )

        dependencies.append(_gemini_api_key_dependency())
        dependencies.append(_gemini_runtime_dependency())

        if _grpc_disabled():
            dependencies.append(
                {
                    "name": "grpc_sidecar",
                    "transport": "grpc-protobuf",
                    "status": "disabled",
                }
            )
        else:
            grpc_status, grpc_detail = await _grpc_sidecar_health()
            grpc_ready = grpc_status in {"ok", "degraded"}
            if _grpc_failed.is_set():
                status = "degraded"
                status_code = 503
                dependencies.append(
                    {
                        "name": "grpc_sidecar",
                        "transport": "grpc-protobuf",
                        "status": "thread_crashed",
                    }
                )
            elif grpc_status == "unavailable":
                status = "degraded"
                status_code = 503
                dependencies.append(
                    {
                        "name": "grpc_sidecar",
                        "transport": "grpc-protobuf",
                        "status": grpc_detail or "unavailable",
                    }
                )
            elif grpc_status == "degraded":
                status = "degraded"
                dependencies.append(
                    {
                        "name": "grpc_sidecar",
                        "transport": "grpc-protobuf",
                        "status": grpc_detail or "models_unavailable",
                    }
                )
            else:
                dependencies.append(
                    {
                        "name": "grpc_sidecar",
                        "transport": "grpc-protobuf",
                        "status": "ok",
                    }
                )

        content = _runtime_envelope(status, dependencies)
        content["wisdev"] = get_wisdev_runtime_profile()
        content["warmup"] = _warmup_snapshot(grpc_ready=grpc_ready)
        return JSONResponse(status_code=status_code, content=content)

    @app.get("/readiness")
    async def readiness():
        grpc_ready = False
        if _grpc_disabled():
            content = _runtime_envelope(
                "ok",
                [
                    _gemini_api_key_dependency(),
                    _gemini_runtime_dependency(),
                    {
                        "name": "grpc_sidecar",
                        "transport": "grpc-protobuf",
                        "status": "disabled",
                    },
                ],
            )
            content["wisdev"] = get_wisdev_runtime_profile()
            content["warmup"] = _warmup_snapshot(grpc_ready=grpc_ready)
            return content
        grpc_status, grpc_detail = await _grpc_sidecar_health()
        grpc_ready = grpc_status in {"ok", "degraded"}
        if _grpc_failed.is_set() or grpc_status == "unavailable":
            content = _runtime_envelope(
                "degraded",
                [
                    _gemini_api_key_dependency(),
                    _gemini_runtime_dependency(),
                    {
                        "name": "grpc_sidecar",
                        "transport": "grpc-protobuf",
                        "status": "thread_crashed"
                        if _grpc_failed.is_set()
                        else (grpc_detail or grpc_status),
                    },
                ],
            )
            content["wisdev"] = get_wisdev_runtime_profile()
            content["warmup"] = _warmup_snapshot(grpc_ready=grpc_ready)
            return JSONResponse(
                status_code=503,
                content=content,
            )
        if grpc_status == "degraded":
            content = _runtime_envelope(
                "degraded",
                [
                    _gemini_api_key_dependency(),
                    _gemini_runtime_dependency(),
                    {
                        "name": "grpc_sidecar",
                        "transport": "grpc-protobuf",
                        "status": grpc_detail or "models_unavailable",
                    },
                ],
            )
            content["wisdev"] = get_wisdev_runtime_profile()
            content["warmup"] = _warmup_snapshot(grpc_ready=grpc_ready)
            return content
        content = _runtime_envelope(
            "ok",
            [
                _gemini_api_key_dependency(),
                _gemini_runtime_dependency(),
                {"name": "grpc_sidecar", "transport": "grpc-protobuf", "status": "ok"},
            ],
        )
        content["wisdev"] = get_wisdev_runtime_profile()
        content["warmup"] = _warmup_snapshot(grpc_ready=grpc_ready)
        return content

    @app.get("/metrics")
    async def metrics():
        return {"caches": {"semantic": semantic_cache.stats()}}

    return app


# ── FastAPI application ───────────────────────────────────────────────────────

app = create_app()


def _http_bind_host() -> str:
    host = os.environ.get("HOST", "127.0.0.1").strip()
    return host or "127.0.0.1"


def _http_bind_port() -> int:
    raw_port = str(
        os.environ.get("PORT", "") or resolve_listen_port("python_sidecar", "http")
    ).strip()
    try:
        return int(raw_port)
    except ValueError as exc:
        raise SystemExit(f"Invalid PORT value: {raw_port!r}") from exc


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host=_http_bind_host(), port=_http_bind_port())

"""
WisDev Python Sidecar
Handles LLM integrations, prompt rendering, and ML primitives for the WisDev agent.

Sidecar Endpoints:
- /ml/pdf   — PDF extraction (HTTP)
- /ml/embed — Text embeddings (HTTP)
- /skills/register — dynamic skill registration (HTTP)
- gRPC      — LLM generation (port 50051)
"""

import asyncio
import os
import threading
from contextlib import asynccontextmanager
from typing import AsyncGenerator
from uuid import uuid4

# ── Observability bootstrap (MUST precede all other imports that log/trace) ───
# configure_telemetry() registers the OTel TracerProvider and reconfigures
# structlog with GCP trace correlation before any request handling begins.
from telemetry import configure_telemetry

_SERVICE_VERSION = "1.1.0"
configure_telemetry(service_version=_SERVICE_VERSION)

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
from proto import llm_v1_pb2 as llm_pb2
from proto import llm_v1_pb2_grpc as llm_pb2_grpc
from routers.ml_router import router as ml_router
from routers.agent_router import router as agent_router
from services.dynamic_skill_registry import router as skill_registry_router
from services.semantic_cache import semantic_cache

logger = structlog.get_logger(__name__)

_grpc_thread: threading.Thread | None = None
_grpc_failed = threading.Event()


_GRPC_HEALTH_PROBE_TIMEOUT_SECONDS = 1.0
_GRPC_STARTUP_TIMEOUT_SECONDS = 10.0
_GRPC_RETRY_INTERVAL_SECONDS = 0.25


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
        # we must exit so the process manager (Container Service) restarts us.
        # os._exit is used to bypass any cleanup that might hang.
        import os

        os._exit(1)


def _grpc_sidecar_address() -> str:
    port = os.environ.get("GRPC_PORT", "50052")
    return f"127.0.0.1:{port}"


def _probe_grpc_sidecar(
    timeout_seconds: float = _GRPC_HEALTH_PROBE_TIMEOUT_SECONDS,
) -> tuple[bool, str]:
    address = _grpc_sidecar_address()
    channel = grpc.insecure_channel(address)
    try:
        grpc.channel_ready_future(channel).result(timeout=timeout_seconds)
        stub = llm_pb2_grpc.LLMServiceStub(channel)
        response = stub.Health(llm_pb2.HealthRequest(), timeout=timeout_seconds)
        if response.ok:
            return True, ""
        return False, response.error or "gRPC sidecar reported not ready"
    except Exception as exc:
        return False, str(exc)
    finally:
        channel.close()


async def _grpc_sidecar_ready(
    timeout_seconds: float = _GRPC_HEALTH_PROBE_TIMEOUT_SECONDS,
) -> tuple[bool, str]:
    return await asyncio.to_thread(_probe_grpc_sidecar, timeout_seconds)


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
    """Periodically check gRPC health and terminate process if it fails."""
    consecutive_failures = 0
    max_failures = 3
    await asyncio.sleep(5)  # Initial grace period
    while True:
        if _grpc_failed.is_set():
            logger.error("grpc_heartbeat_failed", reason="event_set")
            os._exit(1)

        ready, error = await _grpc_sidecar_ready()
        if ready:
            consecutive_failures = 0
        else:
            consecutive_failures += 1
            logger.warning(
                "grpc_heartbeat_degraded",
                failure_count=consecutive_failures,
                error=error,
            )

        if consecutive_failures >= max_failures:
            logger.error("grpc_heartbeat_failed", reason="max_failures_reached")
            os._exit(1)

        await asyncio.sleep(5)


# ── Application lifespan ─────────────────────────────────────────────────────


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    """Application lifespan: start background services, yield, then shut down."""
    logger.info(
        "wisdev_sidecar_starting",
        version=_SERVICE_VERSION,
    )
    global _grpc_thread
    _grpc_failed.clear()
    _grpc_thread = threading.Thread(target=_start_grpc_server, daemon=True)
    _grpc_thread.start()

    try:
        await _wait_for_grpc_sidecar_ready()
    except Exception as exc:
        logger.error("wisdev_sidecar_startup_failed", error=str(exc))
        raise

    # Start the heartbeat task
    heartbeat = asyncio.create_task(_grpc_heartbeat_task())

    yield
    heartbeat.cancel()
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

    app.add_middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )

    app.state.limiter = limiter
    app.add_exception_handler(RateLimitExceeded, _rate_limit_exceeded_handler)
    app.include_router(ml_router)
    app.include_router(agent_router)
    app.include_router(skill_registry_router)

    @app.middleware("http")
    async def internal_key_middleware(request: Request, call_next):
        if request.url.path in ["/", "/health", "/healthz", "/readiness", "/metrics"]:
            return await call_next(request)

        internal_key = os.environ.get("INTERNAL_SERVICE_KEY")

        if not internal_key:
            return await call_next(request)

        provided_key = request.headers.get("X-Internal-Service-Key", "")
        auth_header = request.headers.get("Authorization", "")
        if not provided_key and auth_header.startswith("Bearer "):
            provided_key = auth_header[7:]

        if provided_key == internal_key:
            return await call_next(request)

        logger.warn("unauthorized_access_attempt", path=request.url.path)
        return JSONResponse(
            status_code=403,
            content={
                "error": {"code": "FORBIDDEN", "message": "Invalid service credentials"}
            },
        )

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
        return {"service": "wisdev-sidecar", "version": _SERVICE_VERSION}

    @app.get("/health")
    @limiter.limit("60/minute")
    async def health_check(request: Request):
        grpc_state: str | dict[str, str] = "connected"
        status = "healthy"
        status_code = 200

        grpc_ready, grpc_error = await _grpc_sidecar_ready()
        if _grpc_failed.is_set():
            grpc_state = {"error": "gRPC thread crashed"}
            status = "unhealthy"
            status_code = 503
        elif not grpc_ready:
            grpc_state = {"error": grpc_error or "gRPC sidecar not responding"}
            status = "unhealthy"
            status_code = 503

        content = {
            "status": status,
            "service": "wisdev-sidecar",
            "version": _SERVICE_VERSION,
            "dependencies": {"grpc": grpc_state},
        }
        return JSONResponse(status_code=status_code, content=content)

    @app.get("/readiness")
    async def readiness():
        grpc_ready, grpc_error = await _grpc_sidecar_ready()
        if _grpc_failed.is_set() or not grpc_ready:
            return JSONResponse(
                status_code=503,
                content={
                    "status": "not_ready",
                    "service": "wisdev-sidecar",
                    "reason": "gRPC thread crashed"
                    if _grpc_failed.is_set()
                    else (grpc_error or "grpc_unavailable"),
                },
            )
        return {
            "status": "ready",
            "service": "wisdev-sidecar",
            "version": _SERVICE_VERSION,
        }
        return JSONResponse(status_code=status_code, content=content)

    @app.get("/readiness")
    async def readiness():
        grpc_ready, grpc_error = await _grpc_sidecar_ready()
        if _grpc_failed.is_set() or not grpc_ready:
            return JSONResponse(
                status_code=503,
                content={
                    "status": "not_ready",
                    "service": "wisdev-sidecar",
                    "reason": "gRPC thread crashed"
                    if _grpc_failed.is_set()
                    else (grpc_error or "grpc_unavailable"),
                },
            )
        return {
            "status": "ready",
            "service": "wisdev-sidecar",
            "version": _SERVICE_VERSION,
        }

    @app.get("/metrics")
    async def metrics():
        return {"caches": {"semantic": semantic_cache.stats()}}

    return app


# ── FastAPI application ───────────────────────────────────────────────────────

app = create_app()

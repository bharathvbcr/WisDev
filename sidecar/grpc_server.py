"""
WisDev LLM Sidecar gRPC Server.

Provides primitives for LLM generation, embeddings, and structured output.
Legacy AcademicCapabilityService (v2) has been decommissioned.
"""

import asyncio
import json
import logging
import os
import time
from typing import Any, AsyncIterator, cast

import grpc
from proto import require_generated_module, require_proto_runtime_compatibility

require_proto_runtime_compatibility()
llm_pb2 = require_generated_module("llm_pb2")
llm_pb2_grpc = cast(Any, require_generated_module("llm_pb2_grpc"))

from services.gemini_service import (
    GeminiService,
    GEMINI_HEAVY_MODEL,
    GEMINI_LIGHT_MODEL,
    get_gemini_runtime_diagnostics,
)
from services.llm_runtime import SERVER_VERSION, LLMRuntime, LLMRuntimeError

logger = logging.getLogger(__name__)


def split_stream_text(text: str) -> list[str]:
    """Split text into chunks for streaming simulation."""
    if not text:
        return []
    
    # Try splitting by double newline first
    if "\n\n" in text:
        parts = text.split("\n\n")
        chunks = [p + "\n\n" for p in parts[:-1]]
        if parts[-1]:
            chunks.append(parts[-1])
        return chunks
        
    # Fallback to fixed-length chunking for large contiguous strings
    # This ensures that contract tests expecting multiple chunks for long output pass.
    chunk_size = 128
    return [text[i:i+chunk_size] for i in range(0, len(text), chunk_size)]


def _log(event: str, **fields) -> None:
    if fields:
        logger.info("%s | %s", event, json.dumps(fields, sort_keys=True, default=str))
        return
    logger.info(event)


def _log_error(event: str, **fields) -> None:
    if fields:
        logger.error("%s | %s", event, json.dumps(fields, sort_keys=True, default=str))
        return
    logger.error(event)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_server_start_time = 0.0


async def _abort_with_typed_error(
    context,
    status_code: grpc.StatusCode,
    code: str,
    message: str,
    trace_id: str,
    http_status: int = 500,
    details: dict | None = None,
):
    """Abort with a JSON error envelope in the details field."""
    envelope: dict[str, Any] = {
        "ok": False,
        "traceId": trace_id,
        "error": {
            "code": code,
            "message": message,
            "status": http_status,
            "traceId": trace_id,
        },
    }
    if details:
        envelope["error"]["details"] = details

    await context.abort(status_code, json.dumps(envelope))


def _ensure_non_empty_prompt(prompt: str, trace_id: str) -> str:
    prompt = prompt.strip()
    if not prompt:
        raise ValueError(f"empty prompt for trace_id={trace_id or 'unknown'}")
    return prompt


def _get_trace_id(request) -> str:
    """Extract trace_id from request metadata map."""
    if hasattr(request, "metadata") and request.metadata:
        return request.metadata.get("trace_id", "")
    return ""


def _context_metadata(context) -> dict[str, str]:
    if context is None or not hasattr(context, "invocation_metadata"):
        return {}
    return dict(context.invocation_metadata())


def _validate_internal_key_sync(metadata: dict, internal_key: str, oidc_audience: str) -> None:
    """Synchronous credential check — called via asyncio.to_thread from async handlers.

    Separated from the async wrapper so the blocking OIDC HTTP call does not
    execute on the event loop thread.
    """
    # 1. Try OIDC ID Token if audience is set (production preference)
    if oidc_audience:
        auth_header = metadata.get("authorization", "")
        if auth_header.startswith("Bearer "):
            id_token_str = auth_header[7:]
            try:
                from google.oauth2 import id_token
                from google.auth.transport import requests as google_requests
                id_token.verify_oauth2_token(id_token_str, google_requests.Request(), oidc_audience)
                return  # Success
            except Exception as e:
                _log("oidc_token_verification_failed", error=str(e))
                # Fall through to check internal key

    # 2. Try static internal service key (legacy/internal)
    provided_key = metadata.get("internal_service_key", "")
    if internal_key and provided_key == internal_key:
        return  # Success

    raise PermissionError("Unauthorized: missing or invalid service credentials")


async def _validate_internal_key(request, context) -> None:
    """Async credential validator — safe to await in gRPC handler coroutines.

    Runs the blocking OIDC token verification in a thread-pool executor via
    asyncio.to_thread so that it does not stall the event loop under load.
    """
    internal_key = os.environ.get("INTERNAL_SERVICE_KEY", "")
    oidc_audience = os.environ.get("OIDC_AUDIENCE", "")

    if not internal_key and not oidc_audience:
        return

    metadata = dict(context.invocation_metadata())
    # Delegate the potentially-blocking OIDC HTTP call to a thread.
    await asyncio.to_thread(_validate_internal_key_sync, metadata, internal_key, oidc_audience)


class LLMServiceServicer(llm_pb2_grpc.LLMServiceServicer):  # type: ignore[name-defined, misc, valid-type]
    """Implementation of the sidecar LLMService (v1)."""

    def __init__(self, runtime: LLMRuntime | None = None):
        self.runtime = runtime or LLMRuntime()
        self.default_gemini = self.runtime._default_gemini

    async def Generate(self, request, context):
        try:
            await _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), _get_trace_id(request), 403
            )
            return

        try:
            return await self.runtime.generate(
                request,
                metadata=_context_metadata(context),
                validate_credentials=False,
            )
        except LLMRuntimeError as exc:
            await _abort_with_typed_error(
                context, exc.grpc_status, exc.code, exc.message, exc.trace_id, exc.http_status, exc.details
            )
            return

    async def GenerateStream(self, request, context):
        try:
            await _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), _get_trace_id(request), 403
            )
            return

        try:
            async for chunk in self.runtime.generate_stream(
                request,
                metadata=_context_metadata(context),
                validate_credentials=False,
            ):
                yield chunk
        except LLMRuntimeError as exc:
            await _abort_with_typed_error(
                context, exc.grpc_status, exc.code, exc.message, exc.trace_id, exc.http_status, exc.details
            )

    async def StructuredOutput(self, request, context):
        try:
            await _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), _get_trace_id(request), 403
            )
            return

        try:
            return await self.runtime.structured_output(
                request,
                metadata=_context_metadata(context),
                validate_credentials=False,
            )
        except LLMRuntimeError as exc:
            await _abort_with_typed_error(
                context, exc.grpc_status, exc.code, exc.message, exc.trace_id, exc.http_status, exc.details
            )
            return

    async def Embed(self, request, context):
        try:
            await _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), _get_trace_id(request), 403
            )
            return

        try:
            return await self.runtime.embed(
                request,
                metadata=_context_metadata(context),
                validate_credentials=False,
            )
        except LLMRuntimeError as exc:
            await _abort_with_typed_error(
                context, exc.grpc_status, exc.code, exc.message, exc.trace_id, exc.http_status, exc.details
            )
            return

    async def EmbedBatch(self, request, context):
        try:
            await _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), _get_trace_id(request), 403
            )
            return

        try:
            return await self.runtime.embed_batch(
                request,
                metadata=_context_metadata(context),
                validate_credentials=False,
            )
        except LLMRuntimeError as exc:
            await _abort_with_typed_error(
                context, exc.grpc_status, exc.code, exc.message, exc.trace_id, exc.http_status, exc.details
            )
            return

    async def Health(self, request, context):
        diagnostics = get_gemini_runtime_diagnostics()
        is_ready = bool(diagnostics.get("ready"))
        error_detail = str(diagnostics.get("detail") or "Gemini runtime unavailable")
        error_message = ""
        if not is_ready:
            context_parts: list[str] = []
            if diagnostics.get("source") is not None:
                context_parts.append(f"source={diagnostics.get('source')}")
            if diagnostics.get("mode") is not None:
                context_parts.append(f"mode={diagnostics.get('mode')}")
            if diagnostics.get("projectConfigured") is not None:
                context_parts.append(f"projectConfigured={diagnostics.get('projectConfigured')}")
            if diagnostics.get("proxyConfigured") is not None:
                context_parts.append(f"proxyConfigured={diagnostics.get('proxyConfigured')}")

            if context_parts:
                error_message = f"{error_detail} ({', '.join(context_parts)})"
            else:
                error_message = error_detail
        return llm_pb2.HealthResponse(
            ok=is_ready,
            version=SERVER_VERSION,
            models_available=[GEMINI_LIGHT_MODEL, GEMINI_HEAVY_MODEL],
            error=error_message,
        )


# ---------------------------------------------------------------------------
# Server lifecycle
# ---------------------------------------------------------------------------

async def serve_async(interceptors=None):
    global _server_start_time
    explicit_addr = os.environ.get("PYTHON_SIDECAR_GRPC_ADDR", "").strip()
    if explicit_addr:
        bind_target = explicit_addr
    else:
        bind_target = "[::]:" + os.environ.get("GRPC_PORT", "50052")
    # Grace period (seconds) allowed for in-flight RPCs to complete after
    # SIGTERM before the server is forcefully stopped.
    grace_seconds = int(os.environ.get("GRPC_SHUTDOWN_GRACE_SECONDS", "10"))

    # Pass OTel server interceptor when provided so inbound gRPC calls from Go
    # continue the existing Cloud Trace trace rather than starting a new root span.
    server = grpc.aio.server(interceptors=interceptors or [])

    # Register LLMService (sidecar contract).
    llm_pb2_grpc.add_LLMServiceServicer_to_server(LLMServiceServicer(), server)

    server.add_insecure_port(bind_target)
    await server.start()
    _server_start_time = time.time()
    logging.info(
        f"Starting WisDev LLM Sidecar (Async) on {bind_target} "
        f"| version={SERVER_VERSION} | otel_interceptor={'yes' if interceptors else 'no'}"
    )

    # Register a SIGTERM handler so Cloud Run revision replacements (which
    # send SIGTERM) drain in-flight RPCs rather than dropping them.
    loop = asyncio.get_running_loop()

    async def _graceful_shutdown(sig_name: str) -> None:
        logging.info(f"gRPC server received {sig_name}; draining with {grace_seconds}s grace period")
        await server.stop(grace=grace_seconds)

    import signal
    for sig in (signal.SIGTERM, signal.SIGINT):
        try:
            loop.add_signal_handler(
                sig,
                lambda s=sig: asyncio.ensure_future(_graceful_shutdown(s.name)),
            )
        except (NotImplementedError, RuntimeError):
            # Signal handlers are not available on Windows or in threads.
            pass

    await server.wait_for_termination()


def serve(interceptors=None):
    """Entry point to run the async server.

    Args:
        interceptors: Optional list of gRPC interceptors. Pass the OTel
            interceptor from ``telemetry.make_grpc_server_interceptor()`` to
            propagate trace context from Go into Python gRPC handler spans.
    """
    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    loop.run_until_complete(serve_async(interceptors=interceptors))


if __name__ == '__main__':
    logging.basicConfig(level=logging.INFO)
    serve()
